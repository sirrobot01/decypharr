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

func getMaxOffset(dl *downloader) int64 {
	dl.mu.Lock()
	defer dl.mu.Unlock()
	return dl.maxOffset
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

func TestCacheItemReleaseStopsDownloadersOnZeroOpens(t *testing.T) {
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

	select {
	case <-ctx.Done():
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected downloader context to be canceled when opens reaches zero")
	}

	if got := item.opens.Load(); got != 0 {
		t.Fatalf("unexpected opens after release: got %d, want 0", got)
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
