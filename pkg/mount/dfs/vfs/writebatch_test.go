package vfs

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

// chunkReader yields readSize bytes per Read up to total, then io.EOF.
type chunkReader struct {
	readSize int
	total    int
	served   int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.served >= r.total {
		return 0, io.EOF
	}
	n := r.readSize
	if n > len(p) {
		n = len(p)
	}
	if n > r.total-r.served {
		n = r.total - r.served
	}
	r.served += n
	return n, nil
}

type recordingWriter struct {
	sizes []int
	err   error
}

func (w *recordingWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	w.sizes = append(w.sizes, len(p))
	return len(p), nil
}

func TestCopyBatchedCoalescesWithoutWaiters(t *testing.T) {
	const total = 4 << 20
	src := &chunkReader{readSize: 32 << 10, total: total}
	dst := &recordingWriter{}
	buf := make([]byte, downloadBatchSize)

	if err := copyBatched(dst, src, total, buf, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if len(dst.sizes) != total/downloadBatchSize {
		t.Fatalf("expected %d batched writes, got %d (%v)", total/downloadBatchSize, len(dst.sizes), dst.sizes)
	}
	for i, s := range dst.sizes {
		if s != downloadBatchSize {
			t.Fatalf("write %d has size %d, want %d", i, s, downloadBatchSize)
		}
	}
}

func TestCopyBatchedFlushesPerReadWithWaiters(t *testing.T) {
	const total = 1 << 20
	src := &chunkReader{readSize: 32 << 10, total: total}
	dst := &recordingWriter{}
	buf := make([]byte, downloadBatchSize)

	if err := copyBatched(dst, src, total, buf, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	if len(dst.sizes) != total/(32<<10) {
		t.Fatalf("expected per-read flushes (%d), got %d writes", total/(32<<10), len(dst.sizes))
	}
	for i, s := range dst.sizes {
		if s != 32<<10 {
			t.Fatalf("write %d has size %d, want 32KB", i, s)
		}
	}
}

func TestCopyBatchedFlushesTailOnEOF(t *testing.T) {
	// Source dies after 96KB of a 4MB request: the partial fill must be
	// flushed before the error is surfaced.
	src := &chunkReader{readSize: 32 << 10, total: 96 << 10}
	dst := &recordingWriter{}
	buf := make([]byte, downloadBatchSize)

	err := copyBatched(dst, src, 4<<20, buf, func() bool { return false })
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	var got int
	for _, s := range dst.sizes {
		got += s
	}
	if got != 96<<10 {
		t.Fatalf("flushed %d bytes before EOF, want %d", got, 96<<10)
	}
}

// overReadingReader reports more bytes than the slice it was given, the way a
// segment table with overlapping ranges used to.
type overReadingReader struct{}

func (overReadingReader) Read(p []byte) (int, error) { return 2 * len(p), nil }

func TestCopyBatchedRejectsOverRead(t *testing.T) {
	dst := &recordingWriter{}
	buf := make([]byte, downloadBatchSize)

	err := copyBatched(dst, overReadingReader{}, 4<<20, buf, func() bool { return false })
	if err == nil {
		t.Fatal("expected an error from a reader that over-reports")
	}
	if len(dst.sizes) != 0 {
		t.Fatalf("expected no writes, got %v", dst.sizes)
	}
}

func TestCopyBatchedStopsOnWriteError(t *testing.T) {
	src := &chunkReader{readSize: 32 << 10, total: 4 << 20}
	dst := &recordingWriter{err: io.EOF} // skip-stop signal from cacheWriter
	buf := make([]byte, downloadBatchSize)

	if err := copyBatched(dst, src, 4<<20, buf, func() bool { return true }); err != io.EOF {
		t.Fatalf("expected the writer's io.EOF, got %v", err)
	}
}

// TestKickWaitersFrontierGate: a parked waiter whose range is already
// satisfiable is only woken by cacheWriter.Write once the write frontier
// crosses minWaiterEnd — writes below it must not kick.
func TestKickWaitersFrontierGate(t *testing.T) {
	item, dls := newBenchItem(t, 64<<20)

	// The waiter's range: [1MB, 1.25MB), pre-filled so a kick would fulfil it.
	wr := ranges.Range{Pos: 1 << 20, Size: 256 << 10}
	if _, _, err := item.WriteAtNoOverwrite(make([]byte, wr.Size), wr.Pos); err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	dls.mu.Lock()
	dls.waiters = append(dls.waiters, waiter{r: wr, errChan: errCh})
	dls.waiterCount.Add(1)
	dls.recomputeMinWaiterEndLocked()
	dls.mu.Unlock()

	dl := &downloader{dls: dls, baseChunkSize: 4 << 20, currentChunkSize: 4 << 20}
	w := &cacheWriter{dl: dl, item: item}

	// A write whose frontier stays below the waiter's range end must not kick.
	if _, err := w.Write(make([]byte, 32<<10)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errCh:
		t.Fatal("waiter woken by a write below its range end")
	case <-time.After(50 * time.Millisecond):
	}

	// Advance the frontier past minWaiterEnd: the write ending at 1.25MB+
	// must kick and fulfil the waiter.
	w.offset = wr.End() - (32 << 10)
	if _, err := w.Write(make([]byte, 64<<10)); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("waiter failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter not woken after frontier crossed its range end")
	}
}

// TestWaiterTickerRescue: a waiter whose range becomes present without any
// cacheWriter activity (discontiguous fill) is rescued by the active-waiter
// ticker, not lost.
func TestWaiterTickerRescue(t *testing.T) {
	item, dls := newBenchItem(t, 64<<20)

	wr := ranges.Range{Pos: 8 << 20, Size: 128 << 10}
	errCh := make(chan error, 1)
	dls.mu.Lock()
	dls.waiters = append(dls.waiters, waiter{r: wr, errChan: errCh})
	dls.waiterCount.Add(1)
	dls.recomputeMinWaiterEndLocked()
	// A fake in-range downloader so the ticker's kickWaiters doesn't spawn a
	// real one against the nil manager.
	fakeCtx, fakeCancel := context.WithCancel(context.Background())
	defer fakeCancel()
	dls.dls = append(dls.dls, &downloader{
		dls:       dls,
		quit:      make(chan struct{}),
		kick:      make(chan struct{}, 1),
		ctx:       fakeCtx,
		cancel:    fakeCancel,
		start:     0,
		offset:    32 << 20,
		maxOffset: 64 << 20,
	})
	dls.mu.Unlock()

	// Make the range present outside any cacheWriter (as another downloader
	// or a reopen would).
	item.metaMu.Lock()
	item.info.Rs.Insert(wr)
	item.metaMu.Unlock()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("waiter failed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ticker never rescued the waiter")
	}
}
