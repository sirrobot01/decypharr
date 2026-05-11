package vfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/manager"
	fuseconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

const (
	// maxDownloaderIdleTime is how long a downloader waits before stopping
	maxDownloaderIdleTime = 5 * time.Second
	// maxSkipBytes is how far a downloader will skip before restarting
	maxSkipBytes = 1 << 20 // 1MB
	// maxErrorCount is the number of errors before giving up
	maxErrorCount = 10
	// downloaderWindow is how close a read must be to reuse a downloader
	downloaderWindow = 4 * 1024 * 1024 // 4MB
	// kickerInterval is how often the safety-net ticker checks waiters and idle timeout
	kickerInterval = 5 * time.Second
	// activeWaiterKickerInterval is the faster fallback cadence while readers are blocked.
	activeWaiterKickerInterval = 1 * time.Second
	// idleTimeout is how long before stopping all downloaders due to inactivity
	idleTimeout = 30 * time.Second
	// circuitCooldownDuration is how long to block requests after max errors reached
	circuitCooldownDuration = 20 * time.Minute
	// noProgressTimeout is the max time a stream attempt may run without any bytes written.
	noProgressTimeout = 45 * time.Second
	// noProgressCheckInterval is how often stall detection checks for forward progress.
	noProgressCheckInterval = 1 * time.Second
	// maxChunkSizeMultiplier caps adaptive chunk growth at this multiple of baseChunkSize.
	// Without a cap, binary doubling eventually produces chunk sizes in the GB range,
	// causing oversized HTTP range requests that are wasteful on seeks.
	maxChunkSizeMultiplier = 16
)

// Downloaders coordinates multiple concurrent downloads to a cache item
type Downloaders struct {
	parentCtx     context.Context
	ctx           context.Context
	cancel        context.CancelFunc
	item          *CacheItem
	manager       *manager.Manager
	chunkSize     int64
	readAheadSize int64
	retries       int

	mu         sync.Mutex
	dls        []*downloader
	waiters    []waiter
	errorCount int
	lastErr    error
	closed     bool
	wg         sync.WaitGroup

	// streamID is the active stream registration ID for tracking
	streamID string

	// Atomic waiter count for fast-path check (avoids locking dls.mu in Write() when no waiters)
	waiterCount atomic.Int32

	// Idle timeout tracking
	lastActivity atomic.Int64  // Unix nano timestamp of last download activity
	idle         atomic.Bool   // True when all downloaders stopped due to idle
	kickerDone   chan struct{} // Signals kicker goroutine has exited

	// Circuit breaker - blocks all requests when max errors reached
	circuitOpen   atomic.Bool  // True when circuit is "open" (blocking all requests)
	circuitOpenAt atomic.Int64 // Unix nano timestamp when circuit opened
}

// ensureStreamTracked makes sure the active stream is registered when reads begin.
func (dls *Downloaders) ensureStreamTracked() {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	if dls.closed || dls.streamID != "" {
		return
	}

	dls.streamID = dls.manager.TrackStream(dls.item.entry, dls.item.filename, "DFS")
}

// untrackStreamLocked removes the stream registration. Caller must hold dls.mu.
func (dls *Downloaders) untrackStreamLocked() {
	if dls.streamID == "" {
		return
	}
	dls.manager.UntrackStream(dls.streamID)
	dls.streamID = ""
}

// waiter represents a caller waiting for a range to be downloaded
type waiter struct {
	r       ranges.Range
	errChan chan<- error
}

// downloader represents a single download goroutine
type downloader struct {
	dls    *Downloaders
	quit   chan struct{}
	kick   chan struct{}
	ctx    context.Context    // per-downloader context; canceled by stop()
	cancel context.CancelFunc // cancels ctx

	mu        sync.Mutex
	start     int64 // Starting offset
	offset    int64 // Current offset
	maxOffset int64 // How far to download
	skipped   int64 // Consecutive skipped bytes
	stopped   bool
	closed    bool

	baseChunkSize    int64
	currentChunkSize int64

	wg sync.WaitGroup

	idleTimer *time.Timer
}

