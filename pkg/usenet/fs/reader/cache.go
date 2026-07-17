package reader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"

	"github.com/sirrobot01/decypharr/internal/buffer"
)

// SegmentCache is a usenet-segment-aware view over a buffer.Buffer.
//
// The storage layer is the buffer: it owns the sparse disk file, the
// in-RAM block cache, the page-cache discipline, the hole punching, and
// the bookkeeping for what bytes are present anywhere. SegmentCache adds
// the usenet-specific policy on top:
//
//   - State machine per segment (Empty / Fetching / OnDisk / Failed)
//   - Pin counts so in-flight reads can't race against eviction
//   - Per-segment access timestamps driving the sliding-window evictor
//   - "Last byte delivered to a client" high-water mark for the sliding
//     window's distance test
//   - Hard-disk budget backstop on top of the proactive sweeper
//
// All the actual byte movement (write, read, hole-punch) goes through the
// buffer. That's the entire integration boundary.
type SegmentCache struct {
	// Segment metadata
	segments   []SegmentMeta
	segCount   int
	segOffsets []int64 // cumulative byte offsets for binary-search lookup
	totalSize  int64
	segLengths []atomic.Int64 // bytes actually stored per segment

	// Per-segment state
	states     []atomic.Uint32
	pinCounts  []atomic.Int32
	errors     []atomic.Pointer[error]
	accessTime []atomic.Int64

	// Storage layer.
	buf      *buffer.Buffer
	diskPath string // remembered for RemoveAll on Close

	// Hard-disk budget. The sliding-window sweeper does the routine eviction
	// work; drainOverBudget is the backstop if pinned-segment count or burst
	// inflow pushes curDisk past maxDisk anyway.
	maxDisk      int64
	curDisk      atomic.Int64
	evictSignal  chan struct{}
	evictMu      sync.Mutex          // serializes hard-budget scans and hole punching
	evictScratch []evictionCandidate // reused by findEvictableBatch under evictMu
	evictWg      sync.WaitGroup

	// Sliding-window state. See sweepWindow for the policy.
	maxConsumedOff atomic.Int64
	sweepWg        sync.WaitGroup

	// Sharded waiters: readers blocking on WaitForSegment park on one of
	// numShards condition variables to avoid global wakeup storms.
	shardMu   [numShards]sync.Mutex
	shardCond [numShards]*sync.Cond

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	closed atomic.Bool
	logger zerolog.Logger

	stats *ReaderStats
}

const (
	numShards = 64
	shardMask = numShards - 1

	// Sliding-window eviction tunables. Hardcoded — the cache is internal
	// temporary storage; exposing knobs invites mis-tuning.
	//
	// backWindowBytes keeps a generous slice of recently-played history
	// pinned so brief scrub-back gestures don't trigger a re-fetch. ~170
	// segments at 750 KB each ≈ 25 s of 1080p / 12 s of 4K — covers
	// typical "10 second rewind" buttons in media players with margin.
	backWindowBytes = 128 << 20

	// segmentMinRetentionAge is the minimum time a segment must be
	// untouched before it is eligible for window-based eviction. Defends
	// against the pause-and-resume case: even if a segment is technically
	// "behind" the last delivered offset, we keep it for a moment because
	// the player may still be drawing from the same area.
	segmentMinRetentionAge = 30 * time.Second

	// segmentSweepInterval is how often the proactive sliding-window
	// evictor wakes.
	segmentSweepInterval = 5 * time.Second

	// segmentSweepBatch caps how many segments a single sweep evicts so
	// a large jump in playback position doesn't punch holes for thousands
	// of segments in one burst. Sweeps are cheap; the next tick picks up
	// the rest.
	segmentSweepBatch = 128

	// bufferMemorySize is the per-stream RAM ceiling for the underlying buffer:
	// forward prefetch + recent reads. 32 MB covers ~40 segments hot in RAM —
	// enough headroom that a bursty download or a seek-back within the window
	// doesn't stall playback or force a re-download. Aggregate RAM across many
	// concurrent streams is bounded separately by the global buffer budget
	// (buffer.SetGlobalMemoryBudget), so this can stay generous without the
	// per-stream-size x concurrency blowup that a small ceiling was guarding.
	bufferMemorySize = 32 << 20
)

