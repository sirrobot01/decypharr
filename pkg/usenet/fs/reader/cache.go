package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog"

	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/nntp"
)

// SegmentCache publishes verified segments as immutable RAM extents or as
// ranges in a sparse rewind file.
type SegmentCache struct {
	// Segment metadata
	segments   []SegmentMeta
	segCount   int
	segOffsets []int64 // cumulative byte offsets for binary-search lookup
	totalSize  int64
	segLengths []atomic.Int64 // bytes actually stored per segment

	// Per-segment state
	states    []atomic.Uint32
	pinCounts []atomic.Int32
	errors    []atomic.Pointer[error]

	// Storage layer.
	buf        *buffer.Buffer // sparse rewind tier; nil in window mode
	memoryMode bool
	resident   []atomic.Pointer[residentSegment]
	residentMu sync.Mutex
	residentAt []int
	residentN  atomic.Int64
	extentPool *extentPool

	// memBudget is this stream's RAM window; segBytesHint is the nominal
	// decoded size of one segment. Both feed MaxPrefetchSegments.
	memBudget    int64
	segBytesHint int64
	diskPath     string // remembered for RemoveAll on Close

	// consumedFloor steers extent eviction away from active playback.
	consumedFloor atomic.Int64
	// deliveryMode belongs to a persistent downstream cache. idleDelivery is
	// set once its last handle closes so late speculative fetches are discarded
	// instead of rebuilding a cache nobody is consuming.
	deliveryMode bool
	idleDelivery atomic.Bool

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

type residentSegment struct {
	data   []byte
	charge int64
}

const (
	numShards = 64
	shardMask = numShards - 1

	// prefetchHistoryReserve is the slice of a stream's RAM window held back
	// from the prefetcher so recently-played segments survive a scrub-back.
	prefetchHistoryReserve = 4 << 20
)

// memoryWindowSize keeps one read-ahead window forward and two behind playback.
func memoryWindowSize(cfg Config, segments []SegmentMeta) int64 {
	floor := int64(32 << 20)
	if cfg.PrefetchAhead <= 0 {
		floor = 8 << 20
	}
	segBytes := int64(750 * 1024) // typical usenet segment
	if len(segments) > 0 && segments[0].Bytes > 0 {
		segBytes = segments[0].Bytes
	}
	return max(3*int64(cfg.PrefetchAhead)*segBytes, floor)
}

// NewSegmentCache creates a bounded extent window or a sparse rewind tier.
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

	bufCfg := buffer.Config{}
	diskPath := ""
	memSize := memoryWindowSize(config, segments)
	bufCfg.MemorySize = memSize
	memoryMode := config.Retention != RetentionRewind
	if !memoryMode {
		var err error
		diskPath = config.DiskPath
		if diskPath == "" {
			diskPath, err = os.MkdirTemp("", "usenet-cache-*")
		} else {
			if err = os.MkdirAll(diskPath, 0o755); err == nil {
				diskPath, err = os.MkdirTemp(diskPath, "cache-*")
			}
		}
		if err != nil {
			cancel()
			return nil, fmt.Errorf("create cache dir: %w", err)
		}
		bufCfg.DiskPath = filepath.Join(diskPath, "segments.bin")
		bufCfg.TotalSize = totalSize
		bufCfg.ImmutableDisk = true
	}
	var buf *buffer.Buffer
	if !memoryMode {
		var err error
		buf, err = usenetBufferPool().NewBuffer(bufCfg)
		if err != nil {
			cancel()
			if diskPath != "" {
				_ = os.RemoveAll(diskPath)
			}
			return nil, fmt.Errorf("create buffer: %w", err)
		}
	}

	segBytesHint := int64(750 * 1024)
	if len(segments) > 0 && segments[0].Bytes > 0 {
		segBytesHint = segments[0].Bytes
	}

	sc := &SegmentCache{
		segments:     segments,
		segCount:     segCount,
		segOffsets:   offsets,
		totalSize:    totalSize,
		segLengths:   make([]atomic.Int64, segCount),
		states:       make([]atomic.Uint32, segCount),
		pinCounts:    make([]atomic.Int32, segCount),
		errors:       make([]atomic.Pointer[error], segCount),
		buf:          buf,
		memoryMode:   memoryMode,
		resident:     make([]atomic.Pointer[residentSegment], segCount),
		memBudget:    bufCfg.MemorySize,
		segBytesHint: segBytesHint,
		deliveryMode: config.Retention == RetentionDelivery,
		diskPath:     diskPath,
		ctx:          ctx,
		cancel:       cancel,
		logger:       logger.With().Str("component", "cache").Logger(),
		stats:        stats,
	}
	if sc.memoryMode {
		sc.extentPool = usenetExtentPool()
		sc.extentPool.register(sc)
	}

	for i := range numShards {
		sc.shardCond[i] = sync.NewCond(&sc.shardMu[i])
	}

	return sc, nil
}

