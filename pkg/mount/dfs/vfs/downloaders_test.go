package vfs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

const (
	testKiB = int64(1024)
	testMiB = 1024 * testKiB
)

func setReleaseStopGracePeriodForTest(t *testing.T, d time.Duration) {
	t.Helper()
	old := releaseStopGracePeriodNanos.Swap(int64(d))
	t.Cleanup(func() {
		releaseStopGracePeriodNanos.Store(old)
	})
}

func TestCurrentKickerInterval(t *testing.T) {
	dls := &Downloaders{}

	if got := dls.currentKickerInterval(); got != kickerInterval {
		t.Fatalf("unexpected interval without waiters: got %s, want %s", got, kickerInterval)
	}

	dls.waiterCount.Store(1)
	if got := dls.currentKickerInterval(); got != activeWaiterKickerInterval {
		t.Fatalf("unexpected interval with waiters: got %s, want %s", got, activeWaiterKickerInterval)
	}
}

func TestShouldPrioritizeRead_TinyInitialReadsStayPriority(t *testing.T) {
	dls := &Downloaders{
		item: &CacheItem{info: ItemInfo{Size: 256 * testMiB}},
	}

	reads := []ranges.Range{
		{Pos: 0, Size: 128 * testKiB},
		{Pos: 128 * testKiB, Size: 128 * testKiB},
	}
	for _, r := range reads {
		if !dls.shouldPrioritizeRead(r) {
			t.Fatalf("expected small unproven read at %d to stay priority", r.Pos)
		}
	}
}

func TestShouldPrioritizeRead_PromotesSustainedSequentialSmallReads(t *testing.T) {
	dls := &Downloaders{
		item: &CacheItem{info: ItemInfo{Size: 256 * testMiB}},
	}

	var promoted bool
	for off := int64(0); off < 4*testMiB; off += 128 * testKiB {
		priority := dls.shouldPrioritizeRead(ranges.Range{Pos: off, Size: 128 * testKiB})
		if off < sequentialReadAheadPromotionBytes-128*testKiB && !priority {
			t.Fatalf("read at %d promoted before threshold", off)
		}
		if !priority {
			promoted = true
			break
		}
	}

	if !promoted {
		t.Fatal("expected sustained sequential small reads to promote to bulk path")
	}
}

func TestShouldPrioritizeRead_RandomSmallSeekResetsToPriority(t *testing.T) {
	dls := &Downloaders{
		item: &CacheItem{info: ItemInfo{Size: 512 * testMiB}},
	}

	for off := int64(0); off < 4*testMiB; off += 256 * testKiB {
		_ = dls.shouldPrioritizeRead(ranges.Range{Pos: off, Size: 256 * testKiB})
	}

	if !dls.shouldPrioritizeRead(ranges.Range{Pos: 128 * testMiB, Size: 128 * testKiB}) {
		t.Fatal("expected random small seek to reset and stay priority")
	}
}

func TestShouldPrioritizeRead_TailProbeAlwaysPriority(t *testing.T) {
	dls := &Downloaders{
		item: &CacheItem{info: ItemInfo{Size: 512 * testMiB}},
	}

	// Seed enough sequential reading to promote normal small reads.
	for off := int64(0); off < 4*testMiB; off += 256 * testKiB {
		_ = dls.shouldPrioritizeRead(ranges.Range{Pos: off, Size: 256 * testKiB})
	}

	tail := ranges.Range{Pos: dls.item.info.Size - 4*testKiB, Size: 4 * testKiB}
	if !dls.shouldPrioritizeRead(tail) {
		t.Fatal("expected near-EOF tail probe to remain priority")
	}
}