// NewSegmentCache creates a new segment cache backed by a freshly-created
// buffer.Buffer on a sparse disk file under config.DiskPath (or a temp dir).
func NewSegmentCache(
	ctx context.Context,
	segments []SegmentMeta,
	config Config,
	stats *ReaderStats,
	logger zerolog.Logger,
) (*SegmentCache, error) {
	ctx, cancel := context.WithCancel(ctx)
	segCount := len(segments)

	offsets := computeOffsets(segments)
	totalSize := int64(0)
	if len(offsets) > 0 {
		totalSize = offsets[len(offsets)-1]
	}

	// Resolve a fresh per-cache disk directory. We own it for the cache's
	// lifetime and remove it on Close. The buffer's disk file lives inside.
	diskPath := config.DiskPath
	if diskPath == "" {
		var err error
		diskPath, err = os.MkdirTemp("", "usenet-cache-*")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create temp dir: %w", err)
		}
	} else {
		if err := os.MkdirAll(diskPath, 0o755); err != nil {
			cancel()
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
		var err error
		diskPath, err = os.MkdirTemp(diskPath, "cache-*")
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create temp subdir: %w", err)
		}
	}

	// sc is referenced by the buffer's OnEvict closure; assigned just below
	// before any read/write can trigger a pool-driven punch.
	var sc *SegmentCache

	buf, err := usenetBufferPool().NewBuffer(buffer.Config{
		MemorySize: bufferMemorySize,
		DiskPath:   filepath.Join(diskPath, "segments.bin"),
		TotalSize:  totalSize,
		// Only fires if the usenet pool is given a disk limit (off by default —
		// usenet bounds disk via its own sliding-window sweep). If a pool-driven
		// punch ever does happen, mark the covered segments Empty so they
		// re-fetch instead of pointing at a hole.
		OnEvict: func(off, length int64) {
			if sc != nil {
				sc.onBufferEvict(off, length)
			}
		},
	})
	if err != nil {
		cancel()
		_ = os.RemoveAll(diskPath)
		return nil, fmt.Errorf("create buffer: %w", err)
	}

	sc = &SegmentCache{
		segments:    segments,
		segCount:    segCount,
		segOffsets:  offsets,
		totalSize:   totalSize,
		segLengths:  make([]atomic.Int64, segCount),
		states:      make([]atomic.Uint32, segCount),
		pinCounts:   make([]atomic.Int32, segCount),
		errors:      make([]atomic.Pointer[error], segCount),
		accessTime:  make([]atomic.Int64, segCount),
		buf:         buf,
		diskPath:    diskPath,
		maxDisk:     config.MaxDisk,
		evictSignal: make(chan struct{}, 1),
		ctx:         ctx,
		cancel:      cancel,
		logger:      logger.With().Str("component", "cache").Logger(),
		stats:       stats,
	}

	for i := range numShards {
		sc.shardCond[i] = sync.NewCond(&sc.shardMu[i])
	}

	// Hard-budget backstop. The sliding-window sweeper does the routine
	// eviction work — see sweepLoop.
	sc.evictWg.Add(1)
	go sc.evictLoop()

	// Proactive sliding-window evictor: this is what keeps the cache tight
	// to actual playback instead of growing to the file size.
	sc.sweepWg.Add(1)
	go sc.sweepLoop()

	return sc, nil
}

// computeOffsets calculates cumulative byte offsets for segment lookup.
func computeOffsets(segments []SegmentMeta) []int64 {
	offsets := make([]int64, len(segments)+1)
	if len(segments) > 0 && segments[0].EndOffset > 0 {
		for i, seg := range segments {
			offsets[i] = seg.StartOffset
		}
		if len(segments) > 0 {
			offsets[len(segments)] = segments[len(segments)-1].EndOffset + 1
		}
	} else {
		cumulative := int64(0)
		for i, seg := range segments {
			offsets[i] = cumulative
			size := seg.Bytes
			if size <= 0 {
				size = 750 * 1024
			}
			cumulative += size
		}
		offsets[len(segments)] = cumulative
	}
	return offsets
}

