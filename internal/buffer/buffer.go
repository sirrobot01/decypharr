// Package buffer is a high-performance byte buffer with a bounded
// in-memory ring and a sparse disk-backing file.
//
// Conceptually: a sparse, addressable byte buffer where recent writes are
// held in RAM (fast re-reads, write coalescing) and older or evicted ones
// spill to disk. The buffer knows nothing about networks, downloaders,
// segments, or FUSE — it's pure storage. Callers wrap it with whatever
// policy they need.
//
// Architecture
//
//   - Fixed 1 MB blocks, internal constant. Aligns with SSD page granularity
//     and the natural unit of streaming HTTP/NNTP chunks.
//   - LRU-managed block cache bounded by Config.MemorySize. Hot blocks
//     stay; cold ones are evicted and flushed if dirty.
//   - Sparse disk file pre-truncated to Config.TotalSize (when known) so
//     WriteAt at any offset within the logical range is valid without
//     growing the file lazily.
//   - Async flush worker pool. WriteAt returns when data is in RAM; the
//     flusher persists to disk in the background. Eviction blocks only if
//     it needs to wait for a still-dirty block.
//   - Discard releases bytes from RAM AND from disk via fallocate(PUNCH_HOLE)
//     on Linux. On tmpfs this directly returns RAM to the kernel.
//
// Range tracker
//
// A rangeSet maintains the set of byte ranges that are present anywhere
// (RAM or disk). ReadAt consults this first: any byte in the requested
// range that's not present causes ErrNotPresent. This is a stricter contract
// than zero-fill semantics, and is what callers building caches on top
// actually want — they need to distinguish "data was never written" from
// "data was written as zero".
package buffer

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Block size and default RAM budget are internal constants. The package
// shouldn't expose tuning knobs callers won't know how to set; if a future
// workload needs different defaults we'll adjust here based on data.
const (
	blockSize         = 1 << 20 // 1 MB
	blockSizeLog2     = 20      // matches blockSize; used to index the per-block state slot
	defaultMemorySize = 32 << 20

	// Per-block fast-path states. The states slice lets ReadAt skip mu,
	// the range tree, and the block-map entirely when every block in the
	// requested range is "fully on disk, not RAM-resident" — turning the
	// hot path into a bare pread, the same syscall sequence baseline uses.
	stateSlow     uint32 = 0 // anything else: take the locked slow path
	stateFastDisk uint32 = 1 // fully present on disk, no RAM block — bare pread is safe

	// promoteThreshold is the number of disk-served reads after which a
	// block becomes a promotion candidate. 2 = "first repeat triggers
	// promotion" — the earliest signal that more than one reader cares.
	// Single-shot workloads (probe, random, disjoint stripes) never cross
	// the threshold and pay nothing more than one atomic.Add per read.
	promoteThreshold uint32 = 2

	// promoteQueueDepth bounds the hint channel. Full → drop the hint —
	// the worker is already amortizing well so the marginal hint matters
	// less than not blocking the reading goroutine that produced it.
	promoteQueueDepth = 32
)

// Sentinel errors returned by Buffer methods.
var (
	// ErrNotPresent indicates ReadAt covered a byte range where at least
	// one byte was never written or was subsequently Discarded.
	ErrNotPresent = errors.New("buffer: range not present")
	// ErrClosed is returned by any operation on a Buffer after Close.
	ErrClosed = errors.New("buffer: closed")
	// ErrOutOfRange is returned for negative offsets or sizes.
	ErrOutOfRange = errors.New("buffer: offset/length out of range")
)

// Range describes a contiguous half-open byte interval [Off, Off+Size).
type Range struct {
	Off, Size int64
}

// Stats reports counters for observability. Returned by Buffer.Stats.
type Stats struct {
	BlocksInRAM    int
	BytesInRAM     int64
	BytesPresent   int64 // sum of all present ranges (RAM + disk)
	Hits           int64 // ReadAt fully served from RAM
	PartialHits    int64 // ReadAt mixed RAM and disk
	Misses         int64 // ReadAt served entirely from disk
	Flushes        int64
	Evictions      int64
	WritesThrough  int64 // WriteAt calls that bypassed the cache under pressure
	Promotions     int64 // disk-served reads that promoted a block into RAM
	HolesPunched   int64
	BytesReclaimed int64
}

// Config configures a new Buffer.
type Config struct {
	// MemorySize is the maximum bytes held in RAM across all blocks. When
	// allocating a new block would exceed this, the LRU evictor flushes
	// and reclaims the oldest clean block. Default 64 MB when zero.
	MemorySize int64

	// DiskPath is the file path for the sparse disk-backing file. If
	// empty, a temp file in os.TempDir() is created and removed on Close.
	DiskPath string

	// TotalSize is the logical size of the buffer for sparse-file
	// pre-truncation. Setting it lets WriteAt at any offset in [0, TotalSize)
	// land in the pre-allocated sparse file without lazy growth. Set to 0
	// if unknown; the file grows naturally as bytes are written.
	TotalSize int64

	// InitialRanges, if non-nil, seeds the range tracker with the given
	// half-open ranges. Used by callers reopening an existing DiskPath
	// whose persisted metadata says which byte ranges are valid on disk
	// from prior runs — without this seed the buffer would treat the file
	// as empty and ReadAt would return ErrNotPresent for already-cached
	// data. Ranges may overlap or be out of order; the tracker normalizes.
	InitialRanges []Range
}

