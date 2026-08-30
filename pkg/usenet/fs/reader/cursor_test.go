package reader

import (
	"context"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
)

// newTestReader builds a StreamingReader over a fetcher rig (nil NNTP client,
// maxConns=1 so no prefetch workers run) with every segment pre-filled, so
// reads never need the network.
func newTestReader(t *testing.T, segCount int) *StreamingReader {
	t.Helper()
	const segSize = int64(1000)
	segs := make([]SegmentMeta, segCount)
	for i := range segs {
		segs[i] = SegmentMeta{
			MessageID:   fmt.Sprintf("<seg%d@test>", i),
			Number:      i + 1,
			Bytes:       segSize,
			StartOffset: int64(i) * segSize,
			EndOffset:   int64(i+1)*segSize - 1,
		}
	}
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.MaxConnections = 1
	cfg.PrefetchAhead = 4

	stats := &ReaderStats{}
	cache, err := NewSegmentCache(context.Background(), segs, cfg, stats, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	fetcher := NewSegmentFetcher(context.Background(), nil, cache, cfg, stats, zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	sr := &StreamingReader{
		cache:     cache,
		fetcher:   fetcher,
		config:    cfg,
		totalSize: cache.TotalSize(),
		segCount:  cache.SegmentCount(),
		cursors:   make(map[*Cursor]struct{}),
		ctx:       ctx,
		cancel:    cancel,
		logger:    zerolog.Nop(),
		stats:     stats,
	}
	t.Cleanup(func() {
		fetcher.Close()
		_ = cache.Close()
		cancel()
	})

	return sr
}

// prefillSegments makes the given segments OnDisk so reads of them are cache
// hits. Segments left Empty remain hintable by the prefetch queue.
func prefillSegments(t *testing.T, sr *StreamingReader, indices ...int) {
	t.Helper()
	for _, i := range indices {
		data := make([]byte, sr.cache.SegmentDataSize(i))
		if err := sr.cache.Put(i, data); err != nil {
			t.Fatalf("Put(%d): %v", i, err)
		}
	}
}

// TestCursorsDoNotCancelEachOthersPrefetch is the ffprobe+playback
// regression: a probe cursor jumping to the tail must not drain the
// sequential cursor's queued read-ahead.
func TestCursorsDoNotCancelEachOthersPrefetch(t *testing.T) {
	sr := newTestReader(t, 100)
	// Only the segments the reads land on are cached; the segments the
	// reader hints for prefetch stay Empty (hints for cached segments are
	// dropped at queue time).
	prefillSegments(t, sr, 0, 50, 99)
	play := sr.newCursor()
	probe := sr.newCursor()
	defer play.Close()
	defer probe.Close()

	buf := make([]byte, 500)
	ctx := context.Background()

	// Playback reads segment 0; the reader queues hints for segments 1-4.
	if _, err := play.ReadAtContext(ctx, buf, 0); err != nil {
		t.Fatal(err)
	}
	if got := len(sr.fetcher.prefetchCh); got != 4 {
		t.Fatalf("setup: expected 4 queued playback hints, got %d", got)
	}

	// A probe cursor jumping to the tail must not cancel the playback hints.
	if _, err := probe.ReadAtContext(ctx, buf, sr.totalSize-500); err != nil {
		t.Fatal(err)
	}
	if got := sr.stats.PrefetchCancelled.Load(); got != 0 {
		t.Fatalf("probe read at tail cancelled %d playback hints", got)
	}
	if got := len(sr.fetcher.prefetchCh); got != 4 {
		t.Fatalf("probe read at tail drained playback prefetch: %d hints left, want 4", got)
	}

	// A second probe read near the tail is sequential for THAT cursor —
	// still no cancellation.
	if _, err := probe.ReadAtContext(ctx, buf, sr.totalSize-1000); err != nil {
		t.Fatal(err)
	}
	if got := sr.stats.PrefetchCancelled.Load(); got != 0 {
		t.Fatalf("second probe read cancelled %d hints", got)
	}

	// But a genuine jump WITHIN the playback cursor still cancels the stale
	// window before queueing the new one.
	if _, err := play.ReadAtContext(ctx, buf, 50_000); err != nil {
		t.Fatal(err)
	}
	if got := sr.stats.PrefetchCancelled.Load(); got == 0 {
		t.Fatal("in-cursor seek did not cancel stale prefetch")
	}
}

// TestConsumedFloorTracksSlowestCursor: a tail probe must not advance the
// eviction cutoff past the playback position, and closing the probe cursor
// releases its influence.
func TestConsumedFloorTracksSlowestCursor(t *testing.T) {
	sr := newTestReader(t, 100)
	prefillSegments(t, sr, 0, 99)
	play := sr.newCursor()
	probe := sr.newCursor()
	defer play.Close()

	buf := make([]byte, 500)
	ctx := context.Background()

	if _, err := play.ReadAtContext(ctx, buf, 0); err != nil {
		t.Fatal(err)
	}
	if got := sr.cache.consumedFloor.Load(); got != 500 {
		t.Fatalf("floor after playback read = %d, want 500", got)
	}

	// A probe consuming the tail must not move the floor past the playback
	// position.
	if _, err := probe.ReadAtContext(ctx, buf, sr.totalSize-500); err != nil {
		t.Fatal(err)
	}
	if got := sr.cache.consumedFloor.Load(); got != 500 {
		t.Fatalf("floor after tail probe = %d, want 500 (slowest cursor)", got)
	}

	// Playback advances; floor follows the minimum.
	if _, err := play.ReadAtContext(ctx, buf, 500); err != nil {
		t.Fatal(err)
	}
	if got := sr.cache.consumedFloor.Load(); got != 1000 {
		t.Fatalf("floor = %d, want 1000", got)
	}

	// Probe closes: its (higher) mark leaves; floor recomputes to playback.
	probe.Close()
	if got := sr.cache.consumedFloor.Load(); got != 1000 {
		t.Fatalf("floor after probe close = %d, want 1000", got)
	}

	// A seek-back through the playback cursor pulls the floor back,
	// re-protecting the region being re-read.
	if _, err := play.ReadAtContext(ctx, buf, 0); err != nil {
		t.Fatal(err)
	}
	if got := sr.cache.consumedFloor.Load(); got != 500 {
		t.Fatalf("floor after seek-back = %d, want 500", got)
	}
}