// computeOffsets calculates cumulative byte offsets for segment lookup.
func computeOffsets(segments []SegmentMeta) []int64 {
	offsets := make([]int64, len(segments)+1)
	if len(segments) > 0 && segments[0].EndOffset > 0 && offsetsAscend(segments) {
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

// offsetsAscend validates offsets before they are used for range lookup.
func offsetsAscend(segments []SegmentMeta) bool {
	for i := 1; i < len(segments); i++ {
		if segments[i].StartOffset <= segments[i-1].EndOffset {
			return false
		}
	}
	return segments[len(segments)-1].EndOffset >= segments[len(segments)-1].StartOffset-1
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
	if sc.memoryMode {
		resident := sc.resident[segIdx].Load()
		if resident == nil || segOffset+length > int64(len(resident.data)) {
			sc.stats.CacheMisses.Add(1)
			return 0, false
		}
		copy(dst[:length], resident.data[segOffset:segOffset+length])
		sc.stats.CacheHits.Add(1)
		return int(length), true
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

// segmentWriter is the contract doFetch uses to stream a segment body into
// the cache. Exactly one of Finalize/Discard is called per writer.
type segmentWriter interface {
	Write(p []byte) (int, error)
	DecodeBuffer() []byte
	Adopt(decoded []byte) (int64, error)
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

	seg := sc.segments[segIdx]
	if sc.memoryMode {
		return &memorySegmentWriter{
			dataStart: seg.SegmentDataStart,
			maxBytes:  seg.Bytes,
			cache:     sc,
			segIdx:    segIdx,
		}
	}
	return &bufferStreamWriter{
		buf:       sc.buf,
		offset:    sc.segOffsets[segIdx],
		dataStart: seg.SegmentDataStart,
		maxBytes:  seg.Bytes,
		cache:     sc,
		segIdx:    segIdx,
	}
}

type memorySegmentWriter struct {
	dataStart int64
	maxBytes  int64
	cache     *SegmentCache
	segIdx    int
	resident  *residentSegment
}

func (w *memorySegmentWriter) DecodeBuffer() []byte {
	size := w.dataStart + w.maxBytes
	return make([]byte, 0, nntp.DecodedBodyCapacity(size))
}

func (w *memorySegmentWriter) Adopt(decoded []byte) (int64, error) {
	if w.dataStart < 0 || w.dataStart >= int64(len(decoded)) {
		return 0, fmt.Errorf("decoded segment starts at %d with only %d bytes", w.dataStart, len(decoded))
	}
	end := int64(len(decoded))
	if w.maxBytes > 0 {
		end = min(end, w.dataStart+w.maxBytes)
	}
	if end <= w.dataStart {
		return 0, nil
	}
	w.resident = &residentSegment{
		data:   decoded[w.dataStart:end:end],
		charge: int64(cap(decoded)),
	}
	return end - w.dataStart, nil
}

func (w *memorySegmentWriter) Write(p []byte) (int, error) {
	owned := bytes.Clone(p)
	n, err := w.Adopt(owned)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (w *memorySegmentWriter) Discard() { w.resident = nil }

func (w *memorySegmentWriter) Finalize() {
	if w.cache == nil || w.resident == nil || len(w.resident.data) == 0 {
		return
	}
	w.cache.publishResident(w.segIdx, w.resident)
	w.resident = nil
}

func (sc *SegmentCache) publishResident(segIdx int, resident *residentSegment) {
	if segIdx < 0 || segIdx >= sc.segCount || resident == nil {
		return
	}
	if resident.charge <= 0 {
		resident.charge = int64(len(resident.data))
	}

	// Reserve the global charge before publishing StateOnDisk. Reclaim may
	// lock this cache, so it must happen before residentMu is held.
	sc.extentPool.add(resident.charge)
	sc.residentMu.Lock()
	if sc.deliveryMode && sc.idleDelivery.Load() {
		// Recheck under the same lock used by ReleaseIdleDelivery: an in-flight
		// speculative fetch must not publish immediately after the idle sweep.
		sc.residentMu.Unlock()
		sc.extentPool.release(resident.charge)
		sc.states[segIdx].Store(uint32(StateEmpty))
		sc.wakeWaiters(segIdx)
		return
	}
	old := sc.resident[segIdx].Swap(resident)
	sc.residentAt = append(sc.residentAt, segIdx)
	sc.residentN.Add(resident.charge)
	if old != nil {
		sc.residentN.Add(-old.charge)
	}
	sc.segLengths[segIdx].Store(int64(len(resident.data)))
	sc.states[segIdx].Store(uint32(StateOnDisk))
	sc.residentMu.Unlock()
	if old != nil {
		sc.extentPool.release(old.charge)
	}
	sc.trimResidentTo(sc.memBudget)
	sc.wakeWaiters(segIdx)
}

func (sc *SegmentCache) trimResidentTo(target int64) int64 {
	if !sc.memoryMode || target < 0 || sc.residentN.Load() <= target {
		return 0
	}

	var released int64
	var wake []int
	sc.residentMu.Lock()
	for sc.residentN.Load() > target {
		victimPos := -1
		var victimDistance int64 = -1
		floor := sc.consumedFloor.Load()
		for pos, idx := range sc.residentAt {
			if sc.pinCounts[idx].Load() > 0 || sc.resident[idx].Load() == nil {
				continue
			}
			mid := (sc.segOffsets[idx] + sc.segOffsets[idx+1]) / 2
			distance := mid - floor
			if distance < 0 {
				distance = -distance
			}
			if distance > victimDistance {
				victimDistance = distance
				victimPos = pos
			}
		}
		if victimPos < 0 {
			break
		}
		idx := sc.residentAt[victimPos]
		sc.residentAt[victimPos] = sc.residentAt[len(sc.residentAt)-1]
		sc.residentAt = sc.residentAt[:len(sc.residentAt)-1]
		if !sc.states[idx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEvicting)) {
			continue
		}
		resident := sc.resident[idx].Swap(nil)
		if resident == nil {
			sc.states[idx].Store(uint32(StateEmpty))
			continue
		}
		sc.residentN.Add(-resident.charge)
		released += resident.charge
		sc.stats.Evictions.Add(1)
		sc.states[idx].Store(uint32(StateEmpty))
		wake = append(wake, idx)
	}
	sc.residentMu.Unlock()

	sc.extentPool.release(released)
	for _, idx := range wake {
		sc.wakeWaiters(idx)
	}
	return released
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

func (w *bufferStreamWriter) DecodeBuffer() []byte {
	return nil
}

func (w *bufferStreamWriter) Adopt(decoded []byte) (int64, error) {
	if _, err := w.Write(decoded); err != nil {
		return w.written, err
	}
	return w.written, nil
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
	w.cache.segLengths[w.segIdx].Store(w.written)
	w.cache.states[w.segIdx].Store(uint32(StateOnDisk))
	w.cache.wakeWaiters(w.segIdx)
}

// PinRange protects segments used by an active read from eviction. A race with
// an eviction already in progress is resolved by the segment state machine.
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
	if sc.deliveryMode && sc.idleDelivery.Load() {
		sc.releaseResidentSegments(0, sc.segCount)
	}
}

// ActivateDelivery marks a delivery cache as having a downstream consumer.
// A new handle calls this before it can issue reads or prefetch work.
func (sc *SegmentCache) ActivateDelivery() {
	if sc.deliveryMode {
		sc.idleDelivery.Store(false)
	}
}

// ReleaseCachedRange relinquishes complete resident segments after the
// downstream cache has successfully copied [off, off+length). Partial edge
// segments remain resident; a later cumulative acknowledgement releases them.
func (sc *SegmentCache) ReleaseCachedRange(off, length int64) int64 {
	if !sc.deliveryMode || sc.closed.Load() || length <= 0 || off < 0 || off >= sc.totalSize {
		return 0
	}
	end := off + length
	if end < off || end > sc.totalSize {
		end = sc.totalSize
	}

	first := sc.binarySearchSegment(off)
	if first < sc.segCount && sc.segOffsets[first] < off {
		first++ // the acknowledgement starts inside this segment
	}
	last := first
	for last < sc.segCount && sc.segOffsets[last+1] <= end {
		last++
	}
	return sc.releaseResidentSegments(first, last)
}

// ReleaseIdleDelivery drops every unpinned resident extent and prevents late
// speculative completions from repopulating the cache. Segment metadata and
// provider connections remain warm for a cheap reopen.
func (sc *SegmentCache) ReleaseIdleDelivery() int64 {
	if !sc.deliveryMode || sc.closed.Load() {
		return 0
	}
	sc.idleDelivery.Store(true)
	return sc.releaseResidentSegments(0, sc.segCount)
}

// releaseResidentSegments releases resident segments in [first, last).
func (sc *SegmentCache) releaseResidentSegments(first, last int) int64 {
	first = max(first, 0)
	last = min(last, sc.segCount)
	if !sc.memoryMode || first >= last {
		return 0
	}

	var released int64
	var releasedSegments int64
	var wake []int
	sc.residentMu.Lock()
	for idx := first; idx < last; idx++ {
		if sc.pinCounts[idx].Load() > 0 {
			continue
		}
		if !sc.states[idx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEvicting)) {
			continue
		}
		resident := sc.resident[idx].Swap(nil)
		if resident != nil {
			released += resident.charge
			releasedSegments++
			sc.residentN.Add(-resident.charge)
		}
		sc.states[idx].Store(uint32(StateEmpty))
		wake = append(wake, idx)
	}
	if released > 0 {
		// Acknowledgements are frequent; compact stale positions now so pool
		// pressure scans stay proportional to the live forward window.
		kept := sc.residentAt[:0]
		for _, idx := range sc.residentAt {
			if sc.resident[idx].Load() != nil {
				kept = append(kept, idx)
			}
		}
		sc.residentAt = kept
	}
	sc.residentMu.Unlock()

	if released > 0 {
		sc.extentPool.release(released)
		sc.stats.DeliveryReleases.Add(releasedSegments)
		sc.stats.DeliveryBytes.Add(released)
	}
	for _, idx := range wake {
		sc.wakeWaiters(idx)
	}
	return released
}

// IsPinned returns true if the segment has a positive pin count.
func (sc *SegmentCache) IsPinned(segIdx int) bool {
	if segIdx < 0 || segIdx >= sc.segCount {
		return false
	}
	return sc.pinCounts[segIdx].Load() > 0
}

// MaxPrefetchSegments bounds read-ahead by this stream's current pool share.
func (sc *SegmentCache) MaxPrefetchSegments() int {
	if !sc.memoryMode || sc.memBudget <= 0 || sc.segBytesHint <= 0 {
		return math.MaxInt32
	}
	budget := sc.memBudget
	if share := sc.extentPool.shareFor(sc); share > 0 && share < budget {
		budget = share
	}
	usable := budget - prefetchHistoryReserve
	if usable < sc.segBytesHint {
		// Keep a minimal window moving when the fair share is very small.
		return 1
	}
	return int(usable / sc.segBytesHint)
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

// ResetFailed transitions Failed → Empty without clobbering a concurrent fetch.
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
	if sc.states[segIdx].CompareAndSwap(uint32(StateFetching), uint32(StateEmpty)) {
		// Wake anyone parked on this fetch: the slot is Empty now and no
		// further transition is coming, so a waiter left asleep here would
		// never be woken again.
		sc.wakeWaiters(segIdx)
	}
}

// ErrSegmentEvicted reports that a segment is not cached and nobody is
// fetching it, so waiting would block forever. It is a normal outcome, not a
// failure: the caller re-fetches and retries.
var ErrSegmentEvicted = errors.New("segment evicted before read")

// WaitForSegment waits for an active fetch or eviction. Empty returns
// ErrSegmentEvicted immediately because no producer exists to wake a waiter.
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
	case StateEmpty:
		return ErrSegmentEvicted
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
	ctxStopper := context.AfterFunc(ctx, wakeShard)
	defer ctxStopper()
	cacheStopper := context.AfterFunc(sc.ctx, wakeShard)
	defer cacheStopper()

	mu.Lock()
	defer mu.Unlock()

	for {
		state = SegmentState(sc.states[segIdx].Load())
		switch state {
		case StateOnDisk:
			return nil
		case StateEmpty:
			return ErrSegmentEvicted
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

// WaitForEvictionRelease waits until an extent is fully unpublished.
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

// invalidateForRefetch makes an unreadable cached segment eligible to download.
func (sc *SegmentCache) invalidateForRefetch(segIdx int) {
	if segIdx < 0 || segIdx >= sc.segCount {
		return
	}
	sc.states[segIdx].CompareAndSwap(uint32(StateOnDisk), uint32(StateEmpty))
	sc.errors[segIdx].Store(nil)
}

// wakeWaiters wakes any WaitForSegment callers parked on this segment's shard.
func (sc *SegmentCache) wakeWaiters(segIdx int) {
	shardIdx := segIdx & shardMask
	sc.shardMu[shardIdx].Lock()
	sc.shardCond[shardIdx].Broadcast()
	sc.shardMu[shardIdx].Unlock()
}

// SetConsumedFloor publishes the slowest consumer's delivered offset.
func (sc *SegmentCache) SetConsumedFloor(off int64) {
	if off < 0 {
		return
	}
	sc.consumedFloor.Store(off)
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

	var closeErr error
	if sc.extentPool != nil {
		sc.extentPool.unregister(sc)
		var released int64
		sc.residentMu.Lock()
		for i := range sc.resident {
			if resident := sc.resident[i].Swap(nil); resident != nil {
				released += resident.charge
			}
		}
		sc.residentAt = nil
		sc.residentN.Store(0)
		sc.residentMu.Unlock()
		sc.extentPool.release(released)
	}
	if sc.buf != nil {
		closeErr = sc.buf.Close()
	}
	if sc.diskPath != "" {
		closeErr = errors.Join(closeErr, os.RemoveAll(sc.diskPath))
	}
	return closeErr
}
