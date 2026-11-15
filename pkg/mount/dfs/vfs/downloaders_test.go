package vfs

import (
	"context"
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

	item := newTestItem(t, 64*testMiB)

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
		runEnd:        reqPos, // sequential from the start of the file
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

	// request is cached, look-ahead has a gap after 1 MiB
	item := newTestItem(t, 64*testMiB, ranges.Range{Pos: 0, Size: 1 * testMiB})

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

	want := req.End() + minReadAhead // first read of a run
	got := getMaxOffset(dl)
	if got != want {
		t.Fatalf("unexpected maxOffset: got %d, want %d", got, want)
	}
}

func TestReadAheadGrowsWithSequentialRun(t *testing.T) {
	const readAhead = 128 * testMiB
	dls := &Downloaders{
		item:          newTestItem(t, 1024*testMiB),
		chunkSize:     16 * testMiB,
		readAheadSize: readAhead,
	}
	window := func(pos int64) int64 {
		r := ranges.Range{Pos: pos, Size: 128 * testKiB}
		return dls.extendAndFindMissingRangeLocked(r).End() - r.End()
	}

	if got := window(0); got != minReadAhead {
		t.Fatalf("window on the first read = %d, want %d", got, minReadAhead)
	}
	for pos := int64(0); pos < 8*testMiB; pos += testMiB {
		window(pos)
	}
	if got, want := window(8*testMiB), readAheadRampFactor*(8*testMiB+128*testKiB); got != want {
		t.Fatalf("window after an 8MB run = %d, want %d", got, want)
	}
	if got := window(100 * testMiB); got != minReadAhead {
		t.Fatalf("window after a seek = %d, want %d", got, minReadAhead)
	}
	for pos := int64(100 * testMiB); pos < 140*testMiB; pos += testMiB {
		window(pos)
	}
	if got := window(140 * testMiB); got != readAhead {
		t.Fatalf("window after a 40MB run = %d, want %d", got, readAhead)
	}

	dls.item.opens.Store(2)
	if got := window(500 * testMiB); got != readAhead {
		t.Fatalf("window with two handles open = %d, want %d", got, readAhead)
	}
}

func TestEnsureDownloaderLocked_CachedWindowFullDoesNotExtend(t *testing.T) {
	const (
		reqPos    = 0
		reqSize   = 128 * testKiB
		readAhead = 16 * testMiB
	)

	// request + look-ahead fully cached
	item := newTestItem(t, 64*testMiB, ranges.Range{Pos: 0, Size: 32 * testMiB})

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

	item := &CacheItem{}
	item.downloaders.Store(dls)
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

// Stall detection now lives in the manager stream session (see
// TestSessionStallWatchdogRecovers); the downloader no longer runs its own
// no-progress watchdog.