func TestAdaptiveStateForNewDownloader_InheritsExactContiguousState(t *testing.T) {
	const (
		baseChunk     = 16 * testMiB
		adaptiveEnd   = 64 * testMiB
		adaptiveChunk = 64 * testMiB
	)

	dls := &Downloaders{
		adaptiveEnd:              adaptiveEnd,
		adaptiveChunkSize:        adaptiveChunk,
		adaptiveSuccessfulChunks: 3,
	}

	dls.mu.Lock()
	currentChunk, successfulChunks := dls.adaptiveStateForNewDownloaderLocked(
		adaptiveEnd,
		baseChunk,
		baseChunk,
	)
	dls.mu.Unlock()

	if currentChunk != adaptiveChunk {
		t.Fatalf("unexpected inherited chunk size: got %d, want %d", currentChunk, adaptiveChunk)
	}
	if successfulChunks != 3 {
		t.Fatalf("unexpected inherited successful chunk count: got %d, want 3", successfulChunks)
	}
}

func TestAdaptiveStateForNewDownloader_ResetsOnNonContiguousFullSizeRequest(t *testing.T) {
	const (
		baseChunk     = 16 * testMiB
		adaptiveEnd   = 64 * testMiB
		adaptiveChunk = 64 * testMiB
	)

	dls := &Downloaders{
		adaptiveEnd:              adaptiveEnd,
		adaptiveChunkSize:        adaptiveChunk,
		adaptiveSuccessfulChunks: 3,
	}

	dls.mu.Lock()
	currentChunk, successfulChunks := dls.adaptiveStateForNewDownloaderLocked(
		128*testMiB,
		baseChunk,
		baseChunk,
	)
	dls.mu.Unlock()

	if currentChunk != baseChunk {
		t.Fatalf("unexpected chunk size after reset: got %d, want %d", currentChunk, baseChunk)
	}
	if successfulChunks != 0 {
		t.Fatalf("unexpected successful chunk count after reset: got %d, want 0", successfulChunks)
	}
	if dls.adaptiveEnd != 0 {
		t.Fatalf("expected adaptive end to reset, got %d", dls.adaptiveEnd)
	}
	if dls.adaptiveChunkSize != 0 {
		t.Fatalf("expected adaptive chunk size to reset, got %d", dls.adaptiveChunkSize)
	}
	if dls.adaptiveSuccessfulChunks != 0 {
		t.Fatalf("expected adaptive successful chunk count to reset, got %d", dls.adaptiveSuccessfulChunks)
	}
}

func TestAdaptiveStateForNewDownloader_KeepsStateForNonContiguousSmallProbe(t *testing.T) {
	const (
		baseChunk     = 16 * testMiB
		adaptiveEnd   = 64 * testMiB
		adaptiveChunk = 64 * testMiB
	)

	dls := &Downloaders{
		adaptiveEnd:              adaptiveEnd,
		adaptiveChunkSize:        adaptiveChunk,
		adaptiveSuccessfulChunks: 3,
	}

	dls.mu.Lock()
	currentChunk, successfulChunks := dls.adaptiveStateForNewDownloaderLocked(
		128*testMiB,
		4*testKiB,
		baseChunk,
	)
	dls.mu.Unlock()

	if currentChunk != baseChunk {
		t.Fatalf("unexpected chunk size for small probe: got %d, want %d", currentChunk, baseChunk)
	}
	if successfulChunks != 0 {
		t.Fatalf("unexpected successful chunk count for small probe: got %d, want 0", successfulChunks)
	}
	if dls.adaptiveEnd != adaptiveEnd {
		t.Fatalf("expected adaptive end to be preserved, got %d", dls.adaptiveEnd)
	}
	if dls.adaptiveChunkSize != adaptiveChunk {
		t.Fatalf("expected adaptive chunk size to be preserved, got %d", dls.adaptiveChunkSize)
	}
	if dls.adaptiveSuccessfulChunks != 3 {
		t.Fatalf("expected adaptive successful chunk count to be preserved, got %d", dls.adaptiveSuccessfulChunks)
	}
}

func getMaxOffset(dl *downloader) int64 {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.maxOffset
}

func TestFindDownloaderForPosLocked_ReusesAssignedRangeAheadOfCurrentOffset(t *testing.T) {
	const chunkSize = 16 * testMiB

	dl := &downloader{
		start:     0,
		offset:    2 * testMiB,
		maxOffset: 32 * testMiB,
	}

	dls := &Downloaders{
		chunkSize: chunkSize,
		dls:       []*downloader{dl},
	}

	if got := dls.findDownloaderForPosLocked(20 * testMiB); got != dl {
		t.Fatal("expected downloader to be reused for a position already inside its assigned range")
	}
}

