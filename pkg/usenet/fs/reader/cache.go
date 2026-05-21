package reader

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// SegmentCache provides disk storage for segment data with a pin/unpin mechanism
// to prevent eviction during reads.
//
// Key design:
//   - Segments are pinned while being read, preventing eviction
//   - When unpinned with refcount=0, segments become evictable
//   - Disk pressure evicts oldest unpinned segments (LRU via access timestamps)
//   - Evicted segments are re-downloaded on next access
//   - Single sparse file for disk storage (better locality than per-segment files)
type SegmentCache struct {
	// Segment metadata
	segments   []SegmentMeta
	segCount   int
	segOffsets []int64 // Cumulative byte offsets for segment lookup
	totalSize  int64
	segLengths []atomic.Int64 // Actual bytes stored per segment

	// Per-segment state and data
	states    []atomic.Uint32         // SegmentState per segment
	pinCounts []atomic.Int32          // Reference counts for pinning
	errors    []atomic.Pointer[error] // Error for failed segments

	// Disk storage
	diskPath string
	diskFile *os.File
	onDisk   []atomic.Bool // Whether segment is on disk

	// Eviction tracking: per-segment last-access timestamp (unix nano).
	// Lock-free replacement for container/list LRU — no mutex, no heap allocs.
	accessTime []atomic.Int64

	// Size tracking and limits
	maxDisk int64
	curDisk atomic.Int64

	// Async eviction — keeps eviction off the hot write path.
	evictSignal chan struct{}
	evictWg     sync.WaitGroup

	// Sharded conditions for waiting
	shardMu   [numShards]sync.Mutex
	shardCond [numShards]*sync.Cond

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
	logger zerolog.Logger

	// Stats
	stats *ReaderStats
}

const (
	numShards = 64
	shardMask = numShards - 1
)

// NewSegmentCache creates a new segment cache.
func NewSegmentCache(
	ctx context.Context,
	segments []SegmentMeta,
	config Config,
	stats *ReaderStats,
	logger zerolog.Logger,
) (*SegmentCache, error) {
	ctx, cancel := context.WithCancel(ctx)
	segCount := len(segments)

	// Compute cumulative offsets for O(log n) segment lookup
	offsets := computeOffsets(segments)
	totalSize := int64(0)
	if len(offsets) > 0 {
		totalSize = offsets[len(offsets)-1]
	}

	// Create disk storage directory
	diskPath := config.DiskPath
	if diskPath == "" {
		var err error
		diskPath, err = os.MkdirTemp("", "usenet-cache-*")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
	} else {
		if err := os.MkdirAll(diskPath, 0755); err != nil {
			cancel()
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
		// Create unique subdirectory
		var err error
		diskPath, err = os.MkdirTemp(diskPath, "cache-*")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create temp subdir: %w", err)
		}
	}

	// Create sparse disk file
	diskFilePath := filepath.Join(diskPath, "segments.bin")
	diskFile, err := os.OpenFile(diskFilePath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		cancel()
		_ = os.RemoveAll(diskPath)
		return nil, fmt.Errorf("create disk file: %w", err)
	}

	// Pre-extend file to total size for sparse writes
	if totalSize > 0 {
		if err := diskFile.Truncate(totalSize); err != nil {
			_ = diskFile.Close()
			_ = os.RemoveAll(diskPath)
			cancel()
			return nil, fmt.Errorf("truncate disk file: %w", err)
		}
	}

	sc := &SegmentCache{
		segments:    segments,
		segCount:    segCount,
		segOffsets:  offsets,
		totalSize:   totalSize,
		segLengths:  make([]atomic.Int64, segCount),
		states:      make([]atomic.Uint32, segCount),
		pinCounts:   make([]atomic.Int32, segCount),
		errors:      make([]atomic.Pointer[error], segCount),
		diskPath:    diskPath,
		diskFile:    diskFile,
		onDisk:      make([]atomic.Bool, segCount),
		accessTime:  make([]atomic.Int64, segCount),
		maxDisk:     config.MaxDisk,
		evictSignal: make(chan struct{}, 1),
		ctx:         ctx,
		cancel:      cancel,
		logger:      logger.With().Str("component", "cache").Logger(),
		stats:       stats,
	}

	// Initialize shard conditions
	for i := 0; i < numShards; i++ {
		sc.shardCond[i] = sync.NewCond(&sc.shardMu[i])
	}

	// Start background evictor
	sc.evictWg.Add(1)
	go sc.evictLoop()

	return sc, nil
}

