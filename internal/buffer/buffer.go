// Package buffer provides bounded RAM windows backed optionally by sparse files.
package buffer

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

const (
	blockSize         = 1 << 20 // matches the natural unit of streaming HTTP/NNTP chunks
	defaultMemorySize = 32 << 20
)

var (
	// ErrNotPresent means at least one byte of the range was never written,
	// was Discarded, or was dropped under memory pressure with no disk tier.
	ErrNotPresent = errors.New("buffer: range not present")
	ErrClosed     = errors.New("buffer: closed")
	ErrOutOfRange = errors.New("buffer: offset/length out of range")
	ErrDiskLimit  = errors.New("buffer: disk limit reached")
)

// Range is a half-open byte interval [Off, Off+Size).
type Range struct {
	Off, Size int64
}

// Stats reports counters for observability.
type Stats struct {
	BlocksInRAM    int
	BytesInRAM     int64
	BytesPresent   int64
	BytesOnDisk    int64
	Hits           int64 // reads served from RAM
	Misses         int64 // reads that had to touch the disk tier
	DiskWrites     int64 // block flushes
	Evictions      int64
	HolesPunched   int64
	BytesReclaimed int64
}

// Config configures a new Buffer.
type Config struct {
	// MemorySize is this Buffer's RAM window: the bytes it may hold in blocks
	// before evicting one. Default 32 MB; clamped up to one block.
	MemorySize int64

	// DiskPath enables the persistence tier. Empty means RAM only — no file is
	// created and evicted bytes are reported through OnEvict instead.
	DiskPath string

	// TotalSize is the logical size, used to pre-size the sparse file. 0 if
	// unknown.
	TotalSize int64

	// InitialRanges seeds the tracker with ranges already in the DiskPath file
	// from a prior run. Ignored without a DiskPath.
	InitialRanges []Range

	// InitialRangesPreaccounted means the Pool's InitialDiskUsage already
	// includes InitialRanges, so reopening this file must not count them again.
	InitialRangesPreaccounted bool

	// PersistentDisk keeps on-disk bytes charged to the Pool after Close. The
	// owner must call Pool.ReleaseDisk only when it actually removes the file.
	PersistentDisk bool

	// ImmutableDisk writes complete, non-overlapping ranges directly to the
	// disk tier. It avoids a duplicate userspace RAM window for content that is
	// published only after WriteAt returns.
	ImmutableDisk bool

	// CanDrop vetoes eviction of the blocks covering a range. It runs with the
	// Buffer lock held and must not call back into the Buffer.
	CanDrop func(off, length int64) bool

	// OnEvict reports ranges lost under pressure. It runs without the Buffer
	// lock and is not called for persisted or explicitly discarded ranges.
	OnEvict func(off, length int64)

	// OnPersistChange reports a change to PersistedRanges. It is called with the
	// Buffer lock held and must not call back into the Buffer.
	OnPersistChange func()
}

// Buffer is a byte cache over one stream. Safe for concurrent use.
type Buffer struct {
	pool            *Pool
	onEvict         func(off, length int64)
	onPersistChange func()
	canDrop         func(off, length int64) bool

	file *os.File
	// fileMu keeps a hole punch from racing a flush or Close while b.mu is
	// deliberately released around the punch syscall.
	fileMu sync.RWMutex

	// mu guards blocks, bytesInRAM, ranges and onDisk. Readers take RLock and
	// hold it across a disk-tier pread so an eviction can't move the ground
	// under them.
	mu             sync.RWMutex
	blocks         map[int64]*block
	bytesInRAM     int64
	maxBytes       int64
	immutableDisk  bool
	persistentDisk bool

	ranges *rangeSet // present anywhere
	onDisk *rangeSet // bytes physically represented in the file
	dirty  *rangeSet // resident bytes newer than, or absent from, the file

	// readHead is the consumer's position (see SetReadHead). Eviction steers
	// away from it and the pool's disk backstop reclaims behind it.
	readHead atomic.Int64

	// punchable latches false once the filesystem refuses to release blocks;
	// see punch.go.
	punchable atomic.Bool

	// alloc owns this Buffer's block memory, returning it to the OS promptly
	// rather than on the GC's schedule (see alloc.go).
	alloc blockAllocator

	closed        atomic.Bool
	dropBehindPos atomic.Int64

	statsHits       atomic.Int64
	statsMisses     atomic.Int64
	statsDiskWrites atomic.Int64
	statsEvictions  atomic.Int64
	statsPunches    atomic.Int64
	statsReclaimed  atomic.Int64
}