// Buffer is the public type. Methods are safe for concurrent use.
type Buffer struct {
	cfg Config

	file     *os.File
	diskTemp bool // remove the file on Close

	// mu guards blocks, the LRU list, bytesInRAM, and ranges. Reads
	// (ReadAt, HasRange, Present, Stats) take RLock so multiple readers
	// can hit RAM-cached blocks or pread from disk in parallel. Writers
	// (WriteAt cache path, Discard, eviction) take Lock.
	//
	// The write-through publish path is the one carved-out exception: it
	// holds mu.RLock (shared — does not stall concurrent readers) while
	// inserting into ranges, since RLock still excludes the exclusive
	// Lock that block creation would need. See writeRegion.
	//
	// Caller contract: do not WriteAt/Discard and ReadAt the same byte
	// range concurrently. Both call sites (Usenet SegmentCache, DFS
	// CacheItem) enforce this — segments transition to OnDisk before
	// reads, DFS uses HasRange before serving.
	mu         sync.RWMutex
	blocks     map[int64]*block // keyed by block.off
	lruHead    *block           // most recently used
	lruTail    *block           // least recently used
	bytesInRAM int64
	maxBytes   int64

	ranges *rangeSet

	// states is a per-block fast-path state slot, indexed by
	// blockOff >> blockSizeLog2. Only allocated when TotalSize is known
	// at New(). See stateSlow/stateFastDisk: when every block in a read
	// range carries stateFastDisk, ReadAt bypasses mu, the range tree,
	// and the block map and just pread's — matching baseline's hot path
	// exactly. Transitions are written under mu but read lock-free, so
	// the fast path adds nothing more than one atomic.Load per block.
	states []atomic.Uint32

	// readCounts is the per-block disk-read counter, indexed identically
	// to states. Every disk-served read does an atomic.Add(1); when the
	// counter crosses promoteThreshold we enqueue a promote hint. Single-
	// reader workloads keep the counter at 1 forever and never enqueue,
	// so the only overhead they pay is the atomic.Add itself.
	readCounts []atomic.Uint32

	// promoteCh receives block offsets the read path wants promoted into
	// the RAM cache. The worker (promoteLoop) drains it, loading each
	// block from disk OUTSIDE the exclusive lock and then taking mu.Lock
	// only for the install. Bounded so a backlogged worker drops hints
	// rather than back-pressuring readers.
	promoteCh   chan int64
	promoteStop chan struct{}
	promoteWg   sync.WaitGroup

	// evictionMinOff is a hint from the caller: blocks with offset < this
	// value are evictable, blocks with offset >= this value are protected
	// from eviction (they're in the active sliding-window the reader still
	// cares about). Zero means "no hint, fall back to pure LRU".
	evictionMinOff atomic.Int64

	// blockPool is per-Buffer rather than package-global. Per-Buffer
	// matches the Usenet/DFS usage pattern: each Buffer is one file
	// (minutes-to-hours lifetime) with a large per-file working set. A
	// global pool would either retain freed blocks across Buffers —
	// holding hundreds of MB of RAM and displacing the kernel page cache
	// that disk reads rely on — or be GC-drained periodically (giving no
	// real cross-Buffer reuse). Per-Buffer keeps memory accounted to the
	// file that owns it; when Close drops the Buffer, GC reclaims the
	// pool's contents cleanly.
	blockPool sync.Pool

	closed atomic.Bool

	// Stats counters. Atomic to allow Stats() without holding mu.
	statsHits         atomic.Int64
	statsPartialHits  atomic.Int64
	statsMisses       atomic.Int64
	statsFlushes      atomic.Int64
	statsEvictions    atomic.Int64
	statsWriteThrough atomic.Int64
	statsPromotions   atomic.Int64
	statsPunches      atomic.Int64
	statsReclaimed    atomic.Int64
}