// computeOffsets calculates cumulative byte offsets for segment lookup.
func computeOffsets(segments []SegmentMeta) []int64 {
	offsets := make([]int64, len(segments)+1)

	// Check if segments have explicit offsets
	if len(segments) > 0 && segments[0].EndOffset > 0 {
		for i, seg := range segments {
			offsets[i] = seg.StartOffset
		}
		if len(segments) > 0 {
			offsets[len(segments)] = segments[len(segments)-1].EndOffset + 1
		}
	} else {
		// Compute from segment sizes
		cumulative := int64(0)
		for i, seg := range segments {
			offsets[i] = cumulative
			size := seg.Bytes
			if size <= 0 {
				size = 750 * 1024 // Typical usenet segment size
			}
			cumulative += size
		}
		offsets[len(segments)] = cumulative
	}

	return offsets
}

// Get returns segment data, loading from disk if necessary.
// Returns nil, false if segment is not cached.
// The segment should be pinned before calling Get to prevent eviction.
func (sc *SegmentCache) Get(segIdx int) ([]byte, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil, false
	}

	state := SegmentState(sc.states[segIdx].Load())

	if state == StateOnDisk {
		data, err := sc.loadFromDisk(segIdx)
		if err != nil {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("failed to load from disk")
			sc.stats.CacheMisses.Add(1)
			return nil, false
		}
		sc.stats.CacheHits.Add(1)
		return data, true
	}

	sc.stats.CacheMisses.Add(1)
	return nil, false
}

// ReadInto reads segment data into the provided buffer, avoiding allocation.
// Returns the number of bytes read and whether the segment was available.
// buf must be at least SegmentDataSize(segIdx) bytes.
func (sc *SegmentCache) ReadInto(segIdx int, buf []byte) (int, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return 0, false
	}

	state := SegmentState(sc.states[segIdx].Load())

	if state == StateOnDisk {
		n, err := sc.loadFromDiskInto(segIdx, buf)
		if err != nil {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("failed to load from disk")
			sc.stats.CacheMisses.Add(1)
			return 0, false
		}
		sc.stats.CacheHits.Add(1)
		return n, true
	}

	sc.stats.CacheMisses.Add(1)
	return 0, false
}

// ReadRangeInto reads only [segOffset, segOffset+length) of a segment directly
// into buf, skipping the rest of the segment entirely.
//
// This is the zero-amplification read path: callers that only need a slice of
// a segment should use this instead of ReadInto (which reads the full segment).
// buf must be at least length bytes.
func (sc *SegmentCache) ReadRangeInto(segIdx int, segOffset, length int64, buf []byte) (int, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return 0, false
	}

	state := SegmentState(sc.states[segIdx].Load())

	if state == StateOnDisk {
		n, err := sc.loadRangeFromDiskInto(segIdx, segOffset, length, buf)
		if err != nil {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("failed to read segment range from disk")
			sc.stats.CacheMisses.Add(1)
			return 0, false
		}
		sc.stats.CacheHits.Add(1)
		return n, true
	}

	sc.stats.CacheMisses.Add(1)
	return 0, false
}

// SegmentDataSize returns the stored or expected size of a segment's data.
func (sc *SegmentCache) SegmentDataSize(segIdx int) int64 {
	if segIdx < 0 || segIdx >= sc.segCount {
		return 0
	}
	size := sc.segLengths[segIdx].Load()
	if size <= 0 {
		size = sc.segments[segIdx].Bytes
		if size <= 0 {
			size = sc.segOffsets[segIdx+1] - sc.segOffsets[segIdx]
		}
	}
	return size
}