func newBuffer(p *Pool, cfg Config) (*Buffer, error) {
	if cfg.TotalSize < 0 {
		return nil, ErrOutOfRange
	}
	for _, r := range cfg.InitialRanges {
		if r.Size < 0 || r.Off < 0 || r.Off > math.MaxInt64-r.Size {
			return nil, ErrOutOfRange
		}
	}
	if cfg.MemorySize <= 0 {
		cfg.MemorySize = defaultMemorySize
	}
	if cfg.MemorySize < blockSize {
		cfg.MemorySize = blockSize
	}

	b := &Buffer{
		pool:            p,
		onEvict:         cfg.OnEvict,
		onPersistChange: cfg.OnPersistChange,
		canDrop:         cfg.CanDrop,
		blocks:          make(map[int64]*block),
		ranges:          newRangeSet(),
		onDisk:          newRangeSet(),
		dirty:           newRangeSet(),
		maxBytes:        cfg.MemorySize,
	}
	b.alloc.maxFree = min(max(int(b.maxBytes/blockSize), 1), maxReuseBlocks)

	if cfg.DiskPath == "" {
		return b, nil
	}

	if dir := filepath.Dir(cfg.DiskPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("buffer: create disk dir: %w", err)
		}
	}
	file, err := os.OpenFile(cfg.DiskPath, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("buffer: open disk file: %w", err)
	}
	// Sparse first: on NTFS the truncate below would otherwise reserve every
	// cluster of a multi-GB file. It also gates punching (see punch.go).
	b.punchable.Store(prepareSparse(file) == nil)
	if cfg.TotalSize > 0 {
		if err := file.Truncate(cfg.TotalSize); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("buffer: truncate disk file: %w", err)
		}
	}
	b.file = file
	b.immutableDisk = cfg.ImmutableDisk
	b.persistentDisk = cfg.PersistentDisk
	for _, r := range cfg.InitialRanges {
		if r.Size == 0 {
			continue
		}
		b.ranges.insert(r.Off, r.Size)
		if cfg.InitialRangesPreaccounted {
			b.onDisk.insert(r.Off, r.Size)
		} else {
			b.insertDisk(r.Off, r.Size)
		}
	}
	adviseSequential(file)
	return b, nil
}

// WriteAt writes p at off. The bytes are readable as soon as it returns.
func (b *Buffer) WriteAt(p []byte, off int64) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off > math.MaxInt64-int64(len(p)) {
		return 0, ErrOutOfRange
	}
	if b.immutableDisk {
		n, err := b.writeImmutableAt(p, off)
		if n == 0 && errors.Is(err, ErrDiskLimit) && b.pool.reclaimForWrite(int64(len(p))) {
			return b.writeImmutableAt(p, off)
		}
		return n, err
	}

	end := off + int64(len(p))
	for cur := off; cur < end; {
		blockOff := alignDown(cur)
		lo := int(cur - blockOff)
		hi := int(min(end, blockEnd(blockOff)) - blockOff)
		srcLo := int(cur - off)
		err := b.writeRegion(blockOff, lo, hi, p[srcLo:srcLo+(hi-lo)])
		if errors.Is(err, ErrDiskLimit) && b.pool.reclaimForWrite(int64(blockSize)) {
			err = b.writeRegion(blockOff, lo, hi, p[srcLo:srcLo+(hi-lo)])
		}
		if err != nil {
			return srcLo, err
		}
		cur += int64(hi - lo)
	}
	return len(p), nil
}