// Get returns segment data, loading via the buffer.
// Returns nil, false if the segment isn't cached. Pin before calling.
func (sc *SegmentCache) Get(segIdx int) ([]byte, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil, false
	}
	if SegmentState(sc.states[segIdx].Load()) != StateOnDisk {
		sc.stats.CacheMisses.Add(1)
		return nil, false
	}

	off := sc.segOffsets[segIdx]
	size := sc.SegmentDataSize(segIdx)
	data := make([]byte, size)
	if _, err := sc.buf.ReadAt(data, off); err != nil {
		if !errors.Is(err, buffer.ErrNotPresent) {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("buffer read failed")
		}
		sc.stats.CacheMisses.Add(1)
		return nil, false
	}
	sc.stats.CacheHits.Add(1)
	return data, true
}

// ReadInto reads the full segment into buf. buf must be at least
// SegmentDataSize(segIdx) bytes.
func (sc *SegmentCache) ReadInto(segIdx int, dst []byte) (int, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return 0, false
	}
	if SegmentState(sc.states[segIdx].Load()) != StateOnDisk {
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}

	off := sc.segOffsets[segIdx]
	size := sc.SegmentDataSize(segIdx)
	if int64(len(dst)) < size {
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}
	n, err := sc.buf.ReadAt(dst[:size], off)
	if err != nil {
		if !errors.Is(err, buffer.ErrNotPresent) {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("buffer read failed")
		}
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}
	sc.stats.CacheHits.Add(1)
	return n, true
}

// ReadRangeInto is the zero-amplification read path: copies only the
// requested [segOffset, segOffset+length) slice of the segment.
func (sc *SegmentCache) ReadRangeInto(segIdx int, segOffset, length int64, dst []byte) (int, bool) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return 0, false
	}
	if SegmentState(sc.states[segIdx].Load()) != StateOnDisk {
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}
	if segOffset < 0 || length < 0 || int64(len(dst)) < length {
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}

	size := sc.SegmentDataSize(segIdx)
	if segOffset > size {
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}
	if segOffset+length > size {
		length = size - segOffset
	}
	if length <= 0 {
		sc.stats.CacheHits.Add(1)
		return 0, true
	}

	absoluteOffset := sc.segOffsets[segIdx] + segOffset
	n, err := sc.buf.ReadAt(dst[:length], absoluteOffset)
	if err != nil {
		if !errors.Is(err, buffer.ErrNotPresent) {
			sc.logger.Warn().Err(err).Int("segment", segIdx).Msg("buffer read failed")
		}
		sc.stats.CacheMisses.Add(1)
		return 0, false
	}
	sc.stats.CacheHits.Add(1)
	return n, true
}

// SegmentDataSize returns the stored or expected size of a segment.
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

// Put writes segment data through the buffer.
func (sc *SegmentCache) Put(segIdx int, data []byte) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return fmt.Errorf("segment index out of range: %d", segIdx)
	}
	if sc.closed.Load() {
		return io.ErrClosedPipe
	}

	if sc.maxDisk > 0 && sc.curDisk.Load() > sc.maxDisk {
		sc.drainOverBudget()
	}

	off := sc.segOffsets[segIdx]
	if _, err := sc.buf.WriteAt(data, off); err != nil {
		return fmt.Errorf("write segment %d: %w", segIdx, err)
	}

	sc.curDisk.Add(int64(len(data)))
	sc.segLengths[segIdx].Store(int64(len(data)))
	sc.states[segIdx].Store(uint32(StateOnDisk))
	sc.touchSegment(segIdx)
	sc.wakeWaiters(segIdx)
	sc.signalEvict()
	return nil
}

// segmentWriter is the contract doFetch uses to stream a segment body into
// the cache. Exactly one of Finalize/Discard is called per writer.
type segmentWriter interface {
	Write(p []byte) (int, error)
	Finalize()
	Discard()
}

// StreamWriter returns a buffer-backed writer for the segment. The writer
// skips the yEnc dataStart header and caps writes at the segment's max
// expected size.
func (sc *SegmentCache) StreamWriter(segIdx int) segmentWriter {
	if segIdx < 0 || segIdx >= sc.segCount {
		return nil
	}

	if sc.maxDisk > 0 && sc.curDisk.Load() > sc.maxDisk {
		sc.drainOverBudget()
	}

	seg := sc.segments[segIdx]
	return &bufferStreamWriter{
		buf:       sc.buf,
		offset:    sc.segOffsets[segIdx],
		dataStart: seg.SegmentDataStart,
		maxBytes:  seg.Bytes,
		cache:     sc,
		segIdx:    segIdx,
	}
}