// New creates a Buffer with the given configuration. The disk-backing
// file is created and (if TotalSize is set) pre-truncated.
func New(cfg Config) (*Buffer, error) {
	if cfg.MemorySize <= 0 {
		cfg.MemorySize = defaultMemorySize
	}
	if cfg.TotalSize < 0 {
		return nil, ErrOutOfRange
	}

	var (
		file     *os.File
		diskTemp bool
		err      error
	)
	if cfg.DiskPath == "" {
		file, err = os.CreateTemp("", "buffer-*")
		diskTemp = true
	} else {
		if dir := filepath.Dir(cfg.DiskPath); dir != "" && dir != "." {
			if err = os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("buffer: create disk dir: %w", err)
			}
		}
		file, err = os.OpenFile(cfg.DiskPath, os.O_RDWR|os.O_CREATE, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("buffer: open disk file: %w", err)
	}

	if cfg.TotalSize > 0 {
		if err := file.Truncate(cfg.TotalSize); err != nil {
			_ = file.Close()
			if diskTemp {
				_ = os.Remove(file.Name())
			}
			return nil, fmt.Errorf("buffer: truncate disk file: %w", err)
		}
	}

	b := &Buffer{
		cfg:      cfg,
		file:     file,
		diskTemp: diskTemp,
		blocks:   make(map[int64]*block),
		maxBytes: cfg.MemorySize,
		ranges:   newRangeSet(),
	}
	if cfg.TotalSize > 0 {
		n := int((cfg.TotalSize + blockSize - 1) / blockSize)
		b.states = make([]atomic.Uint32, n)
		b.readCounts = make([]atomic.Uint32, n)
	}
	if cfg.MemorySize >= blockSize {
		b.promoteCh = make(chan int64, promoteQueueDepth)
		b.promoteStop = make(chan struct{})
	}
	b.blockPool.New = func() any {
		buf := make([]byte, blockSize)
		return &buf
	}
	for _, r := range cfg.InitialRanges {
		if r.Size > 0 {
			b.ranges.insert(r.Off, r.Size)
		}
	}
	// Seed fast-path state for any block fully covered by InitialRanges.
	// Cheap one-shot walk; alternative is paying the slow path forever
	// on a reopen of a fully-cached file.
	if b.states != nil {
		for i := range b.states {
			blkOff := int64(i) << blockSizeLog2
			if b.ranges.present(blkOff, blockSize) {
				b.states[i].Store(stateFastDisk)
			}
		}
	}
	// Linux page-cache hint: our workload is dominated by sequential
	// streaming, so let the kernel ramp readahead aggressively. No-op on
	// other platforms.
	adviseSequential(file)
	if b.promoteCh != nil {
		b.promoteWg.Add(1)
		go b.promoteLoop()
	}
	return b, nil
}

// WriteAt writes p at offset off. Returns the number of bytes written.
// Data is visible to ReadAt as soon as WriteAt returns.
//
// Two paths per block region (see writeRegion):
//   - Cached: the block is RAM-resident, or there's RAM budget to make it
//     so. Bytes land in the in-RAM block and are flushed to disk lazily.
//   - Write-through: the block isn't resident and the RAM cache is at its
//     budget. Bytes go straight to disk and the block is not cached. This
//     mirrors a direct pwrite — the right behavior once the working set
//     exceeds the cache, where caching would just thrash. Crucially the
//     pwrite happens OUTSIDE the lock, so concurrent readers aren't
//     stalled behind it the way an under-lock eviction flush would stall
//     them.
func (b *Buffer) WriteAt(p []byte, off int64) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if off < 0 || len(p) == 0 {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, ErrOutOfRange
	}

	end := off + int64(len(p))
	for cur := off; cur < end; {
		blockOff := alignDown(cur)
		blockEnd := blockOff + blockSize
		lo := int(cur - blockOff)
		hi := int(min64(end, blockEnd) - blockOff)
		srcLo := int(cur - off)
		if err := b.writeRegion(blockOff, lo, hi, p[srcLo:srcLo+(hi-lo)]); err != nil {
			return srcLo, err
		}
		cur += int64(hi - lo)
	}
	return len(p), nil
}

// writeRegion writes src into the single block at blockOff covering byte
// range [lo, hi) within that block. Picks the cached or write-through path.
func (b *Buffer) writeRegion(blockOff int64, lo, hi int, src []byte) error {
	// Decide the path under a shared lock. The common write-through case
	// then only takes the exclusive lock once (to publish the range), not
	// twice — taking it here just to read two fields would needlessly
	// serialize against every concurrent reader.
	b.mu.RLock()
	_, resident := b.blocks[blockOff]
	canCache := b.bytesInRAM+blockSize <= b.maxBytes
	b.mu.RUnlock()

	// Resident block, or room to cache one → cached path. Needs the
	// exclusive lock; re-check residency under it since the RLock sample
	// may be stale (acquireBlockLocked returns the existing block if so).
	if resident || canCache {
		b.mu.Lock()
		blk, ok := b.blocks[blockOff]
		if !ok {
			var err error
			if blk, err = b.acquireBlockLocked(blockOff, true); err != nil {
				b.mu.Unlock()
				return err
			}
		}
		err := b.writeIntoBlockLocked(blk, lo, hi, src)
		b.mu.Unlock()
		return err
	}

	// Under cache pressure and not resident → write-through. The range
	// isn't in b.ranges yet, so no reader can fall back to disk for it
	// until we publish below; that lets the pwrite run with no lock.
	diskOff := blockOff + int64(lo)
	if _, err := b.file.WriteAt(src, diskOff); err != nil {
		return fmt.Errorf("buffer: write-through at %d: %w", diskOff, err)
	}
	b.statsWriteThrough.Add(1)

	b.mu.Lock()
	// Rare edge: the cache gained room (e.g. a Discard freed a block) and
	// another writer cached this block while our pwrite was in flight.
	// Mirror our bytes into RAM so the resident block stays authoritative.
	var err error
	if blk, ok := b.blocks[blockOff]; ok {
		err = b.writeIntoBlockLocked(blk, lo, hi, src)
	} else {
		b.ranges.insert(diskOff, int64(hi-lo))
		// Fast-path: if this write completed the block, future reads
		// can skip all locking and go straight to pread.
		b.markStateForBlockLocked(blockOff)
	}
	b.mu.Unlock()
	return err
}

