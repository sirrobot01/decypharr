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
	"github.com/sirrobot01/decypharr/internal/nntp"
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
	// probeChunkSize is the (small) initial chunk a priority downloader uses.
	// Latency-sensitive probe reads (ffprobe seeking to the moov atom near EOF)
	// must return a few bytes fast and release the NNTP connection quickly,
	// instead of being bundled behind a multi-MB read-ahead chunk.
	probeChunkSize = 1 * 1024 * 1024 // 1MB
	// kickerInterval is how often the safety-net ticker checks waiters and idle timeout
	kickerInterval = 5 * time.Second
	// activeWaiterKickerInterval is the faster fallback cadence while readers are blocked.
	activeWaiterKickerInterval = 1 * time.Second
	// idleTimeout is how long before stopping all downloaders due to inactivity
	idleTimeout = 30 * time.Second
	// circuitCooldownDuration is how long to block requests after max errors
	// reached. Kept short: a probe-heavy workload (Sonarr/Radarr library
	// scans) issues many short-lived reads, so a long lockout turns a brief
	// provider hiccup into minutes of "unable to detect if file is a sample".
	circuitCooldownDuration = 2 * time.Minute
	// noProgressTimeout is the max time a stream attempt may run without any
	// bytes written. Keep this above the NNTP per-segment idle timeout so a
	// slow/stalled usenet provider can be classified and retried before DFS
	// cancels the attempt.
	noProgressTimeout = 90 * time.Second
	// noProgressCheckInterval is how often stall detection checks for forward progress.
	noProgressCheckInterval = 1 * time.Second
	// maxChunkSizeMultiplier caps adaptive chunk growth at this multiple of baseChunkSize.
	// Without a cap, binary doubling eventually produces chunk sizes in the GB range,
	// causing oversized HTTP range requests that are wasteful on seeks.
	maxChunkSizeMultiplier = 16
	// minSuccessfulChunksBeforeGrowth keeps short-lived scans at the base chunk size
	// until the stream proves it is sustained sequential reading.
	minSuccessfulChunksBeforeGrowth = 2
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

	// Adaptive state shared across contiguous downloaders so sustained playback
	// does not have to re-ramp when a nearby downloader is recreated.
	adaptiveEnd              int64
	adaptiveChunkSize        int64
	adaptiveSuccessfulChunks int

	// Read-pattern state is used to keep tiny probe/random reads from expanding
	// into full read-ahead fetches. Once a caller proves sustained forward
	// reading, small reads are promoted back to the bulk path.
	lastReadEnd         int64
	sequentialReadBytes int64
	nextDownloaderID    int64 // protected by dls.mu; used only for trace correlation

	mu         sync.Mutex
	dls        []*downloader
	waiters    []waiter
	errorCount int
	lastErr    error
	closed     bool
	// stopping is true while StopAll() is tearing down the current session.
	// It blocks the idle-restart path from spinning up a fresh kicker before
	// the previous one has fully exited and the new ctx is installed.
	stopping bool
	// idle is true when all downloaders have stopped. Guarded by mu so that
	// the restart decision in Download() and the teardown in StopAll() are
	// serialized with kicker lifecycle changes.
	idle bool

	// streamID is the active stream registration ID for tracking
	streamID string

	// Atomic waiter count for fast-path check (avoids locking dls.mu in Write() when no waiters)
	waiterCount atomic.Int32

	// Idle timeout tracking
	lastActivity atomic.Int64  // Unix nano timestamp of last download activity
	kickerDone   chan struct{} // Signals kicker goroutine has exited; a fresh channel per session

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
	// priority marks a latency-sensitive read (e.g. ffprobe's near-EOF moov
	// seek). Priority waiters spawn a dedicated small-chunk downloader with no
	// read-ahead extension so they don't queue behind bulk prefetch.
	priority bool
}

// downloader represents a single download goroutine
type downloader struct {
	id     int64
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
	successfulChunks int
	// priority downloaders keep a small fixed chunk size (no adaptive growth)
	// so each Stream call is short and yields its connection quickly.
	priority bool

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
	return dls.DownloadWithPriority(ctx, r, false)
}