func (b *Buffer) writeImmutableAt(p []byte, off int64) (int, error) {
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return 0, ErrClosed
	}
	reserved, reserveErr := b.reserveDiskLocked(off, int64(len(p)))
	if reserveErr != nil {
		b.mu.Unlock()
		return 0, fmt.Errorf("buffer: reserve immutable disk write at %d: %w", off, reserveErr)
	}
	b.fileMu.RLock()
	n, err := b.file.WriteAt(p, off)
	b.fileMu.RUnlock()
	if n > 0 {
		b.ranges.insert(off, int64(n))
		b.commitReservedDiskLocked(off, int64(n), reserved)
		b.persistChangedLocked()
		b.statsDiskWrites.Add(1)
	} else {
		b.pool.subDisk(reserved)
	}
	b.mu.Unlock()
	if err != nil {
		return n, fmt.Errorf("buffer: immutable disk write at %d: %w", off, err)
	}
	if n != len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

// writeRegion copies src into the block at blockOff, admitting it (and
// evicting to make room) if it isn't resident.
func (b *Buffer) writeRegion(blockOff int64, lo, hi int, src []byte) error {
	b.mu.Lock()
	// Re-check under the lock: a write racing Close must not allocate blocks
	// or publish ranges after Close settled this Buffer's pool accounting.
	if b.closed.Load() {
		b.mu.Unlock()
		return ErrClosed
	}
	blk, ok := b.blocks[blockOff]
	var lost []Range
	if !ok {
		var err error
		if blk, lost, err = b.acquireBlockLocked(blockOff); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	copy(blk.data[lo:hi], src)
	writeOff, writeLen := blockOff+int64(lo), int64(hi-lo)
	b.ranges.insert(writeOff, writeLen)
	if b.file != nil {
		b.dirty.insert(writeOff, writeLen)
		b.persistChangedLocked()
	}
	b.mu.Unlock()

	for _, r := range lost {
		b.onEvict(r.Off, r.Size)
	}
	return nil
}

// WriteMissing writes only the bytes of p that are not already present,
// returning how many it skipped.
func (b *Buffer) WriteMissing(p []byte, off int64) (skipped int, err error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off > math.MaxInt64-int64(len(p)) {
		return 0, ErrOutOfRange
	}

	b.mu.RLock()
	overlaps := b.ranges.anyPresent(off, int64(len(p)))
	b.mu.RUnlock()
	if !overlaps {
		_, err = b.WriteAt(p, off)
		return 0, err
	}

	// Rare: another downloader filled part of this range concurrently.
	b.mu.RLock()
	present := b.ranges.presentRanges(off, int64(len(p)))
	b.mu.RUnlock()

	cur := off
	for _, r := range present {
		if r.Off > cur {
			if _, err = b.WriteAt(p[cur-off:r.Off-off], cur); err != nil {
				return skipped, err
			}
		}
		skipped += int(r.Size)
		cur = r.Off + r.Size
	}
	if end := off + int64(len(p)); cur < end {
		_, err = b.WriteAt(p[cur-off:], cur)
	}
	return skipped, err
}

// ReadAt fills p from off. Every byte must be present, else ErrNotPresent.
// Resident blocks are copied; the rest come from the disk tier.
func (b *Buffer) ReadAt(p []byte, off int64) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 || off > math.MaxInt64-int64(len(p)) {
		return 0, ErrOutOfRange
	}

	end := off + int64(len(p))
	fromDisk := false

	b.mu.RLock()
	defer b.mu.RUnlock()
	if !b.ranges.present(off, int64(len(p))) {
		return 0, ErrNotPresent
	}
	for cur := off; cur < end; {
		blockOff := alignDown(cur)
		lo := int(cur - blockOff)
		hi := int(min(end, blockEnd(blockOff)) - blockOff)
		dst := p[cur-off : cur-off+int64(hi-lo)]

		if blk, ok := b.blocks[blockOff]; ok {
			copy(dst, blk.data[lo:hi])
		} else {
			// Present but not resident means it was flushed to the file.
			if b.file == nil {
				return int(cur - off), io.ErrUnexpectedEOF
			}
			fromDisk = true
			n, err := b.file.ReadAt(dst, cur)
			if err != nil {
				if errors.Is(err, io.EOF) {
					err = io.ErrUnexpectedEOF
				}
				return int(cur-off) + n, fmt.Errorf("buffer: disk read at %d: %w", cur, err)
			}
			if n != len(dst) {
				return int(cur-off) + n, io.ErrUnexpectedEOF
			}
		}
		cur += int64(hi - lo)
	}

	if fromDisk {
		b.statsMisses.Add(1)
	} else {
		b.statsHits.Add(1)
	}
	return len(p), nil
}

