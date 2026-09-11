package reader

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
)

// SegmentFetcher deduplicates downloads and submits them to the shared scheduler.
type SegmentFetcher struct {
	client *nntp.Client
	cache  *SegmentCache
	config Config
	logger zerolog.Logger
	stats  *ReaderStats

	// Request deduplication
	inFlight   map[int]*fetchPromise
	inFlightMu sync.Mutex

	// Background prefetch
	scheduler      *FetchScheduler
	ownedScheduler bool
	prefetchQueued []atomic.Uint64 // one deduplication bit per segment
	prefetchGen    atomic.Uint64
	taskMu         sync.Mutex
	taskWg         sync.WaitGroup
	closing        bool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
}

// fetchPromise allows multiple goroutines to wait for the same segment download.
type fetchPromise struct {
	done chan struct{}
	err  error
}

// NewSegmentFetcher creates a new segment fetcher.
func NewSegmentFetcher(
	ctx context.Context,
	client *nntp.Client,
	cache *SegmentCache,
	config Config,
	stats *ReaderStats,
	logger zerolog.Logger,
) *SegmentFetcher {
	ctx, cancel := context.WithCancel(ctx)

	maxConns := config.MaxConnections
	if maxConns < 1 {
		maxConns = 8
	}

	sf := &SegmentFetcher{
		client:   client,
		cache:    cache,
		config:   config,
		logger:   logger.With().Str("component", "fetcher").Logger(),
		stats:    stats,
		inFlight: make(map[int]*fetchPromise),
		// A packed bitmap avoids per-segment goroutines and large bool arrays.
		prefetchQueued: make([]atomic.Uint64, (cache.SegmentCount()+63)/64),
		ctx:            ctx,
		cancel:         cancel,
	}
	sf.scheduler = config.Scheduler
	if sf.scheduler == nil {
		workers := maxConns
		if client == nil {
			workers = 0
		}
		sf.scheduler = NewFetchScheduler(workers)
		sf.ownedScheduler = true
	}

	return sf
}

// Fetch schedules a foreground download and waits for it. Provider concurrency
// is shared across readers, so opening more files cannot multiply workers.
func (sf *SegmentFetcher) Fetch(ctx context.Context, segIdx int) error {
	return sf.scheduleAndWait(ctx, priorityDemand, func() error {
		return sf.fetchWithRetryDirect(sf.ctx, segIdx)
	})
}

func (sf *SegmentFetcher) scheduleAndWait(ctx context.Context, priority fetchPriority, run func() error) error {
	return sf.waitScheduled(ctx, sf.schedule(ctx, priority, run))
}

func (sf *SegmentFetcher) schedule(ctx context.Context, priority fetchPriority, run func() error) <-chan error {
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan error, 1)
	if sf.scheduler.workers == 0 && priority == priorityDemand {
		done <- run()
		return done
	}
	if !sf.submit(ctx, priority, func() { done <- run() }, func() { done <- ErrCacheClosed }) {
		if err := ctx.Err(); err != nil {
			done <- err
		} else if err := sf.ctx.Err(); err != nil {
			done <- err
		} else {
			done <- ErrCacheClosed
		}
	}
	return done
}

func (sf *SegmentFetcher) waitScheduled(ctx context.Context, done <-chan error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-sf.ctx.Done():
		return sf.ctx.Err()
	}
}

func (sf *SegmentFetcher) submit(ctx context.Context, priority fetchPriority, run, drop func()) bool {
	sf.taskMu.Lock()
	if sf.closing {
		sf.taskMu.Unlock()
		return false
	}
	sf.taskWg.Add(1)
	sf.taskMu.Unlock()

	if sf.scheduler.submit(ctx, priority, func() {
		defer sf.taskWg.Done()
		run()
	}, func() {
		defer sf.taskWg.Done()
		if drop != nil {
			drop()
		}
	}) {
		return true
	}
	sf.taskWg.Done()
	return false
}