// DownloadWithPriority is Download with an optional priority hint. Priority
// reads (small/random/near-EOF, e.g. ffprobe) get a dedicated small-chunk
// downloader with no read-ahead extension so they are not starved behind bulk
// sequential prefetch under high connection load.
func (dls *Downloaders) DownloadWithPriority(ctx context.Context, r ranges.Range, priority bool) error {
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

	dls.mu.Lock()
	if dls.closed {
		dls.mu.Unlock()
		return errors.New("downloaders closed")
	}

	// Lazy restart: if we went idle, restart the kicker goroutine. Skipped
	// while stopping so we don't race a new kicker against StopAll()'s wait
	// on the old kickerDone channel.
	if dls.idle && !dls.stopping {
		dls.idle = false
		dls.ensureKickerRunningLocked()
	}

	// Fast path: already have it
	if dls.item.HasRange(r) {
		if err := dls.ensureDownloaderLocked(r, priority); err != nil {
			dls.mu.Unlock()
			return err
		}
		dls.mu.Unlock()
		return nil
	}
	// Create waiter channel
	errChan := make(chan error, 1)
	dls.waiters = append(dls.waiters, waiter{r: r, errChan: errChan, priority: priority})
	dls.waiterCount.Add(1)

	// Ensure downloader running
	if err := dls.ensureDownloaderLocked(r, priority); err != nil {
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

const (
	// downloadRetryAttempts bounds how many times a read re-attempts a
	// transient download failure before surfacing EIO to FUSE. ffprobe treats
	// one EIO as fatal ("unable to detect if file is a sample"), so a single
	// connection-starvation blip under load must not fail it — but we must not
	// block it long either, since ffprobe has its own I/O timeout.
	downloadRetryAttempts = 3
	downloadRetryBackoff  = 150 * time.Millisecond

	// probeTailZone: a read whose end lands in the final zone of the file is
	// treated as a latency-sensitive probe (ffprobe seeking to the MP4 moov
	// atom / MKV cues near EOF). For sequential playback this only matters at
	// the very end, where read-ahead is clipped anyway — negligible cost.
	probeTailZone = 64 * 1024 * 1024

	// probeReadMaxSize is the largest individual read that may be treated as a
	// probe/random access. Media scanners often issue many 4 KiB-128 KiB reads;
	// expanding each one into tens of MiB of read-ahead wastes bandwidth and
	// NNTP/debrid connections.
	probeReadMaxSize = 1 * 1024 * 1024

	// sequentialReadAheadPromotionBytes is how much contiguous forward reading a
	// caller must perform before small reads are allowed to use the bulk
	// read-ahead path. This keeps ffprobe/header scans cheap while still letting
	// real playback ramp quickly.
	sequentialReadAheadPromotionBytes = 2 * 1024 * 1024

	// readPatternGapTolerance allows small gaps/overlap in OS/player read
	// patterns without resetting sequential detection.
	readPatternGapTolerance = 512 * 1024
)

// isProbeRead reports whether a read is statelessly known to look like a
// media-probe access pattern (near-EOF). Stateful small/random read
// classification is handled by shouldPrioritizeRead.
func isProbeRead(off, length, fileSize int64) bool {
	if fileSize <= 0 || length <= 0 {
		return false
	}
	return off+length >= fileSize-probeTailZone
}

// shouldPrioritizeRead returns true when a read should avoid bulk read-ahead
// and use a small, dedicated downloader instead. This protects probe/random
// reads from causing large upstream range fetches, but promotes sustained
// forward reading back to the normal bulk path once enough contiguous bytes
// have been requested.
func (dls *Downloaders) shouldPrioritizeRead(r ranges.Range) bool {
	r.Clip(dls.item.info.Size)
	if r.IsEmpty() {
		return false
	}

	// Tail probes stay priority regardless of read history.
	if isProbeRead(r.Pos, r.Size, dls.item.info.Size) {
		dls.recordReadPattern(r)
		return true
	}

	sequentialBytes := dls.recordReadPattern(r)
	if r.Size > probeReadMaxSize {
		return false
	}
	return sequentialBytes < sequentialReadAheadPromotionBytes
}

// recordReadPattern records whether r continues the current forward-read
// stream and returns the number of contiguous forward bytes seen so far.
func (dls *Downloaders) recordReadPattern(r ranges.Range) int64 {
	dls.mu.Lock()
	defer dls.mu.Unlock()
	return dls.recordReadPatternLocked(r)
}

func (dls *Downloaders) recordReadPatternLocked(r ranges.Range) int64 {
	end := r.End()
	if end < r.Pos {
		end = r.Pos
	}

	// Treat overlapping reads and small forward gaps as one sequential stream.
	sequential := r.Pos <= dls.lastReadEnd+readPatternGapTolerance && end+readPatternGapTolerance >= dls.lastReadEnd
	if !sequential {
		dls.lastReadEnd = end
		dls.sequentialReadBytes = r.Size
		return dls.sequentialReadBytes
	}

	if advance := end - dls.lastReadEnd; advance > 0 {
		dls.sequentialReadBytes += advance
		dls.lastReadEnd = end
	}
	return dls.sequentialReadBytes
}

// DownloadWithRetry blocks until r is available, re-attempting transient
// failures a few times with short backoff so a momentary connection shortage
// under high load does not surface as a hard read error to ffprobe. It fails
// fast (no retry) on cancellation, an open circuit breaker, or a non-transient
// error — retrying those would only spin.
func (dls *Downloaders) DownloadWithRetry(ctx context.Context, r ranges.Range, priority bool) error {
	var err error
	for attempt := 0; attempt < downloadRetryAttempts; attempt++ {
		if err = dls.DownloadWithPriority(ctx, r, priority); err == nil {
			return nil
		}
		if ctx.Err() != nil || dls.isCircuitOpen() || !customerror.IsRetriableError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(downloadRetryBackoff * time.Duration(attempt+1)):
		}
	}
	return err
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
func (dls *Downloaders) ensureDownloaderLocked(r ranges.Range, priority bool) error {
	// Priority reads fetch only the missing bytes they actually need (no
	// read-ahead extension) so they complete and release the connection fast.
	// Bulk reads extend by read-ahead then clip to actual missing bytes.
	if priority {
		r = dls.item.FindMissing(r)
	} else {
		r = dls.extendAndFindMissingRangeLocked(r)
	}

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
		start, offset, maxOffset := dl.getRange()

		if dls.item.cache != nil {
			dls.item.cache.logger.Trace().
				Str("event", "dfs_downloader_reuse").
				Str("key", dls.item.key).
				Int64("downloader_id", dl.id).
				Int64("requested_pos", r.Pos).
				Int64("target_end", targetEnd).
				Int64("start", start).
				Int64("offset", offset).
				Int64("max_offset_before", maxOffset).
				Msg("dfs downloader reused")
		}

		// Extend existing downloader
		dl.setMaxOffset(targetEnd)
		return nil
	}

	// Start new downloader
	return dls.newDownloaderLocked(r, targetEnd, priority)
}

// extendAndFindMissingRangeLocked expands a request by read-ahead and returns
// only the missing tail that must be downloaded.
func (dls *Downloaders) extendAndFindMissingRangeLocked(r ranges.Range) ranges.Range {
	requested := r

	bufferWindow := dls.readAheadSize
	if bufferWindow <= 0 {
		bufferWindow = dls.chunkSize * 4
	}

	r.Size += bufferWindow
	if r.Pos+r.Size > dls.item.info.Size {
		r.Size = dls.item.info.Size - r.Pos
	}

	missing := dls.item.FindMissing(r)

	// Trace only when this request causes actual upstream work. This lets us
	// measure read amplification without logging every cache hit.
	if !missing.IsEmpty() && dls.item.cache != nil {
		dls.item.cache.logger.Trace().
			Str("event", "dfs_read_ahead").
			Str("key", dls.item.key).
			Int64("requested_offset", requested.Pos).
			Int64("requested_size", requested.Size).
			Int64("expanded_offset", r.Pos).
			Int64("expanded_size", r.Size).
			Int64("missing_offset", missing.Pos).
			Int64("missing_size", missing.Size).
			Msg("dfs read-ahead expansion")
	}

	return missing
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
		start, offset, maxOffset := dl.getRange()

		matchEnd := offset + window
		if maxOffset > matchEnd {
			matchEnd = maxOffset
		}

		if pos >= start && pos < matchEnd {
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
	_, offset, _ := dl.getRange()
	dl.setMaxOffset(offset) // kick without extending
}

// updateAdaptiveState advances shared adaptive state only across exact contiguous progress.
// Non-contiguous reads are probes, seeks, or separate streams and must not inherit growth.
func (dls *Downloaders) updateAdaptiveState(start, end, currentChunk int64, successfulChunks int) {
	dls.mu.Lock()
	defer dls.mu.Unlock()

	if dls.adaptiveEnd != 0 && start != dls.adaptiveEnd {
		return
	}

	dls.adaptiveEnd = end
	dls.adaptiveChunkSize = currentChunk
	dls.adaptiveSuccessfulChunks = successfulChunks
}

// adaptiveStateForNewDownloaderLocked returns the starting adaptive state for a new
// downloader. Caller must hold dls.mu.
func (dls *Downloaders) adaptiveStateForNewDownloaderLocked(pos, size, baseChunk int64) (int64, int) {
	currentChunk := baseChunk
	successfulChunks := 0

	// Reuse adaptive state only when this downloader is an exact contiguous
	// continuation of the last sustained sequential downloader.
	if dls.adaptiveChunkSize > 0 && pos == dls.adaptiveEnd {
		currentChunk = dls.adaptiveChunkSize
		successfulChunks = dls.adaptiveSuccessfulChunks
	} else if size >= baseChunk {
		// A non-contiguous full-size request looks like a new sequential stream or seek.
		// Reset shared adaptive state so the new stream must prove itself again.
		dls.adaptiveEnd = 0
		dls.adaptiveChunkSize = 0
		dls.adaptiveSuccessfulChunks = 0
	}

	return currentChunk, successfulChunks
}

// newDownloaderLocked creates and starts a new downloader
func (dls *Downloaders) newDownloaderLocked(r ranges.Range, targetEnd int64, priority bool) error {
	baseChunk := dls.chunkSize
	if baseChunk <= 0 {
		baseChunk = 4 * 1024 * 1024
	}
	// Priority downloaders use a small fixed chunk so the latency-sensitive
	// bytes return quickly and the NNTP connection is freed for other reads.
	if priority && baseChunk > probeChunkSize {
		baseChunk = probeChunkSize
	}

	currentChunk := baseChunk
	successfulChunks := 0
	if !priority {
		currentChunk, successfulChunks = dls.adaptiveStateForNewDownloaderLocked(r.Pos, r.Size, baseChunk)
	}

	// Each downloader gets its own context derived from the Downloaders context.
	// stop() cancels dlCtx, which interrupts any in-flight manager.Stream call
	// without having to cancel the shared dls.ctx.
	dlCtx, dlCancel := context.WithCancel(dls.ctx)

	dls.nextDownloaderID++
	downloaderID := dls.nextDownloaderID

	dl := &downloader{
		id:               downloaderID,
		dls:              dls,
		quit:             make(chan struct{}),
		kick:             make(chan struct{}, 1),
		ctx:              dlCtx,
		cancel:           dlCancel,
		start:            r.Pos,
		offset:           r.Pos,
		maxOffset:        targetEnd,
		baseChunkSize:    baseChunk,
		currentChunkSize: currentChunk,
		successfulChunks: successfulChunks,
		priority:         priority,
	}

	dls.dls = append(dls.dls, dl)
	if dls.item.cache != nil {
		dls.item.cache.logger.Trace().
			Str("event", "dfs_downloader_new").
			Str("key", dls.item.key).
			Int64("downloader_id", dl.id).
			Int64("start", dl.start).
			Int64("offset", dl.offset).
			Int64("max_offset", dl.maxOffset).
			Int64("base_chunk_size", dl.baseChunkSize).
			Int64("current_chunk_size", dl.currentChunkSize).
			Int("successful_chunks", dl.successfulChunks).
			Bool("priority", dl.priority).
			Msg("dfs downloader created")
	}

	// Track active download count
	dls.item.cache.activeDownloads.Add(1)

	dl.wg.Add(1)
	go func() {
		defer dl.wg.Done()
		defer dls.item.cache.activeDownloads.Add(-1)
		defer dlCancel() // always release the per-downloader context
		n, err := dl.run()
		dl.close(err)

		if dls.item.cache != nil {
			start, offset, maxOffset := dl.getRange()

			event := dls.item.cache.logger.Trace().
				Str("event", "dfs_downloader_closed").
				Str("key", dls.item.key).
				Int64("downloader_id", dl.id).
				Int64("start", start).
				Int64("offset", offset).
				Int64("max_offset", maxOffset).
				Int64("total_bytes", n).
				Bool("success", err == nil)

			if err != nil {
				event = event.Err(err)
			}

			event.Msg("dfs downloader closed")
		}
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
		// Only a genuinely permanent provider failure (article missing, auth,
		// payment/permission) fast-trips the breaker — retrying those 10× is
		// pointless. Transient/ambiguous errors (timeout, stall, "stream
		// produced no data", "exhausted retries") only increment, so the
		// breaker requires SUSTAINED failure. This is what stops one bad
		// moment under load from locking a file out of every ffprobe.
		if nntp.IsArticleNotFoundError(err) || customerror.IsPermanentError(err) {
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
		var missing ranges.Range
		if w.priority {
			missing = dls.item.FindMissing(w.r)
		} else {
			missing = dls.extendAndFindMissingRangeLocked(w.r)
		}
		if missing.IsEmpty() {
			continue
		}
		if dls.findDownloaderForPosLocked(missing.Pos) != nil {
			continue
		}
		_ = dls.ensureDownloaderLocked(w.r, w.priority)
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
	oldKickerDone := dls.kickerDone
	dls.mu.Unlock()

	// Cancel first so any blocked stream operation can exit promptly.
	dls.cancel()

	// Wait for downloaders to finish
	for _, dl := range dlsCopy {
		dl.wg.Wait()
	}

	// Wait for the kicker goroutine via its per-session sentinel.
	if oldKickerDone != nil {
		<-oldKickerDone
	}

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
	dls.idle = true

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
	if dls.closed || dls.stopping {
		// Another StopAll() is already tearing this session down, or we are
		// already closed. Either way there is nothing left for us to do.
		dls.mu.Unlock()
		return
	}
	dls.stopping = true
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
	dls.idle = true
	oldCancel := dls.cancel
	oldKickerDone := dls.kickerDone
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
	// Wait for the kicker goroutine to exit using its per-session sentinel.
	// Each startKicker() allocates a fresh channel, so this never observes a
	// channel that a future kicker has reused.
	if oldKickerDone != nil {
		<-oldKickerDone
	}

	dls.mu.Lock()
	if !dls.closed {
		dls.ctx, dls.cancel = context.WithCancel(dls.parentCtx)
		// Reset error budget for the next session. Accumulated errors from
		// the current session must not poison resumed playback.
		dls.errorCount = 0
		dls.lastErr = nil
		dls.resetCircuitLocked()
	}
	dls.stopping = false
	dls.mu.Unlock()
}

// ensureKickerRunningLocked restarts the kicker goroutine if it has stopped.
// Caller must hold dls.mu. Refuses to start a new kicker while the session
// is closed or being torn down — that would race against StopAll()/Close()
// waiting on the previous kicker's exit sentinel.
func (dls *Downloaders) ensureKickerRunningLocked() {
	if dls.closed || dls.stopping {
		return
	}
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
	done := make(chan struct{})
	dls.kickerDone = done
	go func() {
		defer close(done)

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
			currentChunk, successfulChunks := dl.adjustChunkSize(chunkLen, written, true)
			if !dl.priority {
				dl.dls.updateAdaptiveState(start, end, currentChunk, successfulChunks)
			}
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

// getRange returns the downloader start, current written offset, and assigned end.
func (dl *downloader) getRange() (start, offset, maxOffset int64) {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.start, dl.offset, dl.maxOffset
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

	streamStart := time.Now()
	cache := dl.dls.item.cache

	downloaderStart, downloaderOffset, downloaderMaxOffset := dl.getRange()
	if cache != nil {
		cache.logger.Trace().
			Str("event", "dfs_upstream_fetch_start").
			Str("key", dl.dls.item.key).
			Int64("downloader_id", dl.id).
			Int64("downloader_start", downloaderStart).
			Int64("downloader_offset", downloaderOffset).
			Int64("downloader_max_offset", downloaderMaxOffset).
			Int64("requested_start", start).
			Int64("requested_end", end).
			Int64("missing_offset", missingRange.Pos).
			Int64("missing_size", missingRange.Size).
			Msg("dfs upstream fetch start")
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

	if cache != nil {
		doneStart, doneOffset, doneMaxOffset := dl.getRange()

		event := cache.logger.Trace().
			Str("event", "dfs_upstream_fetch_done").
			Str("key", dl.dls.item.key).
			Int64("downloader_id", dl.id).
			Int64("downloader_start", doneStart).
			Int64("downloader_offset", doneOffset).
			Int64("downloader_max_offset", doneMaxOffset).
			Int64("requested_start", start).
			Int64("requested_end", end).
			Int64("missing_offset", missingRange.Pos).
			Int64("missing_size", missingRange.Size).
			Int64("written", writer.written).
			Dur("duration", time.Since(streamStart)).
			Bool("success", err == nil)

		if err != nil {
			event = event.Err(err)
		}

		event.Msg("dfs upstream fetch complete")
	}

	if err != nil {
		if dl.ctx.Err() != nil {
			return writer.written, dl.ctx.Err()
		}
		if timedOut.Load() {
			return writer.written, fmt.Errorf("stream stalled for %s: i/o timeout", noProgressTimeout)
		}
		return writer.written, err
	}

	// Ensure we made progress (either written data or skipped existing data).
	// If offset hasn't moved, re-check whether a concurrent downloader filled
	// this range while we were streaming. Under high load (ffprobe + import
	// scan + prefetch all touching the same file) overlapping downloaders are
	// common; the range being present now is a benign race, NOT a failure —
	// treating it as one used to trip the circuit breaker and lock the file
	// out for minutes.
	if writer.offset == missingRange.Pos {
		if dl.dls.item.FindMissing(requestedRange).Size <= 0 {
			dl.mu.Lock()
			dl.offset = end
			dl.mu.Unlock()
			return writer.written, nil
		}
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

func (dl *downloader) adjustChunkSize(chunkLen, written int64, success bool) (int64, int) {
	dl.mu.Lock()
	defer dl.mu.Unlock()

	if dl.priority {
		dl.currentChunkSize = dl.baseChunkSize
		return dl.currentChunkSize, dl.successfulChunks
	}

	// Only reset on actual failure, not on partial writes due to pre-cached data.
	// If success is false, it means the stream itself failed or was interrupted.
	if !success {
		dl.currentChunkSize = dl.baseChunkSize
		dl.successfulChunks = 0
		return dl.currentChunkSize, dl.successfulChunks
	}

	// If no data needed to be written (all cached), don't change chunk size.
	if written <= 0 {
		return dl.currentChunkSize, dl.successfulChunks
	}

	dl.successfulChunks++

	// Keep short-lived scans at the base chunk size until the stream has
	// completed enough successful chunks to look like sustained reading.
	if dl.successfulChunks < minSuccessfulChunksBeforeGrowth {
		dl.currentChunkSize = dl.baseChunkSize
		return dl.currentChunkSize, dl.successfulChunks
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
	return dl.currentChunkSize, dl.successfulChunks
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