// Discard releases [off, off+length). After it returns, reads of those bytes
// report ErrNotPresent until they are written again.
func (b *Buffer) Discard(off, length int64) error {
	if b.closed.Load() {
		return ErrClosed
	}
	if length == 0 {
		return nil
	}
	if off < 0 || length < 0 || off > math.MaxInt64-length {
		return ErrOutOfRange
	}
	b.discard(off, length, false)
	return nil
}

// discard drops [off, off+length) from both tiers and returns the bytes
// reclaimed from the file. It does not fire onEvict — a caller-initiated
// Discard already knows what it released.
func (b *Buffer) discard(off, length int64, requireReclaim bool) int64 {
	end := off + length

	if requireReclaim {
		if !b.mu.TryLock() {
			return 0
		}
	} else {
		b.mu.Lock()
	}
	if b.closed.Load() {
		b.mu.Unlock()
		return 0
	}
	if requireReclaim && (b.file == nil || !b.punchable.Load()) {
		b.mu.Unlock()
		return 0
	}
	for blkOff := alignDown(off); blkOff < end; blkOff = blockEnd(blkOff) {
		if blk, ok := b.blocks[blkOff]; ok && blkOff >= off && blockEnd(blkOff) <= end {
			b.dropBlockLocked(blk)
		}
	}
	b.ranges.remove(off, length)
	b.dirty.remove(off, length)
	// A boundary block may have just lost its last present byte.
	for _, blkOff := range [2]int64{alignDown(off), alignDown(end - 1)} {
		if blk, ok := b.blocks[blkOff]; ok && !b.ranges.anyPresent(blkOff, blockEnd(blkOff)-blkOff) {
			b.dropBlockLocked(blk)
		}
	}

	if b.file == nil {
		b.mu.Unlock()
		return 0
	}
	b.persistChangedLocked()
	if !b.punchable.Load() {
		b.mu.Unlock()
		return 0
	}
	// Snapshot for the undo below; unpublish before punching so a reader sees
	// ErrNotPresent rather than preading a half-made hole. Keep the physical
	// bytes accounted until the hole punch succeeds, so a concurrent writer
	// cannot reserve space that the filesystem never actually released.
	onDisk := b.onDisk.presentRanges(off, length)
	b.fileMu.Lock()
	if len(onDisk) == 0 {
		b.fileMu.Unlock()
		b.mu.Unlock()
		return 0
	}
	err := punchHole(b.file, off, length)
	if err == nil {
		removed := b.removeDisk(off, length)
		adviseDontNeed(b.file, off, length)
		b.fileMu.Unlock()
		b.mu.Unlock()
		b.statsPunches.Add(1)
		b.statsReclaimed.Add(removed)
		return removed
	}
	b.fileMu.Unlock()
	if errors.Is(err, errPunchUnsupported) {
		b.punchable.Store(false)
	}

	// The bytes remain allocated and accounted. Pool-driven reclaim restores
	// logical presence, while an explicit Discard stays discarded.
	if !b.closed.Load() {
		if requireReclaim {
			for _, r := range onDisk {
				b.ranges.insert(r.Off, r.Size)
			}
		}
		b.persistChangedLocked()
	}
	b.mu.Unlock()
	return 0
}