// bufferStreamWriter pipes decoded body bytes from NNTP into the buffer at
// the segment's reserved offset. Writes that exceed maxBytes are silently
// dropped (the decoder may include some trailing padding).
type bufferStreamWriter struct {
	buf       *buffer.Buffer
	offset    int64
	dataStart int64
	maxBytes  int64
	skipped   int64
	written   int64
	cache     *SegmentCache
	segIdx    int
}

func (w *bufferStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	consumed := 0

	if w.skipped < w.dataStart {
		skip := min(w.dataStart-w.skipped, int64(len(p)))
		w.skipped += skip
		consumed += int(skip)
		p = p[skip:]
		if len(p) == 0 {
			return consumed, nil
		}
	}

	if w.written >= w.maxBytes {
		return consumed + len(p), nil
	}

	remaining := w.maxBytes - w.written
	writeLen := min(int64(len(p)), remaining)

	n, err := w.buf.WriteAt(p[:writeLen], w.offset+w.written)
	if err != nil {
		return consumed + n, err
	}
	w.written += int64(n)
	return consumed + len(p), nil
}

// Discard is a no-op for the buffer writer: the buffer slot is fixed-offset
// and gets overwritten in place on the next attempt, so there's nothing
// to release on a failed/partial write.
func (w *bufferStreamWriter) Discard() {}

// Finalize commits the segment to the cache: state to OnDisk, length
// recorded, waiters woken.
func (w *bufferStreamWriter) Finalize() {
	if w.cache == nil || w.segIdx < 0 || w.written <= 0 {
		return
	}
	w.cache.curDisk.Add(w.written)
	w.cache.segLengths[w.segIdx].Store(w.written)
	w.cache.states[w.segIdx].Store(uint32(StateOnDisk))
	w.cache.touchSegment(w.segIdx)
	w.cache.wakeWaiters(w.segIdx)
	w.cache.signalEvict()
}

// PinRange marks segments as in-use, preventing eviction.
func (sc *SegmentCache) PinRange(start, end int) {
	for i := start; i <= end && i < sc.segCount; i++ {
		sc.pinCounts[i].Add(1)
	}
}

// UnpinRange decrements the pin count for the range.
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

// MarkFetching atomically transitions Empty → Fetching. Returns true if
// the transition succeeded (caller owns the fetch).
func (sc *SegmentCache) MarkFetching(segIdx int) bool {
	if segIdx < 0 || segIdx >= sc.segCount {
		return false
	}
	return sc.states[segIdx].CompareAndSwap(uint32(StateEmpty), uint32(StateFetching))
}

// MarkFailed records a permanent fetch failure.
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

// ResetFailed transitions Failed → Empty so a retry can re-fetch the segment.
// It is a CAS, not a blind store: a concurrent reader may have successfully
// fetched the segment between attempts, and flipping OnDisk → Empty would both
// force a spurious re-download and leak the segment's bytes out of the curDisk
// accounting (inflating it for the life of the reader, making the budget
// backstop over-evict). It must also never clobber another fetcher's Fetching.
func (sc *SegmentCache) ResetFailed(segIdx int) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	if sc.states[segIdx].CompareAndSwap(uint32(StateFailed), uint32(StateEmpty)) {
		sc.errors[segIdx].Store(nil)
	}
}

// ReleaseFetching transitions Fetching → Empty. Only the fetcher that owns the
// Fetching state (won MarkFetching) may call it, on its cancellation paths.
func (sc *SegmentCache) ReleaseFetching(segIdx int) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	sc.states[segIdx].CompareAndSwap(uint32(StateFetching), uint32(StateEmpty))
}

// WaitForSegment blocks until the segment is OnDisk, fails, or the context
// is canceled.
func (sc *SegmentCache) WaitForSegment(ctx context.Context, segIdx int) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return fmt.Errorf("segment index out of range: %d", segIdx)
	}
	if ctx == nil {
		ctx = context.Background()
	}

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

	shardIdx := segIdx & shardMask
	cond := sc.shardCond[shardIdx]
	mu := &sc.shardMu[shardIdx]

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