// fetchDirect performs one deduplicated fetch inside a scheduler worker.
func (sf *SegmentFetcher) fetchDirect(ctx context.Context, segIdx int) error {
	// Fast path: already cached, or wait until an extent eviction completes.
	for {
		state := sf.cache.GetState(segIdx)
		if state == StateEvicting {
			if err := sf.cache.WaitForEvictionRelease(ctx, segIdx); err != nil {
				return err
			}
			continue // slot is Empty now; re-evaluate
		}
		switch state {
		case StateOnDisk:
			return nil
		case StateFailed:
			return sf.cache.GetError(segIdx)
		}
		break
	}

	// Check if someone else is already fetching
	sf.inFlightMu.Lock()
	if promise, ok := sf.inFlight[segIdx]; ok {
		sf.inFlightMu.Unlock()
		// Wait for existing fetch
		select {
		case <-promise.done:
			return promise.err
		case <-ctx.Done():
			return ctx.Err()
		case <-sf.ctx.Done():
			return sf.ctx.Err()
		}
	}

	// We're the first - create promise
	promise := &fetchPromise{done: make(chan struct{})}
	sf.inFlight[segIdx] = promise
	sf.inFlightMu.Unlock()

	// Actually fetch
	err := sf.doFetch(ctx, segIdx)
	promise.err = err
	close(promise.done)

	// Cleanup
	sf.inFlightMu.Lock()
	delete(sf.inFlight, segIdx)
	sf.inFlightMu.Unlock()

	return err
}

// doFetch performs the actual NNTP download.
func (sf *SegmentFetcher) doFetch(ctx context.Context, segIdx int) error {
	return sf.doFetchAttempt(ctx, segIdx, 0)
}

// doFetchRestarts bounds how many times a single doFetch may restart because
// another party held the slot and then lost it (a cancelled fetch, or an
// eviction landing between the state check and the claim). Each restart does
// real work, so this only stops a pathological loop.
const doFetchRestarts = 4

func (sf *SegmentFetcher) doFetchAttempt(ctx context.Context, segIdx, restarts int) error {
	seg := sf.cache.GetSegment(segIdx)
	if seg == nil {
		return ErrSegmentNotFound
	}
	if restarts > doFetchRestarts {
		return fmt.Errorf("segment %d: slot contended after %d restarts", segIdx, restarts)
	}

	// Try to mark as fetching (atomic transition Empty -> Fetching)
	if !sf.cache.MarkFetching(segIdx) {
		// Someone else is fetching or it's already cached
		state := sf.cache.GetState(segIdx)
		switch state {
		case StateOnDisk:
			return nil
		case StateFailed:
			return sf.cache.GetError(segIdx)
		case StateFetching:
			// Wait for the other fetcher. If it released the slot (cancel)
			// or the segment was dropped right after it landed, the wait
			// reports ErrSegmentEvicted rather than blocking on an event
			// that is no longer coming — retry the fetch ourselves.
			err := sf.cache.WaitForSegment(ctx, segIdx)
			if errors.Is(err, ErrSegmentEvicted) {
				return sf.doFetchAttempt(ctx, segIdx, restarts+1)
			}
			return err
		case StateEvicting:
			if err := sf.cache.WaitForEvictionRelease(ctx, segIdx); err != nil {
				return err
			}
			return sf.doFetchAttempt(ctx, segIdx, restarts+1)
		}
	}

	messageID := seg.MessageID
	timeout := sf.config.DownloadTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	downloadCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// ExecuteWithFailover already retries across configured providers.
	err := sf.client.ExecuteWithFailover(downloadCtx, func(conn *nntp.Connection) error {
		stopCancel := context.AfterFunc(downloadCtx, func() {
			_ = conn.Close()
		})
		defer stopCancel()

		writer := sf.cache.StreamWriter(segIdx)
		if writer == nil {
			return ErrCacheClosed
		}

		var n int64
		var err error
		if decodeBuffer := writer.DecodeBuffer(); decodeBuffer != nil {
			var decoded []byte
			decoded, err = conn.DecodeBodyInto(messageID, decodeBuffer)
			if err == nil {
				n, err = writer.Adopt(decoded)
			}
		} else {
			n, err = conn.StreamBody(messageID, writer)
		}
		if err != nil {
			writer.Discard()
			if ctxErr := downloadCtx.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		if ctxErr := downloadCtx.Err(); ctxErr != nil {
			writer.Discard()
			return ctxErr
		}

		// Treat zero-byte articles as missing — the article exists on the
		// server but its body is empty/corrupted after yEnc decoding.
		if n == 0 {
			writer.Discard()
			return &nntp.Error{
				Type:    nntp.ErrorTypeArticleNotFound,
				Message: "article produced no data after decoding",
			}
		}

		// Commit (updates cache state to StateOnDisk).
		writer.Finalize()

		return nil
	})

	if err != nil {
		sf.stats.DownloadErrors.Add(1)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			sf.cache.ReleaseFetching(segIdx)
			return err
		}
		sf.cache.MarkFailed(segIdx, err)
		return err
	}

	sf.stats.Downloads.Add(1)
	return nil
}