// Put writes segment data directly to disk (for streaming writes).
func (sc *SegmentCache) Put(segIdx int, data []byte) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return fmt.Errorf("segment index out of range: %d", segIdx)
	}
	if sc.closed.Load() {
		return io.ErrClosedPipe
	}

	// Determine offset in the sparse file
	offset := sc.segOffsets[segIdx]

	// Backpressure: if already over budget, reclaim space synchronously before
	// adding more so a fast download burst can't overshoot maxDisk into an
	// ENOSPC before the async evictor catches up.
	if sc.curDisk.Load() > sc.maxDisk {
		sc.drainOverBudget()
	}

	// Write to disk — pwrite at non-overlapping offsets is safe to call concurrently.
	_, err := sc.diskFile.WriteAt(data, offset)

	if err != nil {
		return fmt.Errorf("write segment %d to disk: %w", segIdx, err)
	}

	sc.onDisk[segIdx].Store(true)
	sc.curDisk.Add(int64(len(data)))
	sc.segLengths[segIdx].Store(int64(len(data)))
	sc.states[segIdx].Store(uint32(StateOnDisk))
	sc.touchSegment(segIdx)

	// Wake any waiters
	sc.wakeWaiters(segIdx)

	sc.signalEvict()
	return nil
}

// segmentWriter is the contract doFetch uses to stream a segment body into the
// disk tier. Exactly one of Finalize/Discard is called per writer: Finalize on
// a successful download (commit), Discard on failure/abort.
type segmentWriter interface {
	Write(p []byte) (int, error)
	Finalize()
	Discard()
}

// StreamWriter returns a disk-backed segmentWriter for the segment.
func (sc *SegmentCache) StreamWriter(segIdx int) segmentWriter {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil
	}

	seg := sc.segments[segIdx]
	offset := sc.segOffsets[segIdx]

	// Backpressure: drain before streaming a new segment in so a download
	// burst can't overshoot maxDisk into ENOSPC while the async evictor lags.
	if sc.curDisk.Load() > sc.maxDisk {
		sc.drainOverBudget()
	}

	return &diskStreamWriter{
		file:      sc.diskFile,
		offset:    offset,
		dataStart: seg.SegmentDataStart,
		maxBytes:  seg.Bytes,
		cache:     sc,
		segIdx:    segIdx,
	}
}

// diskStreamWriter writes streamed segment data directly to disk.
type diskStreamWriter struct {
	file      *os.File
	offset    int64 // Segment start offset in file
	dataStart int64 // Bytes to skip at start of stream (yEnc header)
	maxBytes  int64 // Maximum bytes to write
	skipped   int64 // Bytes skipped so far
	written   int64 // Bytes written to disk
	cache     *SegmentCache
	segIdx    int
}

func (w *diskStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	consumed := 0

	// Skip dataStart bytes (yEnc header, etc.)
	if w.skipped < w.dataStart {
		skip := min(w.dataStart-w.skipped, int64(len(p)))
		w.skipped += skip
		consumed += int(skip)
		p = p[skip:]
		if len(p) == 0 {
			return consumed, nil
		}
	}

	// Check if we've written enough
	if w.written >= w.maxBytes {
		return consumed + len(p), nil
	}

	// Limit to remaining bytes allowed
	remaining := w.maxBytes - w.written
	writeLen := int64(len(p))
	if writeLen > remaining {
		writeLen = remaining
	}

	// Write to disk at the correct offset.
	// pwrite at non-overlapping segment offsets is safe to call concurrently.
	writeOffset := w.offset + w.written
	n, err := w.file.WriteAt(p[:writeLen], writeOffset)

	if err != nil {
		return consumed + n, err
	}

	w.written += int64(n)
	return consumed + len(p), nil
}

// Discard is a no-op for the disk writer: a failed/partial sparse write is
// simply overwritten on the next attempt, so there is nothing to release.
func (w *diskStreamWriter) Discard() {}

// Finalize marks the segment as written and updates cache state.
func (w *diskStreamWriter) Finalize() {
	if w.cache != nil && w.segIdx >= 0 && w.written > 0 {
		w.cache.onDisk[w.segIdx].Store(true)
		w.cache.curDisk.Add(w.written)
		w.cache.segLengths[w.segIdx].Store(w.written)
		w.cache.states[w.segIdx].Store(uint32(StateOnDisk))
		w.cache.touchSegment(w.segIdx)
		w.cache.wakeWaiters(w.segIdx)
		w.cache.signalEvict()
	}
}

// loadFromDisk loads segment data from the sparse file.
func (sc *SegmentCache) loadFromDisk(segIdx int) ([]byte, error) {
	if !sc.onDisk[segIdx].Load() {
		return nil, fmt.Errorf("segment %d not on disk", segIdx)
	}

	seg := sc.segments[segIdx]
	offset := sc.segOffsets[segIdx]
	size := sc.segLengths[segIdx].Load()
	if size <= 0 {
		size = seg.Bytes
		if size <= 0 {
			// Calculate from offsets
			size = sc.segOffsets[segIdx+1] - offset
		}
	}

	data := make([]byte, size)
	// pread at non-overlapping offsets is safe without a mutex.
	n, err := sc.diskFile.ReadAt(data, offset)

	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("read segment %d from disk: %w", segIdx, err)
	}

	return data[:n], nil
}

