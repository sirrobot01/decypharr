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
//   - WriteAt returns as soon as the data is in RAM; a block's dirty bytes are
//     persisted to disk lazily — when the block is evicted to make room, and on
//     Sync/Close. There is no background flush worker: the flush runs under the
//     buffer's exclusive lock (see flushBlockLocked for why it must), so
//     evicting a still-dirty block briefly serializes against readers. For the
//     write-once streaming workload this is rare in practice — blocks are
//     usually clean (re-read, never rewritten) by the time they reach the LRU
//     tail.
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
	HolesPunched   int64
	BytesReclaimed int64
}

// Config configures a new Buffer.
type Config struct {
	// MemorySize is the maximum bytes held in RAM across all blocks. When
	// allocating a new block would exceed this, the LRU evictor flushes
	// and reclaims the oldest clean block. Default 32 MB when zero; values
	// below one block (1 MB) are clamped up to it, since the cache is
	// block-granular and a smaller ceiling could never admit a block.
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

	// OnEvict, if non-nil, is invoked after the owning Pool punches a hole
	// behind the read head to reclaim disk (DiskLimit pressure). It reports
	// the byte range [off, off+length) that was released so the caller can
	// keep its own persisted metadata in sync — DFS removes the range from
	// info.Rs, usenet marks the covered segments Empty. It is NOT called for
	// caller-initiated Discard (the caller already knows). Called off the
	// read/write path with no buffer lock held; must not call back into the
	// buffer in a way that blocks.
	OnEvict func(off, length int64)
}

// Buffer is the public type. Methods are safe for concurrent use.
type Buffer struct {
	cfg Config

	// pool owns this Buffer's RAM/disk budgets and eviction policy. Every
	// Buffer belongs to exactly one Pool (the default pool for buffer.New).
	pool *Pool

	// onEvict mirrors Config.OnEvict; called after a pool-driven punch.
	onEvict func(off, length int64)

	file     *os.File
	diskTemp bool // remove the file on Close

	// mu guards blocks, the LRU list, bytesInRAM, and ranges. Reads
	// (ReadAt, HasRange, Present, Stats) take RLock so multiple readers
	// can hit RAM-cached blocks or pread from disk in parallel. Writers
	// (WriteAt cache path, the write-through publish step, Discard,
	// eviction) take Lock; write-through keeps only the pwrite itself
	// outside the lock. See writeRegion.
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

	// range carries stateFastDisk, ReadAt bypasses mu, the range tree,
	// and the block map and just pread's — matching baseline's hot path
	// exactly. Transitions are written under mu but read lock-free, so
	// the fast path adds nothing more than one atomic.Load per block.
	states []atomic.Uint32

	// readHead is the caller's current read position (see SetReadHead). It is
	// the frontier the owning Pool's disk backstop punches behind (it reclaims
	// [0, readHead-BackWindow)). RAM eviction does not consult it — the block
	// cache is pure write-order LRU, which for the streaming workload already
	// keeps the active window resident. Zero means "no hint": no disk punching.
	readHead atomic.Int64

	// diskBytes is this Buffer's running total of on-disk present bytes,
	// maintained alongside the range tracker (insert +=, remove/punch -=) and
	// mirrored into pool.diskInUse. A close proxy for the file's footprint
	// (the RAM cache is a small layer on top); used to enforce DiskLimit.
	diskBytes atomic.Int64

	// alloc owns block memory for this Buffer. It is per-Buffer rather than
	// package-global: each Buffer is one file (minutes-to-hours lifetime)
	// with its own working set, and a shared pool would either hold blocks
	// across Buffers (displacing the kernel page cache disk reads rely on)
	// or need periodic draining for no real cross-file reuse. Unlike the
	// sync.Pool it replaces, alloc unmaps blocks that fall outside a small
	// reuse window immediately, so RAM tracks the live working set instead
	// of lingering until a GC + scavenger pass returns it.
	alloc blockAllocator

	closed atomic.Bool

	// dropBehindPos is the high-water offset up to which the disk file's page
	// cache has been dropped by DropBehind. Monotonic; guards against
	// re-issuing fadvise over already-dropped ranges.
	dropBehindPos atomic.Int64

	// Stats counters. Atomic to allow Stats() without holding mu.
	statsHits         atomic.Int64
	statsPartialHits  atomic.Int64
	statsMisses       atomic.Int64
	statsFlushes      atomic.Int64
	statsEvictions    atomic.Int64
	statsWriteThrough atomic.Int64
	statsPunches      atomic.Int64
	statsReclaimed    atomic.Int64
}