// punchBehindWindow reclaims file space below readHead-backWindow. Called by
// the Pool when it is over its DiskLimit.
func (b *Buffer) punchBehindWindow(backWindow int64) int64 {
	if b.closed.Load() || b.file == nil || !b.punchable.Load() {
		return 0
	}
	head := b.readHead.Load()
	ceiling := head - backWindow
	if head <= 0 || ceiling <= 0 {
		return 0
	}
	if !b.mu.TryRLock() {
		return 0
	}
	present := b.persistedRangesLocked(0, ceiling)
	b.mu.RUnlock()

	var reclaimed int64
	for _, r := range present {
		if n := b.discard(r.Off, r.Size, true); n > 0 {
			reclaimed += n
			if b.onEvict != nil {
				b.onEvict(r.Off, r.Size)
			}
		}
	}
	if reclaimed > 0 {
		b.pool.statsPunches.Add(1)
		b.pool.statsReclaimed.Add(reclaimed)
	}
	return reclaimed
}

// insertDisk records file-resident bytes and mirrors them into the pool's disk
// accounting. Caller holds b.mu.
func (b *Buffer) insertDisk(off, length int64) {
	if added := b.onDisk.insert(off, length); added > 0 {
		b.pool.addDisk(added)
	}
}

// reserveDiskLocked reserves the not-yet-present part of a disk write. Caller
// holds b.mu, which keeps the coverage calculation stable until commit.
func (b *Buffer) reserveDiskLocked(off, length int64) (int64, error) {
	reserved := length - b.onDisk.coverage(off, off+length)
	if err := b.pool.reserveDisk(reserved); err != nil {
		return 0, err
	}
	return reserved, nil
}

// commitReservedDiskLocked publishes a completed write and releases any
// reservation left unused by a partial or overlapping write. Caller holds
// b.mu.
func (b *Buffer) commitReservedDiskLocked(off, length, reserved int64) {
	added := b.onDisk.insert(off, length)
	if added < reserved {
		b.pool.subDisk(reserved - added)
	} else if added > reserved {
		// Defensive fallback for callers that supplied an undersized
		// reservation; production write paths calculate under the same lock.
		b.pool.addDisk(added - reserved)
	}
}

// removeDisk is insertDisk's inverse. Caller holds b.mu.
func (b *Buffer) removeDisk(off, length int64) int64 {
	removed := b.onDisk.remove(off, length)
	if removed > 0 {
		b.pool.subDisk(removed)
	}
	return removed
}

func (b *Buffer) persistChangedLocked() {
	if b.onPersistChange != nil {
		b.onPersistChange()
	}
}