// startNoProgressWatchdog cancels an in-flight stream attempt when no bytes are
// observed for the configured timeout window.
func startNoProgressWatchdog(
	ctx context.Context,
	timeout time.Duration,
	interval time.Duration,
	lastProgressNanos *atomic.Int64,
	cancel context.CancelFunc,
	timedOut *atomic.Bool,
) func() {
	if timeout <= 0 || lastProgressNanos == nil || cancel == nil {
		return func() {}
	}
	if interval <= 0 || interval > timeout {
		interval = timeout / 5
		if interval <= 0 {
			interval = time.Second
		}
	}

	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			close(done)
		})
	}

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				last := lastProgressNanos.Load()
				now := time.Now().UnixNano()
				if last == 0 {
					lastProgressNanos.Store(now)
					continue
				}
				if now-last >= int64(timeout) {
					if timedOut != nil {
						timedOut.Store(true)
					}
					cancel()
					return
				}
			}
		}
	}()

	return stop
}

// NewDownloaders creates a new download coordinator
func NewDownloaders(ctx context.Context, mgr *manager.Manager, item *CacheItem, cfg *fuseconfig.FuseConfig) *Downloaders {
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	chunkSize := cfg.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 4 * 1024 * 1024
	}
	readAheadSize := cfg.ReadAheadSize
	if readAheadSize <= 0 {
		readAheadSize = chunkSize * 4 // Default: 4 chunks ahead
	}
	retries := cfg.Retries
	if retries <= 0 {
		retries = 3
	}

	dls := &Downloaders{
		parentCtx:     parentCtx,
		ctx:           ctx,
		cancel:        cancel,
		item:          item,
		manager:       mgr,
		chunkSize:     chunkSize,
		readAheadSize: readAheadSize,
		retries:       retries,
		// streamID is populated lazily when the first read occurs.
		streamID: "",
	}
	dls.touchActivity() // Initialize activity timestamp

	// Background kicker to handle stalled waiters and idle detection
	dls.startKicker()

	return dls
}

// Download blocks until the range r is on disk, or until ctx is canceled.
func (dls *Downloaders) Download(ctx context.Context, r ranges.Range) error {
	// Circuit breaker: reject immediately if circuit is open
	if dls.isCircuitOpen() {
		lastErr := dls.getLastErr()
		if lastErr == nil {
			return errors.New("circuit breaker open, cooldown active")
		}
		return fmt.Errorf("circuit breaker open, cooldown active: last error: %w", lastErr)
	}

	dls.ensureStreamTracked()

	// Update activity timestamp for idle detection
	dls.touchActivity()

	// Lazy restart: if we went idle, restart the kicker goroutine
	if dls.idle.Load() {
		dls.idle.Store(false)
		dls.ensureKickerRunning()
	}

	dls.mu.Lock()
	if dls.closed {
		dls.mu.Unlock()
		return errors.New("downloaders closed")
	}

	// Fast path: already have it
	if dls.item.HasRange(r) {
		if err := dls.ensureDownloaderLocked(r); err != nil {
			dls.mu.Unlock()
			return err
		}
		dls.mu.Unlock()
		return nil
	}
	// Create waiter channel
	errChan := make(chan error, 1)
	dls.waiters = append(dls.waiters, waiter{r: r, errChan: errChan})
	dls.waiterCount.Add(1)

	// Ensure downloader running
	if err := dls.ensureDownloaderLocked(r); err != nil {
		// Remove our waiter on error
		dls.removeWaiterLocked(errChan)
		dls.mu.Unlock()
		return err
	}

	dls.mu.Unlock()

	// Block until range is fulfilled, caller canceled, or error.
	// Selecting on ctx.Done() prevents goroutine leaks when the FUSE read
	// is interrupted (client disconnect, read timeout, unmount).
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		dls.mu.Lock()
		dls.removeWaiterLocked(errChan)
		dls.mu.Unlock()
		return ctx.Err()
	}
}

// removeWaiterLocked removes a waiter by its channel (call with lock held).
// Order is not significant, so swap-with-last is used for O(1) removal.
func (dls *Downloaders) removeWaiterLocked(errChan chan<- error) {
	for i, w := range dls.waiters {
		if w.errChan == errChan {
			last := len(dls.waiters) - 1
			dls.waiters[i] = dls.waiters[last]
			dls.waiters = dls.waiters[:last]
			dls.waiterCount.Add(-1)
			return
		}
	}
}