// writeIntoBlockLocked copies src into a RAM-resident block's [lo, hi)
// range, flushing first if the new write isn't contiguous with the
// existing dirty range, then records presence. Caller holds b.mu.
func (b *Buffer) writeIntoBlockLocked(blk *block, lo, hi int, src []byte) error {
	// If this write isn't contiguous with the existing dirty range, we
	// must flush what's there before extending; otherwise the next flush
	// would persist arbitrary block contents between the two.
	if !blk.addDirty(lo, hi) {
		if err := b.flushBlockLocked(blk); err != nil {
			return err
		}
		blk.addDirty(lo, hi)
	}
	copy(blk.data[lo:hi], src)
	b.touchLocked(blk)
	b.ranges.insert(blk.off+int64(lo), int64(hi-lo))
	// The block now has authoritative RAM data — keep fast path off so
	// readers don't pread stale disk bytes instead.
	if slot := b.stateSlot(blk.off); slot != nil {
		slot.Store(stateSlow)
	}
	return nil
}

// ReadAt reads len(p) bytes at offset off. Returns ErrNotPresent if any
// byte in the requested range was never written or was Discarded.
//
// The contract is strict on purpose: callers building caches on top need
// to distinguish "not yet fetched" from "fetched as zero bytes". Zero-fill
// semantics would obscure that.
func (b *Buffer) ReadAt(p []byte, off int64) (int, error) {
	if b.closed.Load() {
		return 0, ErrClosed
	}
	if off < 0 || len(p) == 0 {
		if len(p) == 0 {
			return 0, nil
		}
		return 0, ErrOutOfRange
	}

	end := off + int64(len(p))

	// Fast path: if every block in the read range is stateFastDisk
	// (fully on disk, no RAM block) we can bypass the lock, the range
	// tree, and the block-map entirely and just pread. This is the
	// "match baseline's pread-only hot path bit-for-bit" path, with one
	// atomic.Load per block as the only overhead.
	//
	// Safety relies on the same caller contract that the locked path
	// does: a range being read isn't being concurrently written or
	// Discarded. Under that contract no in-flight state transition can
	// invalidate the snapshot we just took.
	if b.states != nil {
		allFast := true
		for cur := off; cur < end; cur = alignDown(cur) + blockSize {
			slot := b.stateSlot(alignDown(cur))
			if slot == nil || slot.Load() != stateFastDisk {
				allFast = false
				break
			}
		}
		if allFast {
			n, err := b.file.ReadAt(p, off)
			if err != nil && !errors.Is(err, io.EOF) {
				return n, fmt.Errorf("buffer: disk read at %d: %w", off, err)
			}
			// Increment read counters; enqueue a promote hint when any
			// block crosses promoteThreshold. Single-shot reads keep the
			// counter at 1 and never enqueue — zero promoter cost.
			for cur := off; cur < end; cur = alignDown(cur) + blockSize {
				b.hintPromote(alignDown(cur))
			}
			b.statsMisses.Add(1)
			return n, nil
		}
	}

	// Slow path: take the locked path. The presence check and the
	// block/disk reads share one critical section so a concurrent writer
	// can't flip state under us between check and read. Writers mutating
	// the block map, the LRU, or ranges take Lock. Do NOT touchLocked
	// here: it mutates the LRU and would force exclusive Lock.
	b.mu.RLock()
	if !b.ranges.present(off, int64(len(p))) {
		b.mu.RUnlock()
		b.statsMisses.Add(1)
		return 0, ErrNotPresent
	}
	var (
		ramServed  bool
		diskServed bool
	)
	for cur := off; cur < end; {
		blockOff := alignDown(cur)
		blockEnd := blockOff + blockSize

		readLo := int(cur - blockOff)
		readHi := int(min64(end, blockEnd) - blockOff)
		dstLo := int(cur - off)
		dstHi := dstLo + (readHi - readLo)

		if blk, ok := b.blocks[blockOff]; ok {
			copy(p[dstLo:dstHi], blk.data[readLo:readHi])
			ramServed = true
		} else {
			// Disk read. pread is thread-safe; a concurrent write-through
			// pwrite to a *different* range is fine, and the caller
			// contract forbids a concurrent write/discard of *this* range.
			if _, err := b.file.ReadAt(p[dstLo:dstHi], cur); err != nil && !errors.Is(err, io.EOF) {
				b.mu.RUnlock()
				return dstLo, fmt.Errorf("buffer: disk read at %d: %w", cur, err)
			}
			diskServed = true
			// Hint promotion: same counter-gated path as the fast path,
			// just inside the RLock window.
			b.hintPromote(blockOff)
		}

		cur += int64(readHi - readLo)
	}
	b.mu.RUnlock()

	switch {
	case ramServed && !diskServed:
		b.statsHits.Add(1)
	case ramServed && diskServed:
		b.statsPartialHits.Add(1)
	default:
		b.statsMisses.Add(1)
	}
	return len(p), nil
}