// loadFromDiskInto reads segment data from disk into the provided buffer.
func (sc *SegmentCache) loadFromDiskInto(segIdx int, buf []byte) (int, error) {
	if !sc.onDisk[segIdx].Load() {
		return 0, fmt.Errorf("segment %d not on disk", segIdx)
	}

	offset := sc.segOffsets[segIdx]
	size := sc.SegmentDataSize(segIdx)

	if int64(len(buf)) < size {
		return 0, fmt.Errorf("buffer too small for segment %d: need %d, have %d", segIdx, size, len(buf))
	}

	// pread at non-overlapping offsets is safe without a mutex.
	n, err := sc.diskFile.ReadAt(buf[:size], offset)

	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("read segment %d from disk: %w", segIdx, err)
	}

	return n, nil
}

func (sc *SegmentCache) loadRangeFromDiskInto(segIdx int, segOffset, length int64, buf []byte) (int, error) {
	if !sc.onDisk[segIdx].Load() {
		return 0, fmt.Errorf("segment %d not on disk", segIdx)
	}
	if segOffset < 0 || length < 0 {
		return 0, fmt.Errorf("invalid segment range: offset=%d length=%d", segOffset, length)
	}
	if int64(len(buf)) < length {
		return 0, fmt.Errorf("buffer too small for segment %d range: need %d, have %d", segIdx, length, len(buf))
	}

	size := sc.SegmentDataSize(segIdx)
	if segOffset > size {
		return 0, fmt.Errorf("segment offset %d beyond size %d for segment %d", segOffset, size, segIdx)
	}
	if segOffset+length > size {
		length = size - segOffset
	}

	absoluteOffset := sc.segOffsets[segIdx] + segOffset
	// pread at non-overlapping offsets is safe without a mutex.
	n, err := sc.diskFile.ReadAt(buf[:length], absoluteOffset)

	if err != nil && err != io.EOF {
		return 0, fmt.Errorf("read segment %d range from disk: %w", segIdx, err)
	}

	return n, nil
}

// PinRange marks segments as in-use, preventing eviction.
// CRITICAL: Must be called before reading segments to prevent the race condition.
func (sc *SegmentCache) PinRange(start, end int) {
	for i := start; i <= end && i < sc.segCount; i++ {
		sc.pinCounts[i].Add(1)
	}
}

// UnpinRange decrements pin count, allowing eviction when count reaches 0.
// CRITICAL: Must be called after reading segments to allow cleanup.
func (sc *SegmentCache) UnpinRange(start, end int) {
	for i := start; i <= end && i < sc.segCount; i++ {
		sc.pinCounts[i].Add(-1)
	}
}

// IsPinned returns true if the segment has a positive pin count.
func (sc *SegmentCache) IsPinned(segIdx int) bool {
	if segIdx < 0 || segIdx >= sc.segCount {
		return false
	}
	return sc.pinCounts[segIdx].Load() > 0
}

// GetState returns the current state of a segment.
func (sc *SegmentCache) GetState(segIdx int) SegmentState {
	if segIdx < 0 || segIdx >= sc.segCount {
		return StateEmpty
	}
	return SegmentState(sc.states[segIdx].Load())
}

// SetState sets the state of a segment.
func (sc *SegmentCache) SetState(segIdx int, state SegmentState) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	sc.states[segIdx].Store(uint32(state))
}

// MarkFetching atomically transitions Empty → Fetching.
// Returns true if the transition succeeded (caller should fetch).
func (sc *SegmentCache) MarkFetching(segIdx int) bool {
	if segIdx < 0 || segIdx >= sc.segCount {
		return false
	}
	return sc.states[segIdx].CompareAndSwap(uint32(StateEmpty), uint32(StateFetching))
}

// MarkFailed marks a segment as failed with the given error.
func (sc *SegmentCache) MarkFailed(segIdx int, err error) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	sc.errors[segIdx].Store(&err)
	sc.states[segIdx].Store(uint32(StateFailed))
	sc.wakeWaiters(segIdx)
}