func (dls *Downloaders) getLastErr() error {
	dls.mu.Lock()
	defer dls.mu.Unlock()
	return dls.lastErr
}

// ensureDownloaderLocked finds or creates a downloader for the range.
// It extends the requested range by readAheadSize before clipping to missing
// data, so the downloader's target always stays ahead of the read position.
// The downloader idle timeout naturally limits waste on probes/seeks.
func (dls *Downloaders) ensureDownloaderLocked(r ranges.Range) error {
	// Extend by read-ahead then clip to actual missing bytes.
	r = dls.extendAndFindMissingRangeLocked(r)

	// If the requested range + read-ahead is already present, we just need
	// to kick an existing downloader to prevent idle timeout. No new download needed.
	if r.IsEmpty() {
		dls.kickExistingDownloaderLocked(r.Pos)
		return nil
	}

	// Target end: download the full missing range
	targetEnd := r.Pos + r.Size

	// Check error count
	if dls.errorCount >= maxErrorCount {
		if dls.lastErr == nil {
			return fmt.Errorf("too many errors (%d)", dls.errorCount)
		}
		return fmt.Errorf("too many errors (%d): last error: %w", dls.errorCount, dls.lastErr)
	}

	// Look for existing downloader in range
	dls.removeClosed()
	if dl := dls.findDownloaderForPosLocked(r.Pos); dl != nil {
		// Extend existing downloader
		dl.setMaxOffset(targetEnd)
		return nil
	}

	// Start new downloader
	return dls.newDownloaderLocked(r, targetEnd)
}

// extendAndFindMissingRangeLocked expands a request by read-ahead and returns
// only the missing tail that must be downloaded.
func (dls *Downloaders) extendAndFindMissingRangeLocked(r ranges.Range) ranges.Range {
	bufferWindow := dls.readAheadSize
	if bufferWindow <= 0 {
		bufferWindow = dls.chunkSize * 4
	}

	r.Size += bufferWindow
	if r.Pos+r.Size > dls.item.info.Size {
		r.Size = dls.item.info.Size - r.Pos
	}
	return dls.item.FindMissing(r)
}

func (dls *Downloaders) downloaderMatchWindowLocked() int64 {
	window := int64(downloaderWindow)
	if half := dls.chunkSize / 2; half > window {
		window = half
	}
	return window
}

func (dls *Downloaders) findDownloaderForPosLocked(pos int64) *downloader {
	window := dls.downloaderMatchWindowLocked()
	for _, dl := range dls.dls {
		start, offset := dl.getRange()
		if pos >= start && pos < offset+window {
			return dl
		}
	}
	return nil
}

// kickExistingDownloaderLocked kicks a nearby downloader to prevent idle timeout.
// Caller must hold dls.mu.
func (dls *Downloaders) kickExistingDownloaderLocked(pos int64) {
	dl := dls.findDownloaderForPosLocked(pos)
	if dl == nil {
		return
	}
	_, offset := dl.getRange()
	dl.setMaxOffset(offset) // kick without extending
}

// newDownloaderLocked creates and starts a new downloader
func (dls *Downloaders) newDownloaderLocked(r ranges.Range, targetEnd int64) error {
	baseChunk := dls.chunkSize
	if baseChunk <= 0 {
		baseChunk = 4 * 1024 * 1024
	}

	// Each downloader gets its own context derived from the Downloaders context.
	// stop() cancels dlCtx, which interrupts any in-flight manager.Stream call
	// without having to cancel the shared dls.ctx.
	dlCtx, dlCancel := context.WithCancel(dls.ctx)

	dl := &downloader{
		dls:              dls,
		quit:             make(chan struct{}),
		kick:             make(chan struct{}, 1),
		ctx:              dlCtx,
		cancel:           dlCancel,
		start:            r.Pos,
		offset:           r.Pos,
		maxOffset:        targetEnd,
		baseChunkSize:    baseChunk,
		currentChunkSize: baseChunk,
	}

	dls.dls = append(dls.dls, dl)

	// Track active download count
	dls.item.cache.activeDownloads.Add(1)

	dl.wg.Add(1)
	go func() {
		defer dl.wg.Done()
		defer dls.item.cache.activeDownloads.Add(-1)
		defer dlCancel() // always release the per-downloader context
		n, err := dl.run()
		dl.close(err)
		// Only count real errors. If dl.ctx was canceled (intentional stop/close),
		// the error is not a network/server failure and must not trip the circuit breaker.
		if dl.ctx.Err() == nil {
			dls.countErrors(n, err)
		}
		dls.kickWaiters()
	}()

	return nil
}