// WaitForEvictionRelease blocks while the segment is in StateEvicting, returning
// once the evictor has finished punching its range and dropped it to Empty (or
// the context/cache is canceled). Callers in the fetch path use this so a
// re-fetch never starts writing into a range mid-Discard.
func (sc *SegmentCache) WaitForEvictionRelease(ctx context.Context, segIdx int) error {
	if segIdx < 0 || segIdx >= sc.segCount {
		return fmt.Errorf("segment index out of range: %d", segIdx)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if SegmentState(sc.states[segIdx].Load()) != StateEvicting {
		return nil
	}

	shardIdx := segIdx & shardMask
	cond := sc.shardCond[shardIdx]
	mu := &sc.shardMu[shardIdx]

	wakeShard := func() {
		mu.Lock()
		cond.Broadcast()
		mu.Unlock()
	}
	ctxStopper := context.AfterFunc(ctx, wakeShard)
	cacheStopper := context.AfterFunc(sc.ctx, wakeShard)
	defer ctxStopper()
	defer cacheStopper()

	mu.Lock()
	defer mu.Unlock()

	for SegmentState(sc.states[segIdx].Load()) == StateEvicting {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sc.ctx.Done():
			return sc.ctx.Err()
		default:
		}
		cond.Wait()
	}
	return nil
}

// invalidateForRefetch forces a segment that is marked OnDisk but whose backing
// bytes are unreadable back to Empty so the next Fetch actually re-downloads it
// instead of trusting the stale OnDisk state and short-circuiting. The CAS
// guarantees the disk accounting is rolled back exactly once even if two readers
// hit the same wedged segment concurrently. Safe to call on a pinned segment —
// the subsequent re-fetch overwrites the slot in place.
func (sc *SegmentCache) invalidateForRefetch(segIdx int) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	if sc.states[segIdx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEmpty)) {
		if size := sc.segLengths[segIdx].Load(); size > 0 {
			sc.curDisk.Add(-size)
		}
	}
	sc.errors[segIdx].Store(nil)
}

// wakeWaiters wakes any WaitForSegment callers parked on this segment's shard.
func (sc *SegmentCache) wakeWaiters(segIdx int) {
	shardIdx := segIdx & shardMask
	sc.shardMu[shardIdx].Lock()
	sc.shardCond[shardIdx].Broadcast()
	sc.shardMu[shardIdx].Unlock()
}

// touchSegment records the current time as the last access for a segment.
func (sc *SegmentCache) touchSegment(segIdx int) {
	sc.accessTime[segIdx].Store(time.Now().UnixNano())
}

// signalEvict pokes the background evictor (non-blocking).
func (sc *SegmentCache) signalEvict() {
	select {
	case sc.evictSignal <- struct{}{}:
	default:
	}
}

// evictLoop runs the budget-backstop evictor. The proactive sliding-window
// sweeper does the routine work; this only runs if curDisk exceeds maxDisk
// despite the sweeper (e.g. burst of pinned segments).
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

// drainOverBudget is the hard-disk backstop.
func (sc *SegmentCache) drainOverBudget() {
	if sc.maxDisk <= 0 {
		return
	}

	// StreamWriter, Put, and the background evictor can all notice the same
	// overshoot concurrently. Let one caller do the scan and punching while
	// the others wait; once they acquire the lock the budget is normally
	// already satisfied. Without this guard, N concurrent segment completions
	// can each scan the full segment table and race to evict the same batch.
	sc.evictMu.Lock()
	defer sc.evictMu.Unlock()

	for sc.curDisk.Load() > sc.maxDisk {
		batch := sc.findEvictableBatch(segmentSweepBatch)
		if len(batch) == 0 {
			break
		}
		sc.evictBatch(batch)
	}
}

type evictionCandidate struct {
	idx int
	t   int64
}