// Discard releases the byte range [off, off+length): drops any cached
// blocks (or trims affected portions of straddling blocks) and issues a
// hole-punch on the disk file. After Discard returns, ReadAt for any
// byte in the range returns ErrNotPresent until it is re-written.
//
// Non-blocking with respect to in-flight flushes for unrelated blocks.
// If a dirty block straddles the discard boundary, the surviving portion
// stays dirty and will be flushed normally.
func (b *Buffer) Discard(off, length int64) error {
	if b.closed.Load() {
		return ErrClosed
	}
	if off < 0 || length <= 0 {
		if length == 0 {
			return nil
		}
		return ErrOutOfRange
	}
	end := off + length

	b.mu.Lock()
	for blkOff := alignDown(off); blkOff < end; blkOff += blockSize {
		blk, ok := b.blocks[blkOff]
		if !ok {
			continue
		}
		blockEnd := blkOff + blockSize
		// A block fully inside the discard range is dropped entirely.
		// A block straddling the boundary keeps the surviving portion.
		if blkOff >= off && blockEnd <= end {
			b.dropBlockLocked(blk)
			continue
		}
		// Partial discard within a block: trim the block's dirty range
		// down to the surviving portion so we don't try to flush bytes
		// the caller just said it doesn't care about.
		if blk.dirtyLo >= 0 {
			startInBlk := int(maxInt64(off-blkOff, 0))
			endInBlk := int(minInt64(end-blkOff, blockSize))
			// Clip [dirtyLo, dirtyHi) by removing [startInBlk, endInBlk).
			if startInBlk <= blk.dirtyLo && endInBlk >= blk.dirtyHi {
				blk.clearDirty()
			} else if startInBlk <= blk.dirtyLo && endInBlk > blk.dirtyLo {
				blk.dirtyLo = endInBlk
			} else if endInBlk >= blk.dirtyHi && startInBlk < blk.dirtyHi {
				blk.dirtyHi = startInBlk
			}
			if blk.dirtyHi <= blk.dirtyLo {
				blk.clearDirty()
			}
		}
	}
	b.ranges.remove(off, length)
	// Recompute fast-path state for every block this discard touched —
	// any FastDisk block whose disk bytes we're punching must drop back
	// to the slow path so readers don't pread the soon-to-be-hole.
	if b.states != nil {
		end := off + length
		for blkOff := alignDown(off); blkOff < end; blkOff += blockSize {
			b.markStateForBlockLocked(blkOff)
			// Discarded bytes are gone; the next reader has to re-earn
			// the promote signal, not inherit the prior counter.
			if b.readCounts != nil {
				idx := blkOff >> blockSizeLog2
				if idx >= 0 && int(idx) < len(b.readCounts) {
					b.readCounts[idx].Store(0)
				}
			}
		}
	}
	b.mu.Unlock()

	// Punch on disk outside the lock — file ops are thread-safe and the
	// caller doesn't want to block other RAM-only readers behind a syscall.
	if err := punchHole(b.file, off, length); err == nil {
		b.statsPunches.Add(1)
		b.statsReclaimed.Add(length)
	}
	// Drop the kernel's page-cache mirror of the discarded range too:
	// punchHole reclaims the disk bytes, this reclaims the kernel RAM.
	adviseDontNeed(b.file, off, length)
	return nil
}