// removeClosed removes closed downloaders from the list
func (dls *Downloaders) removeClosed() {
	newDls := dls.dls[:0]
	for _, dl := range dls.dls {
		if !dl.isClosed() {
			newDls = append(newDls, dl)
		}
	}
	dls.dls = newDls
}

// countErrors tracks errors and resets on success.
// Context cancellation (intentional stop/close) is never counted — it must not
// trip the circuit breaker, because the downloader was halted on purpose.
func (dls *Downloaders) countErrors(n int64, err error) {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	if err == nil && n > 0 {
		dls.errorCount = 0
		dls.lastErr = nil
		// Success resets circuit breaker
		dls.resetCircuitLocked()
		return
	}
	if err != nil {
		// Intentional stop/shutdown — not a real failure.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		dls.errorCount++
		dls.lastErr = err
		if !customerror.IsSilentError(err) {
			dls.item.logger.Debug().Err(err).Int("count", dls.errorCount).Msg("download error")
		}
		if !customerror.IsRetriableError(err) {
			dls.errorCount = maxErrorCount
		}
		// Trip circuit breaker when max errors reached
		if dls.errorCount >= maxErrorCount {
			dls.openCircuitLocked()
		}
	}
}

// kickWaiters checks all waiters and fulfills completed ones
func (dls *Downloaders) kickWaiters() {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	if len(dls.waiters) == 0 {
		return
	}

	// Check circuit state once to avoid spinning
	circuitOpen := dls.circuitOpen.Load()

	fulfilled := 0
	remaining := dls.waiters[:0]
	for _, w := range dls.waiters {
		// Clip range to actual file size
		r := w.r
		r.Clip(dls.item.info.Size)

		if dls.item.HasRange(r) {
			w.errChan <- nil // Fulfilled!
			fulfilled++
		} else if circuitOpen || dls.errorCount >= maxErrorCount {
			// Circuit is open or max errors reached - fail waiter without creating new downloaders
			w.errChan <- dls.lastErr
			fulfilled++
		} else {
			remaining = append(remaining, w)
		}
	}
	dls.waiters = remaining
	if fulfilled > 0 {
		dls.waiterCount.Add(-int32(fulfilled))
	}

	// Spawn at most one missing downloader per kick. Re-ensuring for every
	// waiter can create duplicate stream calls for the same range under load.
	if len(remaining) == 0 || circuitOpen || dls.errorCount >= maxErrorCount {
		return
	}

	// If the shared context is already canceled, fail all remaining waiters
	// immediately instead of spawning downloaders that exit instantly and
	// call kickWaiters() again — that creates a CPU-spinning goroutine loop.
	if dls.ctx.Err() != nil {
		ctxErr := dls.ctx.Err()
		for _, w := range remaining {
			w.errChan <- ctxErr
		}
		dls.waiters = remaining[:0]
		dls.waiterCount.Store(0)
		return
	}

	dls.removeClosed()
	for _, w := range remaining {
		missing := dls.extendAndFindMissingRangeLocked(w.r)
		if missing.IsEmpty() {
			continue
		}
		if dls.findDownloaderForPosLocked(missing.Pos) != nil {
			continue
		}
		_ = dls.ensureDownloaderLocked(w.r)
		break
	}
}