func TestFindDownloaderForPosLocked_ReusesNearbyDownloaderOutsideAssignedRange(t *testing.T) {
	const chunkSize = 8 * testMiB

	dl := &downloader{
		start:     0,
		offset:    10 * testMiB,
		maxOffset: 12 * testMiB,
	}

	dls := &Downloaders{
		chunkSize: chunkSize,
		dls:       []*downloader{dl},
	}

	if got := dls.findDownloaderForPosLocked(13 * testMiB); got != dl {
		t.Fatal("expected nearby downloader to be reused within the match window")
	}
}

func TestEnsureDownloaderLocked_ExtendsMissByReadAhead(t *testing.T) {
	const (
		reqPos    = 10 * testMiB
		reqSize   = 128 * testKiB
		readAhead = 16 * testMiB
	)

	item := &CacheItem{
		info: ItemInfo{
			Size: 64 * testMiB,
		},
	}

	dl := &downloader{
		start:     reqPos,
		offset:    reqPos,
		maxOffset: reqPos + reqSize,
	}

	dls := &Downloaders{
		item:          item,
		chunkSize:     4 * testMiB,
		readAheadSize: readAhead,
		dls:           []*downloader{dl},
	}

	req := ranges.Range{Pos: reqPos, Size: reqSize}
	if err := dls.ensureDownloaderLocked(req, false); err != nil {
		t.Fatalf("ensureDownloaderLocked returned error: %v", err)
	}

	want := req.End() + readAhead
	got := getMaxOffset(dl)
	if got != want {
		t.Fatalf("unexpected maxOffset: got %d, want %d", got, want)
	}
}

func TestEnsureDownloaderLocked_CachedRequestPrefetchesGap(t *testing.T) {
	const (
		reqPos    = 0
		reqSize   = 128 * testKiB
		readAhead = 16 * testMiB
	)

	item := &CacheItem{
		info: ItemInfo{
			Size: 64 * testMiB,
			Rs: ranges.Ranges{
				{Pos: 0, Size: 1 * testMiB}, // request is cached, look-ahead has a gap after 1 MiB
			},
		},
	}

	dl := &downloader{
		start:     512 * testKiB,
		offset:    2 * testMiB,
		maxOffset: 2 * testMiB,
	}

	dls := &Downloaders{
		item:          item,
		chunkSize:     4 * testMiB,
		readAheadSize: readAhead,
		dls:           []*downloader{dl},
	}

	req := ranges.Range{Pos: reqPos, Size: reqSize}
	if err := dls.ensureDownloaderLocked(req, false); err != nil {
		t.Fatalf("ensureDownloaderLocked returned error: %v", err)
	}

	want := req.End() + readAhead
	got := getMaxOffset(dl)
	if got != want {
		t.Fatalf("unexpected maxOffset: got %d, want %d", got, want)
	}
}

func TestEnsureDownloaderLocked_CachedWindowFullDoesNotExtend(t *testing.T) {
	const (
		reqPos    = 0
		reqSize   = 128 * testKiB
		readAhead = 16 * testMiB
	)

	item := &CacheItem{
		info: ItemInfo{
			Size: 64 * testMiB,
			Rs: ranges.Ranges{
				{Pos: 0, Size: 32 * testMiB}, // request + look-ahead fully cached
			},
		},
	}

	dl := &downloader{
		start:     0,
		offset:    2 * testMiB,
		maxOffset: 2 * testMiB,
	}

	dls := &Downloaders{
		item:          item,
		chunkSize:     4 * testMiB,
		readAheadSize: readAhead,
		dls:           []*downloader{dl},
	}

	req := ranges.Range{Pos: reqPos, Size: reqSize}
	if err := dls.ensureDownloaderLocked(req, false); err != nil {
		t.Fatalf("ensureDownloaderLocked returned error: %v", err)
	}

	want := int64(2 * testMiB)
	got := getMaxOffset(dl)
	if got != want {
		t.Fatalf("unexpected maxOffset when window is full: got %d, want %d", got, want)
	}
}