// newBuffer creates a Buffer bound to pool p. Callers go through
// Pool.NewBuffer (or the package-level New, which uses the default pool). The
// disk-backing file is created and (if TotalSize is set) pre-truncated.
func newBuffer(p *Pool, cfg Config) (*Buffer, error) {
	if cfg.MemorySize <= 0 {
		cfg.MemorySize = defaultMemorySize
	}
	if cfg.MemorySize < blockSize {
		// Block-granular cache: a ceiling below one block would make the
		// first allocation unsatisfiable.
		cfg.MemorySize = blockSize
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
		pool:     p,
		onEvict:  cfg.OnEvict,
		file:     file,
		diskTemp: diskTemp,
		blocks:   make(map[int64]*block),
		maxBytes: cfg.MemorySize,
		ranges:   newRangeSet(),
	}
	if cfg.TotalSize > 0 {
		n := int((cfg.TotalSize + blockSize - 1) / blockSize)
		b.states = make([]atomic.Uint32, n)
	}
	// Size the reuse free list to the working set, capped — a Buffer never
	// needs to retain more idle blocks than it can hold resident.
	b.alloc.maxFree = int(b.maxBytes / blockSize)
	if b.alloc.maxFree > maxReuseBlocks {
		b.alloc.maxFree = maxReuseBlocks
	}
	if b.alloc.maxFree < 1 {
		b.alloc.maxFree = 1
	}
	for _, r := range cfg.InitialRanges {
		if r.Size > 0 {
			// Seeded ranges are already on disk from a prior run: count them
			// toward this Buffer's (and the pool's) disk footprint.
			b.rangesInsert(r.Off, r.Size)
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
		hi := int(min(end, blockEnd) - blockOff)
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
	// Cache a new block only if there's room under both the per-stream ceiling
	// and the pool budget; otherwise take the write-through path (to disk, no
	// RAM growth).
	canCache := b.bytesInRAM+blockSize <= b.maxBytes && !b.pool.wouldExceedMemory()
	b.mu.RUnlock()

	// Resident block, or room to cache one → cached path. Needs the
	// exclusive lock; re-check residency under it since the RLock sample
	// may be stale (acquireBlockLocked returns the existing block if so).
	if resident || canCache {
		b.mu.Lock()
		// Re-check under the lock: a WriteAt past the entry closed-check that
		// loses the race with Close must not allocate blocks or publish
		// ranges — the pool RAM/disk accounting Close just settled would
		// drift permanently (this Buffer is already unregistered, so nothing
		// would ever subtract the additions back out).
		if b.closed.Load() {
			b.mu.Unlock()
			return ErrClosed
		}
		blk, ok := b.blocks[blockOff]
		if !ok {
			var err error
			if blk, err = b.acquireBlockLocked(blockOff); err != nil {
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
	// Same closed re-check as the cached path: never publish into the range
	// tracker after Close has settled the disk accounting.
	if b.closed.Load() {
		b.mu.Unlock()
		return ErrClosed
	}
	// Rare edge: the cache gained room (e.g. a Discard freed a block) and
	// another writer cached this block while our pwrite was in flight.
	// Mirror our bytes into RAM so the resident block stays authoritative.
	var err error
	if blk, ok := b.blocks[blockOff]; ok {
		err = b.writeIntoBlockLocked(blk, lo, hi, src)
	} else {
		b.rangesInsert(diskOff, int64(hi-lo))
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
	b.rangesInsert(blk.off+int64(lo), int64(hi-lo))
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
	//
	// One concurrent Discarder is NOT the caller: the pool's disk backstop
	// (punchBehindWindow) punches everything below readHead-BackWindow on its
	// own goroutine. Because this path preads after the lock-free state load
	// with no re-validation, a punch landing between the two would read back a
	// hole as zeros. The invariant that prevents it is the readHead contract:
	// callers MUST publish a read head that covers off (SetReadHead(off) or
	// lower) BEFORE issuing the read, so the backstop's ceiling sits at or
	// below off-BackWindow and never reaches the range in flight. DFS's
	// ReadAtContext does this before both the download and the read.
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
			if n < len(p) {
				// The state slots said this range is fully on disk; a short
				// pread of a present range means an invariant broke (file
				// shorter than the tracked presence). Surface it rather than
				// silently handing back stale caller memory in p's tail.
				return n, io.ErrUnexpectedEOF
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
		readHi := int(min(end, blockEnd) - blockOff)
		dstLo := int(cur - off)
		dstHi := dstLo + (readHi - readLo)

		if blk, ok := b.blocks[blockOff]; ok {
			copy(p[dstLo:dstHi], blk.data[readLo:readHi])
			ramServed = true
		} else {
			// Disk read. pread is thread-safe; a concurrent write-through
			// pwrite to a *different* range is fine, and the caller
			// contract forbids a concurrent write/discard of *this* range.
			n, err := b.file.ReadAt(p[dstLo:dstHi], cur)
			if err != nil && !errors.Is(err, io.EOF) {
				b.mu.RUnlock()
				return dstLo, fmt.Errorf("buffer: disk read at %d: %w", cur, err)
			}
			if n < dstHi-dstLo {
				// Present per the range tracker but the file came up short:
				// an invariant broke. Don't claim bytes we never read.
				b.mu.RUnlock()
				return dstLo + n, io.ErrUnexpectedEOF
			}
			diskServed = true
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
	b.discard(off, length)
	return nil
}

// discard is the core of Discard and of the pool's punch-behind backstop:
// drops/trims any cached blocks over [off, off+length), removes the range
// (updating disk accounting), and punches the hole on disk. Returns the number
// of present bytes reclaimed. It does NOT fire onEvict — that is the backstop's
// job, since caller-initiated Discard already knows what it released.
func (b *Buffer) discard(off, length int64) int64 {
	end := off + length

	b.mu.Lock()
	// Re-check under the lock: the pool's punch backstop passes Discard's
	// entry check on its own goroutine and can lose the race with Close.
	// Close settles this Buffer's disk footprint against the pool exactly
	// once (diskBytes.Swap); a discard slipping in afterwards would subtract
	// the same bytes a second time and leave pool.diskInUse under-counted
	// forever — silently disabling the DiskLimit backstop.
	if b.closed.Load() {
		b.mu.Unlock()
		return 0
	}
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
			startInBlk := int(max(off-blkOff, 0))
			endInBlk := int(min(end-blkOff, blockSize))
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
	removed := b.rangesRemove(off, length)
	// Recompute fast-path state for every block this discard touched —
	// any FastDisk block whose disk bytes we're punching must drop back
	// to the slow path so readers don't pread the soon-to-be-hole.
	if b.states != nil {
		for blkOff := alignDown(off); blkOff < end; blkOff += blockSize {
			b.markStateForBlockLocked(blkOff)
		}
	}
	b.mu.Unlock()

	// Punch on disk outside the lock — file ops are thread-safe and the
	// caller doesn't want to block other RAM-only readers behind a syscall.
	if err := punchHole(b.file, off, length); err == nil {
		b.statsPunches.Add(1)
		b.statsReclaimed.Add(removed)
	}
	// Drop the kernel's page-cache mirror of the discarded range too:
	// punchHole reclaims the disk bytes, this reclaims the kernel RAM.
	adviseDontNeed(b.file, off, length)
	return removed
}

// punchBehindWindow reclaims disk by punching every present range below
// readHead-backWindow. Invoked by the owning Pool when it is over its
// DiskLimit. Fires onEvict for each reclaimed range so the owner can update
// its persisted metadata. Returns total bytes reclaimed. A no-op when there is
// no read head yet or nothing behind the window.
func (b *Buffer) punchBehindWindow(backWindow int64) int64 {
	if b.closed.Load() {
		return 0
	}
	head := b.readHead.Load()
	if head <= 0 {
		return 0
	}
	ceiling := head - backWindow
	if ceiling <= 0 {
		return 0
	}
	b.mu.RLock()
	present := b.ranges.presentRanges(0, ceiling)
	b.mu.RUnlock()
	if len(present) == 0 {
		return 0
	}
	var reclaimed int64
	for _, r := range present {
		n := b.discard(r.Off, r.Size)
		if n > 0 {
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

// rangesInsert records presence of [off, off+length) in the range tracker and
// adds the newly-covered bytes to this Buffer's and the pool's disk footprint.
// Caller holds b.mu (or is in single-threaded construction).
func (b *Buffer) rangesInsert(off, length int64) {
	added := b.ranges.insert(off, length)
	if added > 0 {
		b.diskBytes.Add(added)
		b.pool.addDisk(added)
	}
}

// rangesRemove drops [off, off+length) from the range tracker and subtracts the
// reclaimed bytes from the disk footprint. Returns bytes removed. Caller holds
// b.mu.
func (b *Buffer) rangesRemove(off, length int64) int64 {
	removed := b.ranges.remove(off, length)
	if removed > 0 {
		b.diskBytes.Add(-removed)
		b.pool.subDisk(removed)
	}
	return removed
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

// DropBehind releases the disk file's page cache for the region that is more
// than `margin` bytes behind `offset` (the current read position), keeping the
// trailing `margin` resident so kernel readahead and short seek-backs are
// untouched. Unlike Discard it does NOT punch a hole — the bytes stay on disk,
// so a seek-back past the margin re-reads from disk rather than re-downloading.
//
// It is monotonic (only ever advances), lock-free, cheap, and a no-op on
// non-Linux platforms. Intended to be called from the read path of a long
// sequential stream so page cache tracks a sliding window instead of the whole
// played-through range. margin <= 0 disables it.
func (b *Buffer) DropBehind(offset, margin int64) {
	if b.closed.Load() || margin <= 0 || offset <= margin {
		return
	}
	target := offset - margin
	// Advance the high-water mark atomically; only one caller wins a given
	// advance, so we never re-fadvise an already-dropped range.
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

// Sync forces all dirty in-memory blocks to disk and calls fsync.
// Returns the first error encountered.
func (b *Buffer) Sync() error {
	if b.closed.Load() {
		return ErrClosed
	}
	b.mu.Lock()
	// Flush every dirty block under the exclusive lock — flushBlockLocked
	// requires it (releasing mu around the pwrite risks a torn write; see
	// its doc comment).
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
// Every still-resident block is unmapped here, and the allocator's reuse
// list is drained. With mmap-backed blocks the GC would not reclaim them on
// its own, so Close must hand them back explicitly — which also means a
// Buffer's whole RAM footprint returns to the OS the instant its owner
// (DFS CacheItem idle eviction, Usenet SegmentCache teardown) closes it.
func (b *Buffer) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return ErrClosed
	}

	// Drain any remaining dirty blocks so callers don't silently lose
	// writes they hadn't yet Synced, then unmap every resident block.
	// Holding the lock here excludes readers (RLock), so no goroutine can
	// be touching a block's memory as we unmap it.
	b.mu.Lock()
	for off, blk := range b.blocks {
		if !blk.isClean() {
			// Best effort: a failed flush here just loses data the
			// caller never Synced. Surface no error to keep semantics
			// simple — Close is also the cleanup path.
			_ = b.flushBlockLocked(blk)
		}
		munmapBlock(blk.bufPtr)
		delete(b.blocks, off)
	}
	b.pool.dropBytes(b.bytesInRAM) // release this Buffer's share of the pool RAM budget
	b.bytesInRAM = 0
	// Release this Buffer's share of the pool disk footprint and unregister it
	// so the disk backstop stops considering it.
	if db := b.diskBytes.Swap(0); db > 0 {
		b.pool.subDisk(db)
	}
	// Empty the range tracker so a racing discard/publish that somehow
	// reaches it (belt and suspenders on top of the closed re-checks) finds
	// nothing to remove and cannot perturb the pool accounting again.
	b.ranges = newRangeSet()
	b.mu.Unlock()
	b.pool.remove(b)

	// Return the reuse free list to the OS as well.
	b.alloc.drain()

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

// SetReadHead publishes the caller's current read position. It is the frontier
// the owning Pool's disk backstop punches behind: under DiskLimit pressure the
// pool reclaims [0, off-BackWindow). RAM eviction is unaffected — the block
// cache is pure write-order LRU. Pass 0 to clear (no disk punching). Cheap,
// atomic, no lock.
//
// Driven from the streaming cursor: usenet's SegmentCache.MarkConsumed and
// DFS's ReadAtContext both call this as playback advances, so eviction never
// fights the data the reader is about to ask for.
//
// Contract for backstop safety (DiskLimit > 0): publish a read head that
// covers the offset you are about to read BEFORE issuing that read. The disk
// backstop punches everything below readHead-BackWindow, and the lock-free
// fast read path does not re-validate after its state load — so a read whose
// offset sits below the current readHead-BackWindow can race a punch and read
// a hole back as zeros. Calling SetReadHead(off) first pulls the protected
// frontier over the in-flight read and closes that window (notably on a
// seek-back, where off is below the prior forward read head).
func (b *Buffer) SetReadHead(off int64) {
	if off < 0 {
		off = 0
	}
	// Plain store, not monotonic: usenet feeds an already-monotonic consumed
	// cursor, while DFS feeds its actual read position so a real seek-back
	// pulls the frontier back and re-protects the region now being read,
	// instead of letting the disk backstop punch right behind a seek.
	b.readHead.Store(off)
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
func (b *Buffer) acquireBlockLocked(blockOff int64) (*block, error) {
	if blk, ok := b.blocks[blockOff]; ok {
		return blk, nil
	}

	// Make room if needed before allocating, for both the per-stream ceiling
	// and the pool budget. A failure to evict is only fatal when we're over
	// the per-stream ceiling; if it's purely pool pressure and this Buffer
	// has nothing left to evict, allocate anyway (bounded overshoot — one block
	// per actively-allocating Buffer). Breaking on no-progress (not just on
	// error) is load-bearing: evictOneLocked with an empty LRU evicts nothing
	// and returns no error, and retrying it while the pool stays over budget
	// would spin forever under the exclusive lock.
	for b.bytesInRAM+blockSize > b.maxBytes || b.pool.wouldExceedMemory() {
		evicted, err := b.evictOneLocked()
		if evicted {
			continue
		}
		if b.bytesInRAM+blockSize > b.maxBytes {
			if err == nil {
				err = fmt.Errorf("buffer: no evictable blocks under memory ceiling")
			}
			return nil, err
		}
		break
	}

	bufPtr := b.alloc.get()
	buf := (*bufPtr)[:blockSize]

	// Load from disk if any part of this block is known to be present.
	if b.ranges.anyPresent(blockOff, blockSize) {
		if _, err := b.file.ReadAt(buf, blockOff); err != nil && !errors.Is(err, io.EOF) {
			b.alloc.put(bufPtr)
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
	b.pool.addBlock()
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
	b.pool.dropBytes(int64(blockSize))
	b.alloc.put(blk.bufPtr)
	// No more RAM block at this offset. If the block is fully on disk,
	// future reads can take the fast pread path; otherwise keep them on
	// the locked path so they handle the partial coverage correctly.
	b.markStateForBlockLocked(blk.off)
}

// evictOneLocked drops the LRU clean block (flushing the oldest dirty
// block first if all candidates are dirty). Returns whether a block was
// actually evicted — (false, nil) means there was nothing to evict, and the
// caller must not retry, or it would spin forever. Caller holds b.mu.
func (b *Buffer) evictOneLocked() (bool, error) {
	for blk := b.lruTail; blk != nil; blk = blk.prev {
		if blk.isClean() {
			b.dropBlockLocked(blk)
			b.statsEvictions.Add(1)
			return true, nil
		}
	}
	// All blocks are dirty. Flush the oldest, then drop it.
	if b.lruTail == nil {
		return false, nil
	}
	blk := b.lruTail
	if err := b.flushBlockLocked(blk); err != nil {
		return false, err
	}
	b.dropBlockLocked(blk)
	b.statsEvictions.Add(1)
	return true, nil
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
	blk.lastAccess = nowNano()
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