// Close stops all downloaders and returns unfulfilled waiters with error
func (dls *Downloaders) Close(inErr error) error {
	dls.mu.Lock()
	if dls.closed {
		dls.mu.Unlock()
		return nil
	}
	dls.closed = true
	dls.untrackStreamLocked()

	// Copy slice before unlocking to avoid races while waiting.
	dlsCopy := make([]*downloader, len(dls.dls))
	copy(dlsCopy, dls.dls)

	// Stop all downloaders
	for _, dl := range dlsCopy {
		dl.stop()
	}
	dls.mu.Unlock()

	// Cancel first so any blocked stream operation can exit promptly.
	dls.cancel()

	// Wait for downloaders to finish
	for _, dl := range dlsCopy {
		dl.wg.Wait()
	}

	dls.wg.Wait()

	// Close remaining waiters
	dls.mu.Lock()
	for _, w := range dls.waiters {
		if inErr != nil {
			w.errChan <- inErr
		} else {
			w.errChan <- errors.New("downloaders closed")
		}
	}
	dls.waiterCount.Store(0)
	dls.waiters = nil
	dls.dls = nil
	dls.mu.Unlock()

	return nil
}

// touchActivity updates the last activity timestamp
func (dls *Downloaders) touchActivity() {
	dls.lastActivity.Store(time.Now().UnixNano())
}

// isCircuitOpen returns true if the circuit breaker is open and cooldown hasn't expired
func (dls *Downloaders) isCircuitOpen() bool {
	if !dls.circuitOpen.Load() {
		return false
	}
	// Check if cooldown has expired using raw nanoseconds (no allocation)
	openedAt := dls.circuitOpenAt.Load()
	if openedAt == 0 {
		return false
	}
	if time.Now().UnixNano()-openedAt >= int64(circuitCooldownDuration) {
		// Cooldown expired - reset circuit and clear error budget
		dls.mu.Lock()
		openedAt = dls.circuitOpenAt.Load()
		if openedAt != 0 && time.Now().UnixNano()-openedAt >= int64(circuitCooldownDuration) {
			dls.circuitOpen.Store(false)
			dls.circuitOpenAt.Store(0)
			dls.errorCount = 0
			dls.lastErr = nil
		}
		dls.mu.Unlock()
		return false
	}
	return true
}

// openCircuitLocked trips the circuit breaker. Caller must hold dls.mu.
func (dls *Downloaders) openCircuitLocked() {
	if dls.circuitOpen.Load() {
		return // Already open
	}
	dls.circuitOpen.Store(true)
	dls.circuitOpenAt.Store(time.Now().UnixNano())
	dls.item.cache.circuitBreakers.Add(1)
}

// resetCircuitLocked resets the circuit breaker after successful download. Caller must hold dls.mu.
func (dls *Downloaders) resetCircuitLocked() {
	if !dls.circuitOpen.Load() {
		return // Already closed
	}
	dls.circuitOpen.Store(false)
	dls.circuitOpenAt.Store(0)
	dls.item.cache.circuitBreakers.Add(-1)
}

// checkIdleTimeout returns true if idle timeout has been reached and stops all downloaders
func (dls *Downloaders) checkIdleTimeout() bool {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	// Don't timeout if already closed or already idle
	if dls.closed {
		return true
	}

	// Don't timeout if there are active waiters
	if len(dls.waiters) > 0 {
		return false
	}

	// Check if any downloaders are still running
	activeDownloaders := 0
	for _, dl := range dls.dls {
		if !dl.isClosed() {
			activeDownloaders++
		}
	}

	// Check idle timeout
	lastActivity := dls.lastActivity.Load()
	if lastActivity == 0 {
		return false
	}

	idleDuration := time.Since(time.Unix(0, lastActivity))
	if idleDuration < idleTimeout {
		return false
	}

	// Idle timeout reached - stop all downloaders
	for _, dl := range dls.dls {
		dl.stop()
	}
	dls.dls = nil
	dls.untrackStreamLocked()
	dls.idle.Store(true)

	// Reset error budget so the next session starts fresh.
	// Errors from the previous session must not carry into a resumed session —
	// that would shrink the error budget and could immediately trip the circuit
	// breaker the next time the user starts playback.
	dls.errorCount = 0
	dls.lastErr = nil
	dls.resetCircuitLocked()

	return true
}