// HasRange reports whether [off, off+length) is fully present (RAM or disk).
func (b *Buffer) HasRange(off, length int64) bool {
	if b.closed.Load() || length <= 0 {
		return false
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ranges.present(off, length)
}

// Present returns the buffered subranges within [off, off+length). Useful
// for "what's still missing?" decisions in cache callers.
func (b *Buffer) Present(off, length int64) []Range {
	if b.closed.Load() || length <= 0 {
		return nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ranges.presentRanges(off, length)
}

// WillRead hints to the kernel that the given byte range will be read
// soon, so it can prefetch on-disk pages into its page cache. Non-blocking,
// cheap, and a no-op on non-Linux platforms. Safe to call concurrently
// with reads/writes; takes no buffer locks.
//
// Callers like the prefetcher use this when they've queued a download
// that lands at this offset and expect a reader moments later.
func (b *Buffer) WillRead(off, length int64) {
	if b.closed.Load() || length <= 0 {
		return
	}
	adviseWillNeed(b.file, off, length)
}

// Sync forces all dirty in-memory blocks to disk and calls fsync.
// Returns the first error encountered.
func (b *Buffer) Sync() error {
	if b.closed.Load() {
		return ErrClosed
	}
	b.mu.Lock()
	// Collect dirty blocks without holding the lock during disk writes.
	var dirty []*block
	for _, blk := range b.blocks {
		if !blk.isClean() {
			dirty = append(dirty, blk)
		}
	}
	for _, blk := range dirty {
		if err := b.flushBlockLocked(blk); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	b.mu.Unlock()
	return b.file.Sync()
}

// Close flushes any pending dirty blocks, closes the disk file, and (if
// the file was a temp file) removes it. Subsequent calls return ErrClosed.
//
// Blocks held in the package-level blockPool are *not* returned here:
// dropBlockLocked already returned them on eviction; any still-resident
// blocks are simply dropped on the floor and the pool entries for them
// die with the GC when nothing references the slices. Trying to drain
// them into the pool on Close would be a long-tail RAM win that isn't
// worth the extra code on the teardown path.
func (b *Buffer) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}

	// Stop the promote worker before tearing down. Closed flag is set, so
	// hint enqueues from in-flight ReadAts become no-ops (or drop via the
	// select default).
	if b.promoteStop != nil {
		close(b.promoteStop)
		b.promoteWg.Wait()
	}

	// Drain any remaining dirty blocks so callers don't silently lose
	// writes they hadn't yet Synced.
	b.mu.Lock()
	for _, blk := range b.blocks {
		if !blk.isClean() {
			// Best effort: a failed flush here just loses data the
			// caller never Synced. Surface no error to keep semantics
			// simple — Close is also the cleanup path.
			_ = b.flushBlockLocked(blk)
		}
	}
	b.mu.Unlock()

	// Release the kernel's page-cache footprint for this file before
	// closing it. Callers that own this Buffer (DFS CacheItem on idle
	// eviction, Usenet SegmentCache teardown) routinely follow Close with
	// removing the underlying file; any pages still cached are dead
	// weight, and a single hint reclaims them all at once instead of
	// waiting for the kernel to do it lazily under memory pressure.
	// No-op on non-Linux.
	adviseDontNeedAll(b.file)

	closeErr := b.file.Close()
	if b.diskTemp {
		_ = os.Remove(b.file.Name())
	}
	return closeErr
}

// SetEvictionMinOff hints the buffer that blocks with offset < off are
// no longer interesting to active readers — eviction may freely target
// them. Blocks with offset >= off are part of the active sliding window
// and should be preserved when possible. Pass 0 to clear the hint
// (falls back to pure LRU). Cheap, atomic, no lock.
//
// In Usenet/DFS streaming this is driven from the sliding-window cursor
// (SegmentCache.MarkConsumed): we never want to fight the cursor by
// evicting blocks the reader is about to ask for.
func (b *Buffer) SetEvictionMinOff(off int64) {
	if off < 0 {
		off = 0
	}
	b.evictionMinOff.Store(off)
}

// hintPromote increments the per-block read counter and enqueues a
// promote hint on the first read past promoteThreshold. Cheap (one
// atomic.Add) when there's no shared interest; only crosses into the
// promote channel when there's real signal.
func (b *Buffer) hintPromote(blockOff int64) {
	if b.readCounts == nil || b.promoteCh == nil {
		return
	}
	idx := blockOff >> blockSizeLog2
	if idx < 0 || int(idx) >= len(b.readCounts) {
		return
	}
	if b.readCounts[idx].Add(1) != promoteThreshold {
		return
	}
	select {
	case b.promoteCh <- blockOff:
	default:
		// Worker is behind; the hint is best-effort, drop it.
	}
}

// promoteLoop drains promoteCh, installing each requested block into RAM.
// Disk I/O happens OUTSIDE the exclusive lock — the lock is held only for
// the install (map insert + LRU push + state flip), keeping the critical
// section in the microsecond range.
func (b *Buffer) promoteLoop() {
	defer b.promoteWg.Done()
	for {
		select {
		case <-b.promoteStop:
			return
		case blockOff, ok := <-b.promoteCh:
			if !ok {
				return
			}
			b.promoteOne(blockOff)
		}
	}
}

// promoteOne loads blockOff from disk into a fresh block and splices it
// into the RAM cache. Bails out early at every safety check so a wrong
// hint costs nearly nothing.
func (b *Buffer) promoteOne(blockOff int64) {
	if b.closed.Load() {
		return
	}
	// Cheap residency check under RLock — concurrent readers proceed.
	b.mu.RLock()
	_, resident := b.blocks[blockOff]
	b.mu.RUnlock()
	if resident {
		return
	}

	// Allocate + load OUTSIDE the exclusive lock. The kernel page cache
	// likely still has these bytes from the reader's recent pread, so the
	// syscall here is hot-path cheap.
	bufPtr := b.blockPool.Get().(*[]byte)
	buf := (*bufPtr)[:blockSize]
	if _, err := b.file.ReadAt(buf, blockOff); err != nil && !errors.Is(err, io.EOF) {
		b.blockPool.Put(bufPtr)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	// Re-check residency — someone may have installed while we were
	// loading. Also bail if a concurrent Discard removed the range.
	if _, ok := b.blocks[blockOff]; ok {
		b.blockPool.Put(bufPtr)
		return
	}
	if !b.ranges.anyPresent(blockOff, blockSize) {
		b.blockPool.Put(bufPtr)
		return
	}
	// Make room — only clean evictions, never flush dirty under the
	// promote path; that would re-introduce the under-lock-syscall cost
	// write-through was designed to eliminate.
	for b.bytesInRAM+blockSize > b.maxBytes {
		if !b.tryEvictCleanLocked() {
			b.blockPool.Put(bufPtr)
			return
		}
	}
	blk := &block{
		off:     blockOff,
		data:    buf,
		bufPtr:  bufPtr,
		dirtyLo: -1,
		dirtyHi: -1,
	}
	b.blocks[blockOff] = blk
	b.bytesInRAM += int64(blockSize)
	b.pushFrontLocked(blk)
	// Block is now RAM-resident: fast path must be off so readers see
	// the RAM data, not a stale pread.
	if slot := b.stateSlot(blockOff); slot != nil {
		slot.Store(stateSlow)
	}
	b.statsPromotions.Add(1)
}

// tryEvictCleanLocked drops the LRU-tail clean block, respecting the
// caller-supplied eviction-min-offset hint when set. Returns true if a
// block was evicted. Caller holds b.mu.
func (b *Buffer) tryEvictCleanLocked() bool {
	minOff := b.evictionMinOff.Load()
	for blk := b.lruTail; blk != nil; blk = blk.prev {
		if !blk.isClean() {
			continue
		}
		if minOff > 0 && blk.off >= minOff {
			continue // block is in the protected active window
		}
		b.dropBlockLocked(blk)
		b.statsEvictions.Add(1)
		return true
	}
	return false
}


// Stats returns the current observability counters.
func (b *Buffer) Stats() Stats {
	b.mu.RLock()
	blocksInRAM := len(b.blocks)
	bytesInRAM := b.bytesInRAM
	bytesPresent := b.ranges.totalSize()
	b.mu.RUnlock()
	return Stats{
		BlocksInRAM:    blocksInRAM,
		BytesInRAM:     bytesInRAM,
		BytesPresent:   bytesPresent,
		Hits:           b.statsHits.Load(),
		PartialHits:    b.statsPartialHits.Load(),
		Misses:         b.statsMisses.Load(),
		Flushes:        b.statsFlushes.Load(),
		Evictions:      b.statsEvictions.Load(),
		WritesThrough:  b.statsWriteThrough.Load(),
		Promotions:     b.statsPromotions.Load(),
		HolesPunched:   b.statsPunches.Load(),
		BytesReclaimed: b.statsReclaimed.Load(),
	}
}

// -----------------------------------------------------------------------
// Internal: cache lookup, LRU, eviction, flush
// -----------------------------------------------------------------------

// acquireBlockLocked returns the block at blockOff, creating it (and
// loading from disk if any bytes in the block are already present) if
// not already cached. Must be called with b.mu held.
//
// On allocation that would exceed maxBytes, evicts the LRU clean block.
// If only dirty blocks remain, the oldest is flushed synchronously to free
// space.
func (b *Buffer) acquireBlockLocked(blockOff int64, forWrite bool) (*block, error) {
	if blk, ok := b.blocks[blockOff]; ok {
		return blk, nil
	}

	// Make room if needed before allocating.
	for b.bytesInRAM+blockSize > b.maxBytes {
		if err := b.evictOneLocked(); err != nil {
			return nil, err
		}
	}

	bufPtr := b.blockPool.Get().(*[]byte)
	buf := (*bufPtr)[:blockSize]

	// Load from disk if any part of this block is known to be present.
	if b.ranges.anyPresent(blockOff, blockSize) {
		if _, err := b.file.ReadAt(buf, blockOff); err != nil && !errors.Is(err, io.EOF) {
			b.blockPool.Put(bufPtr)
			return nil, fmt.Errorf("buffer: load block %d: %w", blockOff, err)
		}
	}

	blk := &block{
		off:     blockOff,
		data:    buf,
		bufPtr:  bufPtr,
		dirtyLo: -1,
		dirtyHi: -1,
	}
	b.blocks[blockOff] = blk
	b.bytesInRAM += int64(blockSize)
	b.pushFrontLocked(blk)
	// A RAM block now exists for this offset — readers must take the
	// locked path to see the RAM data, not pread stale disk bytes.
	if slot := b.stateSlot(blockOff); slot != nil {
		slot.Store(stateSlow)
	}
	return blk, nil
}

// dropBlockLocked removes a block from the cache and returns its buffer to
// the pool. Caller must hold b.mu. Dirty bytes are discarded — callers
// that need to preserve dirty data must flush first.
func (b *Buffer) dropBlockLocked(blk *block) {
	delete(b.blocks, blk.off)
	b.unlinkLocked(blk)
	b.bytesInRAM -= int64(blockSize)
	b.blockPool.Put(blk.bufPtr)
	// No more RAM block at this offset. If the block is fully on disk,
	// future reads can take the fast pread path; otherwise keep them on
	// the locked path so they handle the partial coverage correctly.
	b.markStateForBlockLocked(blk.off)
	// Reset the promote counter so a single re-read after eviction
	// doesn't immediately re-trigger promotion — the signal has to be
	// re-earned from a fresh start.
	if b.readCounts != nil {
		idx := blk.off >> blockSizeLog2
		if idx >= 0 && int(idx) < len(b.readCounts) {
			b.readCounts[idx].Store(0)
		}
	}
}

// evictOneLocked drops the LRU clean block (flushing the oldest dirty
// block first if all candidates are dirty). Caller holds b.mu.
func (b *Buffer) evictOneLocked() error {
	for blk := b.lruTail; blk != nil; blk = blk.prev {
		if blk.isClean() {
			b.dropBlockLocked(blk)
			b.statsEvictions.Add(1)
			return nil
		}
	}
	// All blocks are dirty. Flush the oldest, then drop it.
	if b.lruTail == nil {
		// Nothing to evict — caller is asking for room we can't provide.
		// This shouldn't happen unless MemorySize < blockSize.
		return nil
	}
	blk := b.lruTail
	if err := b.flushBlockLocked(blk); err != nil {
		return err
	}
	b.dropBlockLocked(blk)
	b.statsEvictions.Add(1)
	return nil
}

// flushBlockLocked writes a block's dirty range to disk.
//
// Must be called with b.mu exclusively held (Lock, not RLock). Releasing
// the lock around pwrite is unsafe: a concurrent WriteAt could mutate
// blk.data while file.WriteAt is reading from it, producing a torn write
// — bytes flushed end up a mix of pre- and post-mutation, and the
// dirty-clear that follows would "forget" the new writes. The historical
// symptom was video corruption (frames depend on intact byte runs) while
// audio still played (independent packets tolerate loss).
//
// ReadAt does NOT contend with this — it holds RLock, which excludes
// Lock, so flushes simply wait for readers to drain rather than racing.
func (b *Buffer) flushBlockLocked(blk *block) error {
	if blk.isClean() {
		return nil
	}
	lo, hi := blk.dirtyLo, blk.dirtyHi
	if _, err := b.file.WriteAt(blk.data[lo:hi], blk.off+int64(lo)); err != nil {
		return fmt.Errorf("buffer: flush block %d [%d,%d): %w", blk.off, lo, hi, err)
	}
	blk.clearDirty()
	b.statsFlushes.Add(1)
	return nil
}

// -----------------------------------------------------------------------
// LRU list manipulation. Operate only on b.lruHead / b.lruTail; require
// b.mu held by the caller.
// -----------------------------------------------------------------------

func (b *Buffer) pushFrontLocked(blk *block) {
	blk.prev = nil
	blk.next = b.lruHead
	if b.lruHead != nil {
		b.lruHead.prev = blk
	}
	b.lruHead = blk
	if b.lruTail == nil {
		b.lruTail = blk
	}
}

func (b *Buffer) unlinkLocked(blk *block) {
	if blk.prev != nil {
		blk.prev.next = blk.next
	} else {
		b.lruHead = blk.next
	}
	if blk.next != nil {
		blk.next.prev = blk.prev
	} else {
		b.lruTail = blk.prev
	}
	blk.prev, blk.next = nil, nil
}

// touchLocked moves blk to the LRU head. Caller must hold the exclusive
// Lock (not RLock) because this mutates the list. ReadAt deliberately
// does NOT call this: it would force ReadAt to take exclusive Lock and
// re-introduce the multi-reader contention we removed. The LRU is
// therefore write-order, which for our streaming workload (write-once,
// read-many) matches the working set we want to keep in RAM.
func (b *Buffer) touchLocked(blk *block) {
	if b.lruHead == blk {
		return
	}
	b.unlinkLocked(blk)
	b.pushFrontLocked(blk)
}

// -----------------------------------------------------------------------
// Helpers.
// -----------------------------------------------------------------------

func alignDown(off int64) int64 { return off &^ (blockSize - 1) }

// stateSlot returns the atomic state slot for the block containing off,
// or nil if the buffer was constructed without a TotalSize (so no slot
// was allocated). Safe for concurrent use; readers may load lock-free.
func (b *Buffer) stateSlot(blockOff int64) *atomic.Uint32 {
	if b.states == nil {
		return nil
	}
	idx := blockOff >> blockSizeLog2
	if idx < 0 || int(idx) >= len(b.states) {
		return nil
	}
	return &b.states[idx]
}

// markStateForBlockLocked recomputes the fast-path state for blockOff
// after a write or block-cache change. Caller holds b.mu.
//
//   - If the block has a RAM resident: stateSlow. The truth lives in RAM,
//     not on disk, so reads must take the locked path to see it.
//   - Else if the block is fully covered by ranges: stateFastDisk.
//   - Else: stateSlow (partial coverage).
func (b *Buffer) markStateForBlockLocked(blockOff int64) {
	slot := b.stateSlot(blockOff)
	if slot == nil {
		return
	}
	if _, ok := b.blocks[blockOff]; ok {
		slot.Store(stateSlow)
		return
	}
	if b.ranges.present(blockOff, blockSize) {
		slot.Store(stateFastDisk)
	} else {
		slot.Store(stateSlow)
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