// findEvictableBatch returns up to maxN unpinned OnDisk segments, sorted
// oldest-first by access time. Used by drainOverBudget only, with evictMu
// held. The scratch slice is retained so repeated budget checks do not create
// a large allocation-and-GC cycle; its size follows the number of actually
// cached segments, not the total NZB segment count.
func (sc *SegmentCache) findEvictableBatch(maxN int) []int {
	if maxN <= 0 {
		return nil
	}

	cands := sc.evictScratch[:0]
	if cands == nil {
		cands = make([]evictionCandidate, 0, min(maxN*2, sc.segCount))
	}
	for i := 0; i < sc.segCount; i++ {
		if sc.pinCounts[i].Load() > 0 {
			continue
		}
		if SegmentState(sc.states[i].Load()) != StateOnDisk {
			continue
		}
		cands = append(cands, evictionCandidate{i, sc.accessTime[i].Load()})
	}
	if len(cands) == 0 {
		sc.evictScratch = cands
		return nil
	}
	sort.Slice(cands, func(a, b int) bool {
		if cands[a].t != cands[b].t {
			return cands[a].t < cands[b].t
		}
		return cands[a].idx < cands[b].idx
	})
	out := make([]int, min(len(cands), maxN))
	for i, c := range cands[:len(out)] {
		out[i] = c.idx
	}
	sc.evictScratch = cands[:0]
	return out
}

// MarkConsumed records that bytes in [off, off+length) have been delivered
// to a client. Monotonic high-water mark used by the sliding-window
// evictor; backward seeks don't lower it because the back-window already
// absorbs them.
func (sc *SegmentCache) MarkConsumed(off, length int64) {
	if length <= 0 {
		return
	}
	end := off + length
	for {
		cur := sc.maxConsumedOff.Load()
		if end <= cur {
			return
		}
		if sc.maxConsumedOff.CompareAndSwap(cur, end) {
			// Plumb the cursor into the buffer's eviction policy: blocks
			// behind the consumed offset are safe to evict (we're done
			// with them), blocks ahead are the active window the reader
			// will still hit. Cheap atomic store, no buffer lock.
			//
			// Skip the back-window margin (we keep some history pinned at
			// the SegmentCache level for scrub-back); the buffer can be
			// stricter — anything we've explicitly consumed past is fair
			// game for promotion to evict.
			if sc.buf != nil {
				sc.buf.SetReadHead(end)
			}
			return
		}
	}
}

// sweepLoop runs the proactive sliding-window evictor.
func (sc *SegmentCache) sweepLoop() {
	defer sc.sweepWg.Done()
	ticker := time.NewTicker(segmentSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-sc.ctx.Done():
			return
		case <-ticker.C:
			sc.sweepWindow()
		}
	}
}

// sweepWindow picks segments that are both:
//
//  1. Behind the back-window (segEnd < maxConsumedOff - backWindowBytes), and
//  2. Untouched for at least segmentMinRetentionAge.
//
// Both conditions must hold — see the package comment in cache.go for the
// rationale behind each.
func (sc *SegmentCache) sweepWindow() {
	consumedHi := sc.maxConsumedOff.Load()
	if consumedHi <= 0 {
		return
	}
	cutoffOff := consumedHi - backWindowBytes
	if cutoffOff <= 0 {
		return
	}
	cutoffAccessNs := time.Now().Add(-segmentMinRetentionAge).UnixNano()

	indices := make([]int, 0, segmentSweepBatch)
	for i := 0; i < sc.segCount && len(indices) < segmentSweepBatch; i++ {
		if SegmentState(sc.states[i].Load()) != StateOnDisk {
			continue
		}
		if sc.pinCounts[i].Load() > 0 {
			continue
		}
		segEnd := sc.segOffsets[i+1]
		if segEnd > cutoffOff {
			continue
		}
		if sc.accessTime[i].Load() > cutoffAccessNs {
			continue
		}
		indices = append(indices, i)
	}
	if len(indices) == 0 {
		return
	}
	sc.evictBatch(indices)
}