// StopAll stops all active downloaders but keeps the Downloaders struct alive
// for potential reuse. This is called when all file handles are closed.
func (dls *Downloaders) StopAll() {
	dls.mu.Lock()
	if dls.closed {
		dls.mu.Unlock()
		return
	}
	dls.untrackStreamLocked()

	// Copy slice before unlocking to avoid race during Wait
	dlsCopy := make([]*downloader, len(dls.dls))
	copy(dlsCopy, dls.dls)
	waitersCopy := make([]waiter, len(dls.waiters))
	copy(waitersCopy, dls.waiters)
	dls.waiters = nil
	dls.waiterCount.Store(0)

	// Stop all downloaders
	for _, dl := range dlsCopy {
		dl.stop()
	}
	dls.dls = nil
	dls.idle.Store(true)
	oldCancel := dls.cancel
	dls.mu.Unlock()

	// Cancel active context so in-flight Stream calls can be interrupted.
	oldCancel()

	// Unblock any pending readers waiting on ranges from old downloaders.
	for _, w := range waitersCopy {
		w.errChan <- errors.New("downloaders stopped")
	}

	// Wait for them to finish (using copy, safe to iterate without lock)
	for _, dl := range dlsCopy {
		dl.wg.Wait()
	}
	dls.wg.Wait()

	dls.mu.Lock()
	if !dls.closed {
		dls.ctx, dls.cancel = context.WithCancel(dls.parentCtx)
		// Reset error budget for the next session. Accumulated errors from
		// the current session must not poison resumed playback.
		dls.errorCount = 0
		dls.lastErr = nil
		dls.resetCircuitLocked()
	}
	dls.mu.Unlock()
}

// ensureKickerRunning restarts the kicker goroutine if it has stopped
func (dls *Downloaders) ensureKickerRunning() {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	// Check if kicker has exited (non-blocking check)
	select {
	case <-dls.kickerDone:
		// Kicker has exited, need to restart it
		dls.startKicker()
	default:
		// Kicker still running
	}
}

func (dls *Downloaders) currentKickerInterval() time.Duration {
	if dls.waiterCount.Load() > 0 {
		return activeWaiterKickerInterval
	}
	return kickerInterval
}