func (sf *SegmentFetcher) markPrefetchQueued(segIdx int) bool {
	if segIdx < 0 || segIdx >= sf.cache.SegmentCount() {
		return false
	}
	word := &sf.prefetchQueued[segIdx>>6]
	mask := uint64(1) << uint(segIdx&63)
	for {
		old := word.Load()
		if old&mask != 0 {
			return false
		}
		if word.CompareAndSwap(old, old|mask) {
			return true
		}
	}
}

func (sf *SegmentFetcher) clearPrefetchQueued(segIdx int) {
	if segIdx < 0 || segIdx >= sf.cache.SegmentCount() {
		return
	}
	word := &sf.prefetchQueued[segIdx>>6]
	mask := uint64(1) << uint(segIdx&63)
	word.And(^mask)
}

// QueuePrefetch submits speculative work without creating a per-file worker.
func (sf *SegmentFetcher) QueuePrefetch(segIdx int) {
	sf.queueSpeculative(segIdx, priorityPrefetch)
}

func (sf *SegmentFetcher) QueueProbe(segIdx int) {
	if sf.scheduler.workers > 1 {
		sf.queueSpeculative(segIdx, priorityProbe)
	}
}

func (sf *SegmentFetcher) QueueProbeRange(startSeg, endSeg int) {
	for i := startSeg; i <= endSeg; i++ {
		sf.QueueProbe(i)
	}
}

func (sf *SegmentFetcher) queueSpeculative(segIdx int, priority fetchPriority) {
	state := sf.cache.GetState(segIdx)
	if state == StateOnDisk || state == StateFetching {
		return
	}
	if !sf.markPrefetchQueued(segIdx) {
		return
	}
	gen := sf.prefetchGen.Load()
	if !sf.submit(sf.ctx, priority, func() {
		if gen != sf.prefetchGen.Load() {
			return
		}
		defer func() {
			if gen == sf.prefetchGen.Load() {
				sf.clearPrefetchQueued(segIdx)
			}
		}()
		sf.prefetchOne(segIdx)
	}, func() {
		if gen == sf.prefetchGen.Load() {
			sf.clearPrefetchQueued(segIdx)
		}
	}) {
		sf.clearPrefetchQueued(segIdx)
		sf.stats.PrefetchMisses.Add(1)
	}
}

// QueuePrefetchRange queues multiple segments for prefetch.
func (sf *SegmentFetcher) QueuePrefetchRange(startSeg, endSeg int) {
	for i := startSeg; i <= endSeg; i++ {
		sf.QueuePrefetch(i)
	}
}

func (sf *SegmentFetcher) prefetchOne(segIdx int) {
	state := sf.cache.GetState(segIdx)
	if state == StateOnDisk {
		sf.stats.PrefetchHits.Add(1)
		return
	}

	fetchCtx, cancel := context.WithTimeout(sf.ctx, sf.config.DownloadTimeout)
	err := sf.fetchWithRetryDirect(fetchCtx, segIdx)
	cancel()

	if err != nil && err != context.Canceled && err != context.DeadlineExceeded {
		sf.logger.Debug().Err(err).Int("segment", segIdx).Msg("prefetch failed")
	}
}