// HasRange reports whether [off, off+length) is fully present.
func (b *Buffer) HasRange(off, length int64) bool {
	if b.closed.Load() || off < 0 || length <= 0 || off > math.MaxInt64-length {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ranges.present(off, length)
}

// FindMissing returns [off, off+length) with its present prefix trimmed: what
// the caller must still fetch before it can read from off.
func (b *Buffer) FindMissing(off, length int64) Range {
	if b.closed.Load() || off < 0 || length <= 0 || off > math.MaxInt64-length {
		return Range{Off: off}
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	o, n := b.ranges.missingTail(off, length)
	return Range{Off: o, Size: n}
}

// PersistedRanges returns the subranges of [off, off+length) that are in the
// file. This is what an owner must record to describe the cache across a
// restart: Present also covers bytes that only exist in the RAM window, and a
// crash would leave those claimed but absent, so a reopen would read holes
// back as zeros instead of reporting them missing. Unlike other accessors, it
// remains available after Close so owners can record the final immutable state.
func (b *Buffer) PersistedRanges(off, length int64) []Range {
	if off < 0 || length <= 0 || off > math.MaxInt64-length || b.file == nil {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.persistedRangesLocked(off, length)
}

// persistedRangesLocked returns logical, clean file ranges. Caller holds b.mu.
func (b *Buffer) persistedRangesLocked(off, length int64) []Range {
	var out []Range
	for _, disk := range b.onDisk.presentRanges(off, length) {
		for _, live := range b.ranges.presentRanges(disk.Off, disk.Size) {
			out = append(out, b.dirty.missingWithin(live.Off, live.Size)...)
		}
	}
	return out
}

// Present returns the present subranges within [off, off+length).
func (b *Buffer) Present(off, length int64) []Range {
	if b.closed.Load() || off < 0 || length <= 0 || off > math.MaxInt64-length {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ranges.presentRanges(off, length)
}

// DropBehind releases the page cache for the file region more than margin
// bytes behind offset. The bytes stay on disk, so a longer seek-back re-reads
// locally instead of re-downloading. Monotonic; no-op without a disk tier.
func (b *Buffer) DropBehind(offset, margin int64) {
	if b.closed.Load() || margin <= 0 || offset <= margin || b.file == nil {
		return
	}
	target := offset - margin
	for {
		prev := b.dropBehindPos.Load()
		if target <= prev {
			return
		}
		if b.dropBehindPos.CompareAndSwap(prev, target) {
			adviseDropBehind(b.file, prev, target)
			return
		}
	}
}

// SetReadHead publishes the consumer's position. Eviction steers away from it
// and the pool's disk backstop reclaims behind it. Deliberately not monotonic:
// pulling it back on a seek re-protects the region now being read.
func (b *Buffer) SetReadHead(off int64) {
	b.readHead.Store(max(off, 0))
	if limit := b.pool.diskLimit.Load(); limit > 0 && b.pool.diskInUse.Load() >= limit {
		b.pool.signalDiskEvict()
	}
}

// Share reports the RAM this Buffer may hold: its MemorySize, reduced to its
// weighted slice of the pool budget while other Buffers are open. Owners use
// it to size how far they read ahead.
func (b *Buffer) Share() int64 { return b.pool.shareFor(b) }

// Stats returns the current counters.
func (b *Buffer) Stats() Stats {
	b.mu.RLock()
	blocks, inRAM := len(b.blocks), b.bytesInRAM
	present, onDisk := b.ranges.totalSize(), b.onDisk.totalSize()
	b.mu.RUnlock()
	return Stats{
		BlocksInRAM:    blocks,
		BytesInRAM:     inRAM,
		BytesPresent:   present,
		BytesOnDisk:    onDisk,
		Hits:           b.statsHits.Load(),
		Misses:         b.statsMisses.Load(),
		DiskWrites:     b.statsDiskWrites.Load(),
		Evictions:      b.statsEvictions.Load(),
		HolesPunched:   b.statsPunches.Load(),
		BytesReclaimed: b.statsReclaimed.Load(),
	}
}

// Flush writes every dirty resident byte to the disk tier.
func (b *Buffer) Flush() error {
	if b.closed.Load() {
		return ErrClosed
	}
	if b.file == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed.Load() {
		return ErrClosed
	}
	for _, blk := range b.blocks {
		if err := b.flushBlockLocked(blk); err != nil {
			return err
		}
	}
	return nil
}

// Close releases every resident block, drains the allocator, and closes the
// disk tier.
func (b *Buffer) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}

	// The exclusive lock excludes readers, so no goroutine can be touching a
	// block's memory as it is unmapped.
	b.mu.Lock()
	var flushErr error
	for off, blk := range b.blocks {
		if b.file != nil {
			if err := b.flushBlockLocked(blk); err != nil && flushErr == nil {
				flushErr = err
			}
		}
		munmapBlock(blk.bufPtr)
		delete(b.blocks, off)
	}
	b.pool.dropBytes(b.bytesInRAM)
	b.bytesInRAM = 0
	if n := b.onDisk.totalSize(); n > 0 && !b.persistentDisk {
		b.pool.subDisk(n)
	}
	// Keep the range trackers as an immutable final snapshot. DFS records the
	// persisted subset after Close, when pool reclamation can no longer race it.
	b.mu.Unlock()
	b.pool.remove(b)
	b.alloc.drain()

	if b.file == nil {
		return flushErr
	}
	// Owners routinely remove the file after Close; cached pages are then
	// dead weight.
	b.fileMu.Lock()
	adviseDontNeedAll(b.file)
	err := b.file.Close()
	b.fileMu.Unlock()
	if flushErr != nil {
		return flushErr
	}
	return err
}

// acquireBlockLocked returns the resident block at blockOff, evicting to make
// room and faulting in any of its bytes that live on disk. Returns the ranges
// that were lost outright (no disk tier) for the caller to report via onEvict
// after releasing the lock. Caller holds b.mu.
func (b *Buffer) acquireBlockLocked(blockOff int64) (*block, []Range, error) {
	if blk, ok := b.blocks[blockOff]; ok {
		return blk, nil, nil
	}

	var lost []Range
	for b.overBudgetLocked() {
		victim := b.pickVictimLocked()
		if victim == nil {
			// Nothing droppable (empty, or every block pinned). Allocate
			// anyway — a bounded overshoot of one block per writer beats
			// spinning under the lock.
			break
		}
		evicted, err := b.evictLocked(victim)
		if err != nil {
			return nil, lost, err
		}
		lost = append(lost, evicted...)
	}

	bufPtr := b.alloc.get()
	blk := &block{off: blockOff, data: (*bufPtr)[:blockSize], bufPtr: bufPtr}
	b.blocks[blockOff] = blk
	b.bytesInRAM += blockSize
	b.pool.addBlock()

	// Anything of this block already in the file must be faulted in, or a read
	// of it would find the block resident and copy out zeros.
	if b.file != nil {
		for _, r := range b.onDisk.presentRanges(blockOff, blockEnd(blockOff)-blockOff) {
			dst := blk.data[r.Off-blockOff : r.Off-blockOff+r.Size]
			n, err := b.file.ReadAt(dst, r.Off)
			if err != nil || n != len(dst) {
				b.dropBlockLocked(blk)
				if errors.Is(err, io.EOF) || err == nil {
					err = io.ErrUnexpectedEOF
				}
				return nil, lost, fmt.Errorf("buffer: fault in at %d: %w", r.Off, err)
			}
		}
	}
	return blk, lost, nil
}

func (b *Buffer) overBudgetLocked() bool {
	if b.bytesInRAM+blockSize > b.maxBytes {
		return true
	}
	// The pool share only binds while the pool is actually short, so a Buffer
	// at or under its share keeps its window and the hoarders give memory back
	// (see Pool.reclaimMemory).
	return b.pool.wouldExceedMemory() && b.bytesInRAM+blockSize > b.pool.shareFor(b)
}

// evictLocked removes one block from RAM, flushing dirty bytes first when a
// disk tier exists. Caller holds b.mu.
func (b *Buffer) evictLocked(victim *block) ([]Range, error) {
	if b.file == nil {
		length := blockEnd(victim.off) - victim.off
		lost := b.ranges.presentRanges(victim.off, length)
		b.ranges.remove(victim.off, length)
		b.dropBlockLocked(victim)
		b.statsEvictions.Add(1)
		if b.onEvict == nil {
			return nil, nil
		}
		return lost, nil
	}

	if err := b.flushBlockLocked(victim); err != nil {
		return nil, err
	}
	b.dropBlockLocked(victim)
	b.statsEvictions.Add(1)
	return nil, nil
}

// flushBlockLocked writes the dirty portions of blk. Caller holds b.mu.
func (b *Buffer) flushBlockLocked(blk *block) error {
	for _, r := range b.dirty.presentRanges(blk.off, blockEnd(blk.off)-blk.off) {
		reserved, err := b.reserveDiskLocked(r.Off, r.Size)
		if err != nil {
			return fmt.Errorf("buffer: reserve disk write at %d: %w", r.Off, err)
		}
		lo := r.Off - blk.off
		b.fileMu.Lock()
		n, writeErr := b.file.WriteAt(blk.data[lo:lo+r.Size], r.Off)
		b.fileMu.Unlock()
		if n > 0 {
			b.commitReservedDiskLocked(r.Off, int64(n), reserved)
			b.dirty.remove(r.Off, int64(n))
			b.persistChangedLocked()
		} else {
			b.pool.subDisk(reserved)
		}
		if writeErr != nil {
			return fmt.Errorf("buffer: disk write at %d: %w", r.Off, writeErr)
		}
		if int64(n) != r.Size {
			return fmt.Errorf("buffer: disk write at %d: %w", r.Off, io.ErrShortWrite)
		}
		b.statsDiskWrites.Add(1)
	}
	return nil
}

// pickVictimLocked chooses the resident block whose loss hurts least, judged
// against the read head: furthest behind it first (already-played history),
// else furthest ahead (deepest prefetch). Never the block nearest the head —
// write-order LRU picked exactly that one whenever the consumer trailed the
// download frontier, stalling playback in a re-fetch loop. Caller holds b.mu.
func (b *Buffer) pickVictimLocked() *block {
	head := b.readHead.Load()
	var behind, ahead *block
	for _, blk := range b.blocks {
		length := blockEnd(blk.off) - blk.off
		if b.canDrop != nil && !b.canDrop(blk.off, length) {
			continue // a reader holds these bytes
		}
		if blockEnd(blk.off) <= head {
			if behind == nil || blk.off < behind.off {
				behind = blk
			}
		} else if ahead == nil || blk.off > ahead.off {
			ahead = blk
		}
	}
	if behind != nil {
		return behind
	}
	return ahead
}

// trimTo drops blocks until this Buffer holds at most target bytes, returning
// how much it gave back. Called by the Pool's reclaim worker so RAM comes from
// whoever is hoarding it rather than from whoever happens to be writing.
func (b *Buffer) trimTo(target int64) int64 {
	if b.closed.Load() {
		return 0
	}
	target = max(target, blockSize)

	var (
		lost  []Range
		freed int64
	)
	b.mu.Lock()
	if b.closed.Load() {
		b.mu.Unlock()
		return 0
	}
	for b.bytesInRAM > target {
		victim := b.pickVictimLocked()
		if victim == nil {
			break // everything left is pinned by an in-flight read
		}
		before := b.bytesInRAM
		evicted, err := b.evictLocked(victim)
		if err != nil {
			break
		}
		lost = append(lost, evicted...)
		if b.bytesInRAM == before {
			break // the flush failed; stop rather than spin
		}
		freed += before - b.bytesInRAM
	}
	b.mu.Unlock()

	for _, r := range lost {
		b.onEvict(r.Off, r.Size)
	}
	return freed
}

// dropBlockLocked removes a block and returns its memory to the allocator.
// Callers must have settled the range trackers first. Caller holds b.mu.
func (b *Buffer) dropBlockLocked(blk *block) {
	delete(b.blocks, blk.off)
	b.bytesInRAM -= blockSize
	b.pool.dropBytes(blockSize)
	b.alloc.put(blk.bufPtr)
}

func alignDown(off int64) int64 { return off &^ (blockSize - 1) }

func blockEnd(off int64) int64 {
	if off > math.MaxInt64-blockSize {
		return math.MaxInt64
	}
	return off + blockSize
}