// startKicker starts a background safety-net goroutine that periodically checks
// waiters and handles idle timeout. The primary notification path is direct
// kickWaiters() calls from cacheWriter.Write(); this ticker is only a fallback.
func (dls *Downloaders) startKicker() {
	ctx := dls.ctx
	dls.kickerDone = make(chan struct{})
	dls.wg.Add(1)
	go func() {
		defer dls.wg.Done()
		defer close(dls.kickerDone)

		interval := dls.currentKickerInterval()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				// Adjust cadence if waiter presence changed since last tick.
				if next := dls.currentKickerInterval(); next != interval {
					interval = next
					ticker.Reset(next)
				}
				dls.kickWaiters()
				if dls.checkIdleTimeout() {
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
}

// downloader methods

// run is the main download loop
func (dl *downloader) run() (totalBytes int64, err error) {
	for {
		// Single lock to get all state
		start, targetEnd, chunkSize, fileSize, stopped := dl.getState()
		if stopped || start >= fileSize {
			return totalBytes, nil
		}

		// Nothing to do - wait for more work or timeout
		if start >= targetEnd {
			if !dl.waitForWork() {
				return totalBytes, nil
			}
			continue
		}

		// Calculate chunk boundaries
		// Always download at least chunkSize to reduce Stream calls
		chunkEnd := start + chunkSize
		if chunkEnd > fileSize {
			chunkEnd = fileSize
		}

		// Ensure we're downloading something meaningful
		if chunkEnd <= start {
			continue
		}

		// Download with retry
		written, chunkErr := dl.downloadChunkWithRetry(start, chunkEnd)
		totalBytes += written

		if chunkErr != nil {
			if errors.Is(chunkErr, io.EOF) {
				return totalBytes, nil
			}
			if dl.ctx.Err() != nil {
				return totalBytes, dl.ctx.Err()
			}
			return totalBytes, chunkErr
		}
	}
}

// getState returns current download state with single lock acquisition
func (dl *downloader) getState() (start, targetEnd, chunkSize, fileSize int64, stopped bool) {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	chunkSize = dl.currentChunkSize
	if chunkSize <= 0 {
		chunkSize = dl.baseChunkSize
		if chunkSize <= 0 {
			chunkSize = 4 * 1024 * 1024
		}
	}

	fileSize = dl.dls.item.info.Size
	targetEnd = dl.maxOffset
	if targetEnd > fileSize {
		targetEnd = fileSize
	}

	return dl.offset, targetEnd, chunkSize, fileSize, dl.stopped
}

// waitForWork blocks until new work arrives or timeout
func (dl *downloader) waitForWork() bool {
	if dl.idleTimer == nil {
		dl.idleTimer = time.NewTimer(maxDownloaderIdleTime)
	} else {
		if !dl.idleTimer.Stop() {
			select {
			case <-dl.idleTimer.C:
			default:
			}
		}
		dl.idleTimer.Reset(maxDownloaderIdleTime)
	}
	select {
	case <-dl.quit:
		return false
	case <-dl.kick:
		return true
	case <-dl.idleTimer.C:
		return false
	}
}

// downloadChunkWithRetry downloads a chunk with retry logic
func (dl *downloader) downloadChunkWithRetry(start, end int64) (int64, error) {
	attempts := dl.retryAttempts()
	chunkLen := end - start
	delay := config.DefaultRetryDelay
	maxDelay := config.DefaultRetryDelayMax

	for attempt := 1; attempt <= attempts; attempt++ {
		written, err := dl.streamChunk(start, end)

		if err == nil {
			dl.adjustChunkSize(chunkLen, written, true)
			return written, nil
		}

		dl.adjustChunkSize(chunkLen, written, false)

		// Non-retriable conditions
		if errors.Is(err, io.EOF) {
			return written, err
		}
		if dl.ctx.Err() != nil {
			return written, dl.ctx.Err()
		}
		if !customerror.IsRetriableError(err) {
			return written, err
		}

		// Last attempt failed
		if attempt == attempts {
			return written, err
		}

		// Log and backoff
		if !customerror.IsSilentError(err) {
			dl.dls.item.logger.Debug().
				Err(err).
				Int("attempt", attempt).
				Msg("stream error, retrying")
		}

		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-dl.ctx.Done():
			timer.Stop()
			return written, dl.ctx.Err()
		}

		// Exponential backoff
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}

	return 0, errors.New("exhausted retries")
}

// getRange returns the current download range
func (dl *downloader) getRange() (start, offset int64) {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.start, dl.offset
}

func (dl *downloader) streamChunk(start, end int64) (int64, error) {
	dl.mu.Lock()
	if dl.stopped {
		dl.mu.Unlock()
		return 0, io.EOF
	}
	dl.mu.Unlock()

	// Check if this range is already cached BEFORE calling the streaming function.
	// This avoids expensive network/reader operations for already-present data.
	requestedRange := ranges.Range{Pos: start, Size: end - start}
	missingRange := dl.dls.item.FindMissing(requestedRange)
	if missingRange.Size <= 0 {
		// All data already present - just advance offset and return
		dl.mu.Lock()
		dl.offset = end
		dl.mu.Unlock()
		return 0, nil
	}

	// Stream the missing portion
	// Advance offset to skip already-cached data before the missing range
	if missingRange.Pos > start {
		dl.mu.Lock()
		dl.offset = missingRange.Pos
		dl.mu.Unlock()
	}

	writer := &cacheWriter{
		dl:     dl,
		item:   dl.dls.item,
		offset: missingRange.Pos,
	}

	// Use an attempt-scoped context so a no-progress timeout can cancel only this
	// stream call while keeping the downloader alive for retries.
	attemptCtx, attemptCancel := context.WithCancel(dl.ctx)
	defer attemptCancel()

	var lastProgressNanos atomic.Int64
	lastProgressNanos.Store(time.Now().UnixNano())
	var timedOut atomic.Bool
	stopWatchdog := startNoProgressWatchdog(
		attemptCtx,
		noProgressTimeout,
		noProgressCheckInterval,
		&lastProgressNanos,
		attemptCancel,
		&timedOut,
	)
	defer stopWatchdog()

	writer.onProgress = func(_ int) {
		lastProgressNanos.Store(time.Now().UnixNano())
	}

	err := dl.dls.manager.Stream(
		attemptCtx,
		dl.dls.item.entry,
		dl.dls.item.filename,
		missingRange.Pos,
		missingRange.Pos+missingRange.Size-1, // manager.Stream uses inclusive end
		writer,
		nil,
		"DFS",
	)

	if err != nil {
		if dl.ctx.Err() != nil {
			return writer.written, dl.ctx.Err()
		}
		if timedOut.Load() {
			return writer.written, fmt.Errorf("stream stalled for %s: i/o timeout", noProgressTimeout)
		}
		return writer.written, err
	}

	// Ensure we made progress (either written data or skipped existing data)
	// If offset hasn't moved, we're in an infinite loop
	if writer.offset == missingRange.Pos {
		return writer.written, errors.New("stream produced no data")
	}

	// Final kick to notify waiters of any remaining data
	if writer.written > 0 {
		dl.dls.kickWaiters()
	}

	return writer.written, nil
}

// setMaxOffset extends the download range
func (dl *downloader) setMaxOffset(max int64) {
	dl.mu.Lock()
	if max > dl.maxOffset {
		dl.maxOffset = max
	}
	dl.mu.Unlock()

	// Kick to wake up if waiting
	select {
	case dl.kick <- struct{}{}:
	default:
	}
}

func (dl *downloader) adjustChunkSize(chunkLen, written int64, success bool) {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	// Only reset on actual failure, not on partial writes due to pre-cached data
	// If success is false, it means the stream itself failed
	if !success {
		dl.currentChunkSize = dl.baseChunkSize
		return
	}

	// If no data needed to be written (all cached), don't change chunk size
	if chunkLen <= 0 {
		return
	}

	// Double chunk size on successful download to quickly ramp up on good connections,
	// but cap at maxChunkSizeMultiplier × base to avoid oversized HTTP range requests on seeks.
	next := dl.currentChunkSize * 2
	if maxChunk := dl.baseChunkSize * maxChunkSizeMultiplier; next > maxChunk {
		next = maxChunk
	}
	if next <= 0 {
		next = dl.baseChunkSize
	}
	dl.currentChunkSize = next
}

// stop signals the downloader to stop and cancels its context so any
// in-flight manager.Stream call is interrupted promptly.
func (dl *downloader) stop() {
	dl.mu.Lock()
	if !dl.stopped {
		dl.stopped = true
		close(dl.quit)
	}
	dl.mu.Unlock()
	dl.cancel() // interrupt in-flight manager.Stream
}

// close marks the downloader as closed
func (dl *downloader) close(err error) {
	dl.mu.Lock()
	dl.closed = true
	dl.mu.Unlock()
}

// isClosed returns true if downloader is closed
func (dl *downloader) isClosed() bool {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.closed
}

func (dl *downloader) retryAttempts() int {
	if dl.dls.retries <= 0 {
		return 3
	}
	return dl.dls.retries
}

// cacheWriter writes to the sparse cache, tracking progress
type cacheWriter struct {
	dl      *downloader
	item    *CacheItem
	offset  int64
	written int64
	// onProgress is called whenever bytes are consumed from the stream.
	onProgress func(int)
}

func (w *cacheWriter) Write(p []byte) (int, error) {
	n, skipped, err := w.item.WriteAtNoOverwrite(p, w.offset)
	if err != nil {
		return n, err
	}
	if n > 0 && w.onProgress != nil {
		w.onProgress(n)
	}

	w.dl.mu.Lock()
	// Track skipped bytes
	if skipped == n {
		w.dl.skipped += int64(skipped)
	} else {
		w.dl.skipped = 0
	}
	w.dl.offset += int64(n)

	// Stop if skipping too much (seeking happened elsewhere)
	if w.dl.skipped > maxSkipBytes {
		w.dl.stopped = true
		w.dl.mu.Unlock()
		return n, io.EOF // Signal to stop streaming
	}
	w.dl.mu.Unlock()

	w.offset += int64(n)
	actuallyWritten := int64(n - skipped)
	w.written += actuallyWritten

	// Track total bytes downloaded
	if actuallyWritten > 0 {
		w.dl.dls.item.cache.AddDownloadedBytes(actuallyWritten)
		if w.dl.dls.waiterCount.Load() > 0 {
			w.dl.dls.kickWaiters()
		}
	}

	return n, nil
}