// GetError returns the error for a failed segment.
func (sc *SegmentCache) GetError(segIdx int) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil
	}
	if errPtr := sc.errors[segIdx].Load(); errPtr != nil {
		return *errPtr
	}
	return nil
}

// ResetState resets a segment to Empty for retry.
func (sc *SegmentCache) ResetState(segIdx int) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	sc.states[segIdx].Store(uint32(StateEmpty))
	sc.errors[segIdx].Store(nil)
}

// WaitForSegment blocks until the segment is available or an error occurs.
func (sc *SegmentCache) WaitForSegment(ctx context.Context, segIdx int) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return fmt.Errorf("segment index out of range: %d", segIdx)
	}

	// Fast path: check state without locking
	state := SegmentState(sc.states[segIdx].Load())
	switch state {
	case StateOnDisk:
		return nil
	case StateFailed:
		if err := sc.GetError(segIdx); err != nil {
			return err
		}
		return fmt.Errorf("segment %d failed", segIdx)
	}

	// Slow path: wait on shard condition
	shardIdx := segIdx & shardMask
	cond := sc.shardCond[shardIdx]
	mu := &sc.shardMu[shardIdx]

	// Context cancellation watchers
	wakeShard := func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	}
	var stopWatchers []func()
	if ctx != nil {
		stopper := context.AfterFunc(ctx, wakeShard)
		stopWatchers = append(stopWatchers, func() { stopper() })
	}
	cacheStopper := context.AfterFunc(sc.ctx, wakeShard)
	stopWatchers = append(stopWatchers, func() { cacheStopper() })
	defer func() {
		for _, stop := range stopWatchers {
			if stop != nil {
				stop()
			}
		}
	}()

	mu.Lock()
	defer mu.Unlock()

	for {
		state = SegmentState(sc.states[segIdx].Load())
		switch state {
		case StateOnDisk:
			return nil
		case StateFailed:
			if err := sc.GetError(segIdx); err != nil {
				return err
			}
			return fmt.Errorf("segment %d failed", segIdx)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sc.ctx.Done():
			return sc.ctx.Err()
		default:
		}

		cond.Wait()
	}
}

// wakeWaiters wakes all goroutines waiting for the given segment.
func (sc *SegmentCache) wakeWaiters(segIdx int) {
	shardIdx := segIdx & shardMask
	sc.shardMu[shardIdx].Lock()
	sc.shardCond[shardIdx].Broadcast()
	sc.shardMu[shardIdx].Unlock()
}

// touchSegment records the current time as the last access for a segment.
// Lock-free replacement for touchLRU — no mutex, no heap allocation.
func (sc *SegmentCache) touchSegment(segIdx int) {
	sc.accessTime[segIdx].Store(time.Now().UnixNano())
}

// signalEvict notifies the background evictor that disk usage may have exceeded
// the budget. Non-blocking: if a signal is already pending this is a no-op.
func (sc *SegmentCache) signalEvict() {
	select {
	case sc.evictSignal <- struct{}{}:
	default:
	}
}

// evictLoop is the background goroutine that drains disk usage when over budget.
// Running eviction asynchronously keeps it off the hot write path.
func (sc *SegmentCache) evictLoop() {
	defer sc.evictWg.Done()
	for {
		select {
		case <-sc.ctx.Done():
			return
		case <-sc.evictSignal:
		}
		sc.drainOverBudget()
	}
}

// drainOverBudget evicts oldest unpinned on-disk segments until disk usage is
// back under maxDisk, or nothing more is evictable (everything still in budget
// is pinned by in-flight reads). Safe for concurrent callers: evictFromDisk
// uses CAS to claim each segment exactly once, and findEvictable returning -1
// guarantees the loop terminates rather than spinning when all is pinned.
func (sc *SegmentCache) drainOverBudget() {
	for sc.curDisk.Load() > sc.maxDisk {
		idx := sc.findEvictable()
		if idx < 0 {
			break
		}
		sc.evictFromDisk(idx)
	}
}