// evictBatch transitions the given segments out of the cache and releases
// their byte ranges through the buffer. Adjacent ranges are coalesced into
// fewer Discard calls — for sequential playback eviction, ~dozen segments
// merge into one buffer.Discard (and thus one fallocate(PUNCH_HOLE)).
//
// Each segment moves OnDisk -> Evicting -> (Discard) -> Empty. The Evicting
// hold is what makes eviction safe against a concurrent re-fetch: MarkFetching
// only transitions Empty -> Fetching, so no fetcher can begin writing into a
// segment's range while we are punching it. Only after the Discard completes do
// we drop the slot to Empty and wake any reader/fetcher that parked on it; that
// re-fetch then writes into a freshly-punched, no-longer-contended range.
//
// Previously the slot went straight to Empty before the (deferred, coalesced)
// Discard, so a reader could re-download the segment in the gap and have its
// bytes punched right back out — leaving the slot OnDisk but unreadable and the
// "segment N still missing after re-fetch" wedge.
func (sc *SegmentCache) evictBatch(indices []int) {
	type rng struct {
		off  int64
		size int64
	}
	pieces := make([]rng, 0, len(indices))
	evicted := make([]int, 0, len(indices))

	for _, idx := range indices {
		if sc.pinCounts[idx].Load() > 0 {
			continue
		}
		// Reserve the segment for eviction. The CAS from OnDisk fences out both
		// a concurrent re-fetch (MarkFetching needs Empty) and another evictor.
		if !sc.states[idx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEvicting)) {
			continue
		}
		size := sc.segLengths[idx].Load()
		if size <= 0 {
			size = sc.segments[idx].Bytes
			if size <= 0 {
				size = sc.segOffsets[idx+1] - sc.segOffsets[idx]
			}
		}
		sc.curDisk.Add(-size)
		sc.stats.Evictions.Add(1)
		pieces = append(pieces, rng{sc.segOffsets[idx], size})
		evicted = append(evicted, idx)
	}
	if len(pieces) == 0 {
		return
	}

	sort.Slice(pieces, func(a, b int) bool { return pieces[a].off < pieces[b].off })

	// Coalesce adjacent ranges into the fewest possible Discard calls.
	merged := pieces[:1]
	for _, r := range pieces[1:] {
		last := &merged[len(merged)-1]
		if last.off+last.size == r.off {
			last.size += r.size
		} else {
			merged = append(merged, r)
		}
	}
	for _, r := range merged {
		if err := sc.buf.Discard(r.off, r.size); err != nil {
			sc.logger.Debug().
				Err(err).
				Int64("offset", r.off).
				Int64("size", r.size).
				Msg("buffer discard failed; slot will be overwritten on next fetch")
		}
	}

	// The disk ranges are gone; release the slots and wake anyone waiting so
	// they re-fetch into the now-punched (and no-longer-contended) range.
	for _, idx := range evicted {
		sc.states[idx].Store(uint32(StateEmpty))
		sc.wakeWaiters(idx)
	}
}

// onBufferEvict is invoked by the buffer pool after it punches a hole behind
// the read head (only when the usenet pool is configured with a disk limit —
// off by default). It marks every segment fully inside the reclaimed range
// Empty so a later read re-fetches it rather than reading a hole. Segments that
// only partially overlap are left alone; the pool only punches present ranges,
// so a partial overlap means the segment straddles the back-window boundary and
// should be kept.
func (sc *SegmentCache) onBufferEvict(off, length int64) {
	end := off + length
	startIdx, endIdx := sc.SegmentsForRange(off, length)
	for idx := startIdx; idx <= endIdx && idx < sc.segCount; idx++ {
		segStart := sc.segOffsets[idx]
		segEnd := sc.segOffsets[idx+1]
		if segStart < off || segEnd > end {
			continue // not fully contained
		}
		if sc.pinCounts[idx].Load() > 0 {
			continue
		}
		if !sc.states[idx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEmpty)) {
			continue
		}
		size := sc.segLengths[idx].Load()
		if size <= 0 {
			size = segEnd - segStart
		}
		sc.curDisk.Add(-size)
		sc.stats.Evictions.Add(1)
	}
}

// SegmentsForRange returns the segment indices covering [offset, offset+length).
func (sc *SegmentCache) SegmentsForRange(offset, length int64) (int, int) {
	if sc.segCount == 0 {
		return 0, 0
	}
	endOffset := offset + length - 1
	startIdx := sc.binarySearchSegment(offset)
	if startIdx >= sc.segCount {
		startIdx = sc.segCount - 1
	}
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
func (sc *SegmentCache) SegmentCount() int { return sc.segCount }

// TotalSize returns the total size of all segments.
func (sc *SegmentCache) TotalSize() int64 { return sc.totalSize }

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

	for i := range numShards {
		sc.shardMu[i].Lock()
		sc.shardCond[i].Broadcast()
		sc.shardMu[i].Unlock()
	}

	sc.evictWg.Wait()
	sc.sweepWg.Wait()

	if sc.buf != nil {
		_ = sc.buf.Close()
	}
	if sc.diskPath != "" {
		_ = os.RemoveAll(sc.diskPath)
	}
	return nil
}