func TestStopAllClearsWaiters(t *testing.T) {
	parentCtx := context.Background()
	ctx, cancel := context.WithCancel(parentCtx)

	dls := &Downloaders{
		parentCtx: parentCtx,
		ctx:       ctx,
		cancel:    cancel,
	}

	errCh := make(chan error, 1)
	dls.waiters = append(dls.waiters, waiter{
		r:       ranges.Range{Pos: 0, Size: 1},
		errChan: errCh,
	})
	dls.waiterCount.Store(1)

	dls.StopAll()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected waiter to receive stop error")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("waiter was not unblocked by StopAll")
	}

	if got := dls.waiterCount.Load(); got != 0 {
		t.Fatalf("unexpected waiter count after StopAll: got %d, want 0", got)
	}
}

func TestCacheItemReleaseStopsDownloadersAfterGracePeriod(t *testing.T) {
	setReleaseStopGracePeriodForTest(t, 20*time.Millisecond)

	parentCtx := context.Background()
	ctx, cancel := context.WithCancel(parentCtx)

	dls := &Downloaders{
		parentCtx: parentCtx,
		ctx:       ctx,
		cancel:    cancel,
	}

	item := &CacheItem{
		downloaders: dls,
	}
	item.opens.Store(1)

	item.Release()

	// Releasing the last handle should no longer stop downloaders synchronously.
	select {
	case <-ctx.Done():
		t.Fatal("downloader context was canceled immediately instead of waiting for grace period")
	default:
	}

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("expected downloader context to be canceled after release grace period")
	}

	if got := item.opens.Load(); got != 0 {
		t.Fatalf("unexpected opens after release: got %d, want 0", got)
	}
}

func TestCacheItemOpenCancelsPendingDownloaderStop(t *testing.T) {
	setReleaseStopGracePeriodForTest(t, 200*time.Millisecond)

	parentCtx := context.Background()
	ctx, cancel := context.WithCancel(parentCtx)

	dls := &Downloaders{
		parentCtx: parentCtx,
		ctx:       ctx,
		cancel:    cancel,
	}

	item := &CacheItem{
		downloaders: dls,
	}
	item.opens.Store(1)

	item.Release()
	item.Open()

	select {
	case <-ctx.Done():
		t.Fatal("downloader context was canceled even though the item reopened during grace period")
	case <-time.After(500 * time.Millisecond):
	}

	if got := item.opens.Load(); got != 1 {
		t.Fatalf("unexpected opens after reopen: got %d, want 1", got)
	}
}

func TestNoProgressWatchdogCancelsStalledAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lastProgressNanos atomic.Int64
	lastProgressNanos.Store(time.Now().Add(-300 * time.Millisecond).UnixNano())

	var timedOut atomic.Bool
	stop := startNoProgressWatchdog(
		ctx,
		120*time.Millisecond,
		10*time.Millisecond,
		&lastProgressNanos,
		cancel,
		&timedOut,
	)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected context cancellation from no-progress watchdog")
	}

	if !timedOut.Load() {
		t.Fatal("expected watchdog timeout flag to be set")
	}
}

func TestNoProgressWatchdogKeepsAliveWithProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var lastProgressNanos atomic.Int64
	lastProgressNanos.Store(time.Now().UnixNano())

	var timedOut atomic.Bool
	stop := startNoProgressWatchdog(
		ctx,
		160*time.Millisecond,
		20*time.Millisecond,
		&lastProgressNanos,
		cancel,
		&timedOut,
	)
	defer stop()

	deadline := time.Now().Add(300 * time.Millisecond)
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()

	for time.Now().Before(deadline) {
		select {
		case <-ticker.C:
			lastProgressNanos.Store(time.Now().UnixNano())
		case <-ctx.Done():
			t.Fatal("unexpected watchdog cancellation while progress was advancing")
		}
	}

	if timedOut.Load() {
		t.Fatal("watchdog timed out despite ongoing progress")
	}
}