// findEvictable returns the index of the oldest unpinned on-disk segment,
// or -1 if none is evictable. Lock-free O(n) scan over access timestamps.
// For typical Usenet files (50–500 segments) this is faster than walking a
// linked list under a global mutex.
func (sc *SegmentCache) findEvictable() int {
	oldest := int64(math.MaxInt64)
	result := -1
	for i := 0; i < sc.segCount; i++ {
		if sc.pinCounts[i].Load() > 0 {
			continue
		}
		if SegmentState(sc.states[i].Load()) != StateOnDisk {
			continue
		}
		if t := sc.accessTime[i].Load(); t < oldest {
			oldest = t
			result = i
		}
	}
	return result
}

// evictFromDisk removes a segment from the logical disk cache.
//
// Uses CAS to atomically claim the segment (OnDisk → Empty) and rechecks
// pin count to close the TOCTOU window between findEvictable and eviction.
func (sc *SegmentCache) evictFromDisk(segIdx int) bool {
	// Recheck pin — may have been acquired since findEvictable returned.
	if sc.pinCounts[segIdx].Load() > 0 {
		return false
	}
	// Atomically transition OnDisk → Empty. If another goroutine beat us
	// (concurrent evictor, or segment was re-fetched), skip cleanly.
	if !sc.states[segIdx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEmpty)) {
		return false
	}
	size := sc.segLengths[segIdx].Load()
	if size <= 0 {
		size = sc.segments[segIdx].Bytes
		if size <= 0 {
			size = sc.segOffsets[segIdx+1] - sc.segOffsets[segIdx]
		}
	}
	sc.onDisk[segIdx].Store(false)
	sc.curDisk.Add(-size)
	sc.stats.Evictions.Add(1)

	// Physically release the region so the space is actually returned to the
	// filesystem. Without this the bytes linger in segments.bin until Close;
	// on tmpfs/ramdisk those are RAM pages, so a file streamed through a small
	// maxDisk would still grow memory toward the whole file size. Best-effort:
	// the per-segment offset is fixed and a re-fetch just rewrites here, so a
	// failed punch only forgoes the reclaim — it never breaks correctness.
	if err := punchHole(sc.diskFile, sc.segOffsets[segIdx], size); err != nil {
		sc.logger.Debug().Err(err).Int("segment", segIdx).Msg("hole punch on evict failed")
	}
	return true
}

// SegmentsForRange returns the segment indices that cover the byte range.
func (sc *SegmentCache) SegmentsForRange(offset, length int64) (int, int) {
	if sc.segCount == 0 {
		return 0, 0
	}

	endOffset := offset + length - 1

	// Binary search for first segment containing offset
	startIdx := sc.binarySearchSegment(offset)
	if startIdx >= sc.segCount {
		startIdx = sc.segCount - 1
	}

	// Binary search for last segment containing endOffset
	endIdx := sc.binarySearchSegment(endOffset)
	if endIdx >= sc.segCount {
		endIdx = sc.segCount - 1
	}

	return startIdx, endIdx
}

// binarySearchSegment finds the segment containing the given offset.
func (sc *SegmentCache) binarySearchSegment(offset int64) int {
	lo, hi := 0, sc.segCount
	for lo < hi {
		mid := (lo + hi) / 2
		if sc.segOffsets[mid+1] <= offset {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// GetSegment returns segment metadata by index.
func (sc *SegmentCache) GetSegment(segIdx int) *SegmentMeta {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil
	}
	return &sc.segments[segIdx]
}

// SegmentCount returns the total number of segments.
func (sc *SegmentCache) SegmentCount() int {
	return sc.segCount
}

// TotalSize returns the total size of all segments.
func (sc *SegmentCache) TotalSize() int64 {
	return sc.totalSize
}

// SegmentOffset returns the byte offset of a segment.
func (sc *SegmentCache) SegmentOffset(segIdx int) int64 {
	if segIdx < 0 || segIdx > sc.segCount {
		return 0
	}
	return sc.segOffsets[segIdx]
}

// Close releases all resources.
func (sc *SegmentCache) Close() error {
	if sc.closed.Swap(true) {
		return nil
	}

	sc.cancel()

	// Wake all waiters
	for i := 0; i < numShards; i++ {
		sc.shardMu[i].Lock()
		sc.shardCond[i].Broadcast()
		sc.shardMu[i].Unlock()
	}

	// Wait for background evictor to exit
	sc.evictWg.Wait()

	// Close disk file and remove directory
	if sc.diskFile != nil {
		_ = sc.diskFile.Close()
	}
	if sc.diskPath != "" {
		_ = os.RemoveAll(sc.diskPath)
	}

	return nil
}