// EnsureSegments fetches all missing segments through the shared scheduler.
func (sf *SegmentFetcher) EnsureSegments(ctx context.Context, startSeg, endSeg int) error {
	return sf.PrepareSegments(ctx, startSeg, endSeg)()
}

// PrepareSegments submits all required downloads before returning a waiter.
// Callers can enqueue speculative work after demand owns its queue position.
func (sf *SegmentFetcher) PrepareSegments(ctx context.Context, startSeg, endSeg int) func() error {
	var missing []int
	for i := startSeg; i <= endSeg; i++ {
		if sf.cache.GetState(i) != StateOnDisk {
			missing = append(missing, i)
		}
	}
	if len(missing) == 0 {
		return func() error { return nil }
	}

	waits := make([]<-chan error, len(missing))
	for j, segIdx := range missing {
		waits[j] = sf.schedule(ctx, priorityDemand, func() error {
			return sf.fetchWithRetryDirect(sf.ctx, segIdx)
		})
	}
	return func() error {
		for _, wait := range waits {
			if err := sf.waitScheduled(ctx, wait); err != nil {
				return err
			}
		}
		return nil
	}
}

// CancelPendingPrefetch invalidates queued work in O(bitmap words). Tasks
// already running finish and remain reusable; stale queued tasks become no-ops.
func (sf *SegmentFetcher) CancelPendingPrefetch() {
	sf.prefetchGen.Add(1)
	var cancelled int64
	for i := range sf.prefetchQueued {
		cancelled += int64(bits.OnesCount64(sf.prefetchQueued[i].Swap(0)))
	}
	if cancelled > 0 {
		sf.stats.PrefetchCancelled.Add(cancelled)
	}
}

func (sf *SegmentFetcher) pendingPrefetch() int {
	var pending int
	for i := range sf.prefetchQueued {
		pending += bits.OnesCount64(sf.prefetchQueued[i].Load())
	}
	return pending
}

func (sf *SegmentFetcher) fetchWithRetryDirect(ctx context.Context, segIdx int) error {
	maxAttempts := sf.config.MaxRetries
	if maxAttempts < 1 {
		maxAttempts = 3
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Clear the failed state so the segment can be re-fetched, then
			// back off briefly before retrying. ResetFailed is a CAS: if a
			// concurrent reader fetched the segment meanwhile it stays OnDisk.
			sf.cache.ResetFailed(segIdx)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-sf.ctx.Done():
				return sf.ctx.Err()
			case <-time.After(sf.retryBackoff(attempt)):
			}
		}

		err := sf.fetchDirect(ctx, segIdx)
		if err == nil {
			return nil
		}
		lastErr = err

		// Don't retry permanent errors or cancellations.
		if nntp.IsArticleNotFoundError(err) || nntp.IsYencDecodeError(err) || ctx.Err() != nil || sf.ctx.Err() != nil {
			return err
		}
		// The failover layer already retried per provider and across
		// providers for this error; allow one outer pass for transient
		// blips, then stop rather than multiplying the attempt budget.
		if nntp.IsAllProvidersFailed(err) && attempt >= 1 {
			return err
		}
	}
	return lastErr
}

// retryBackoff returns the delay before the given (1-indexed) retry attempt.
func (sf *SegmentFetcher) retryBackoff(attempt int) time.Duration {
	base := sf.config.RetryDelay
	if base <= 0 {
		base = time.Second
	}
	d := base << (attempt - 1)
	if maxDelay := 5 * time.Second; d > maxDelay {
		d = maxDelay
	}
	return d
}

// Close cancels this reader's work. Shared scheduler workers outlive it.
func (sf *SegmentFetcher) Close() {
	sf.taskMu.Lock()
	if sf.closing {
		sf.taskMu.Unlock()
		return
	}
	sf.closing = true
	sf.taskMu.Unlock()
	sf.cancel()
	if sf.ownedScheduler {
		sf.scheduler.Close()
	}
	sf.taskWg.Wait()
}

// Error types
var (
	ErrSegmentNotFound = &segmentError{msg: "segment not found"}
	ErrCacheClosed     = &segmentError{msg: "cache closed"}
)

type segmentError struct {
	msg string
}

func (e *segmentError) Error() string {
	return e.msg
}
