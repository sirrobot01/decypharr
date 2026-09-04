package reader

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

// newTestFetcher builds a cache+fetcher pair with no NNTP client. Its private
// scheduler has no workers, so hints stay queued without using the nil client.
func newTestFetcher(t *testing.T, segCount int) *SegmentFetcher {
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

	stats := &ReaderStats{}
	cache, err := NewSegmentCache(context.Background(), segs, cfg, stats, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	sf := NewSegmentFetcher(context.Background(), nil, cache, cfg, stats, zerolog.Nop())
	t.Cleanup(sf.Close)
	return sf
}

func TestCancelPendingPrefetchDrainsQueue(t *testing.T) {
	sf := newTestFetcher(t, 10)

	for i := 2; i <= 5; i++ {
		sf.QueuePrefetch(i)
	}
	if got := sf.pendingPrefetch(); got != 4 {
		t.Fatalf("expected 4 queued hints, got %d", got)
	}

	sf.CancelPendingPrefetch()

	if got := sf.pendingPrefetch(); got != 0 {
		t.Errorf("expected empty queue after cancel, got %d hints", got)
	}
	if got := sf.stats.PrefetchCancelled.Load(); got != 4 {
		t.Errorf("PrefetchCancelled = %d, want 4", got)
	}

	// The dedup bits must be cleared so the same segments can be re-hinted
	// for the new window.
	sf.QueuePrefetch(3)
	if got := sf.pendingPrefetch(); got != 1 {
		t.Errorf("expected segment re-queueable after cancel, queue len = %d", got)
	}
}

func TestPrefetchRangePipelinesAndPublishesEverySegment(t *testing.T) {
	srv, cache, fetcher, stats := newPipelineTestFetcher(t, func(int) bool { return true })

	fetcher.QueuePrefetchRange(0, streamBodyPipelineDepth-1)
	waitForSegmentState(t, cache, 0, StateOnDisk)
	waitForSegmentState(t, cache, 1, StateOnDisk)
	waitForCondition(t, func() bool { return stats.Downloads.Load() == streamBodyPipelineDepth })

	if got := srv.Bodies.Load(); got != streamBodyPipelineDepth {
		t.Fatalf("BODY responses = %d, want %d", got, streamBodyPipelineDepth)
	}
	if got := stats.Downloads.Load(); got != streamBodyPipelineDepth {
		t.Fatalf("completed downloads = %d, want %d", got, streamBodyPipelineDepth)
	}
}

func TestPrefetchRangePublishesSuccessAfterMissingArticle(t *testing.T) {
	_, cache, fetcher, stats := newPipelineTestFetcher(t, func(i int) bool { return i == 1 })

	fetcher.QueuePrefetchRange(0, streamBodyPipelineDepth-1)
	waitForSegmentState(t, cache, 0, StateFailed)
	waitForSegmentState(t, cache, 1, StateOnDisk)
	waitForCondition(t, func() bool { return stats.Downloads.Load()+stats.DownloadErrors.Load() == streamBodyPipelineDepth })

	if err := cache.GetError(0); !nntp.IsArticleNotFoundError(err) {
		t.Fatalf("missing segment error = %v, want article-not-found", err)
	}
	if got := stats.Downloads.Load(); got != 1 {
		t.Fatalf("completed downloads = %d, want 1", got)
	}
	if got := stats.DownloadErrors.Load(); got != 1 {
		t.Fatalf("download errors = %d, want 1", got)
	}
}

func TestStreamBodyPipelinePlanPreservesParallelism(t *testing.T) {
	tests := []struct {
		name         string
		workers      int
		segmentCount int
		priority     fetchPriority
		wantSingles  int
	}{
		{"single worker", 1, 16, priorityPrefetch, 16},
		{"short range preserves parallelism", 8, 8, priorityPrefetch, 8},
		{"pipeline tail after filling workers", 8, 10, priorityPrefetch, 7},
		{"full multi-worker pipeline", 8, 14, priorityPrefetch, 0},
		{"two workers pipeline", 2, 2, priorityPrefetch, 0},
		{"probe may use reserved worker", 8, 9, priorityProbe, 9},
		{"probe pipeline tail", 8, 12, priorityProbe, 8},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sf := &SegmentFetcher{scheduler: &FetchScheduler{workers: tt.workers}}
			if got := sf.streamBodyPipelineSingleCount(tt.segmentCount, tt.priority); got != tt.wantSingles {
				t.Fatalf("single articles = %d, want %d", got, tt.wantSingles)
			}
		})
	}
}

func newPipelineTestFetcher(t *testing.T, present func(int) bool) (*nntpd.Server, *SegmentCache, *SegmentFetcher, *ReaderStats) {
	t.Helper()
	srv, err := nntpd.New(nntpd.Config{RTT: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	const segmentSize = 64 * 1024
	segments := make([]SegmentMeta, streamBodyPipelineDepth)
	for i := range segments {
		messageID := fmt.Sprintf("<pipeline-%d@nntpd>", i)
		offset := int64(i * segmentSize)
		if present(i) {
			srv.AddArticle(messageID, nntpd.Encode(nntpd.Pattern(offset, segmentSize), "pipeline.bin", i+1, streamBodyPipelineDepth*segmentSize, offset))
		}
		segments[i] = SegmentMeta{
			MessageID:   messageID,
			Number:      i + 1,
			Bytes:       segmentSize,
			StartOffset: offset,
			EndOffset:   offset + segmentSize - 1,
		}
	}
	host, port := srv.Addr()
	client, err := nntp.NewClient(&config.Config{Usenet: config.Usenet{Providers: []config.UsenetProvider{{
		Host: host, Port: port, MaxConnections: 2,
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.MaxConnections = 2
	cfg.DownloadTimeout = 5 * time.Second
	stats := &ReaderStats{}
	cache, err := NewSegmentCache(t.Context(), segments, cfg, stats, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	fetcher := NewSegmentFetcher(t.Context(), client, cache, cfg, stats, zerolog.Nop())
	t.Cleanup(fetcher.Close)
	return srv, cache, fetcher, stats
}

func waitForSegmentState(t *testing.T, cache *SegmentCache, segIdx int, want SegmentState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if state := cache.GetState(segIdx); state == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("segment %d state = %s, want %s (error: %v)", segIdx, cache.GetState(segIdx), want, cache.GetError(segIdx))
}

func waitForCondition(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func TestEnsureSegmentsPropagatesPermanentFailure(t *testing.T) {
	sf := newTestFetcher(t, 6)

	notFound := &nntp.Error{Type: nntp.ErrorTypeArticleNotFound, Message: "gone"}
	// Multiple missing segments exercises the concurrent fan-out path.
	for i := 1; i <= 4; i++ {
		sf.cache.MarkFetching(i)
		sf.cache.MarkFailed(i, notFound)
	}

	err := sf.EnsureSegments(context.Background(), 1, 4)
	if err == nil {
		t.Fatal("expected error for permanently failed segments")
	}
	if !nntp.IsArticleNotFoundError(err) {
		t.Errorf("expected article-not-found error, got %v", err)
	}
}

func TestSeekAbandonedWindow(t *testing.T) {
	const ahead = 40
	cases := []struct {
		name             string
		prevEnd          int64
		startSeg, endSeg int
		want             bool
	}{
		{"first read never seeks", -1, 500, 501, false},
		{"sequential next read", 10, 11, 12, false},
		{"read within ahead window", 10, 45, 46, false},
		{"jump past ahead window", 10, 51, 52, true},
		{"small backward overlap", 50, 48, 49, false},
		{"backward within window", 50, 15, 16, false},
		{"backward seek past window", 100, 10, 11, true},
		{"prefetch disabled", 10, 500, 501, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := ahead
			if tc.name == "prefetch disabled" {
				a = 0
			}
			if got := seekAbandonedWindow(tc.prevEnd, tc.startSeg, tc.endSeg, a); got != tc.want {
				t.Errorf("seekAbandonedWindow(%d, %d, %d, %d) = %v, want %v",
					tc.prevEnd, tc.startSeg, tc.endSeg, a, got, tc.want)
			}
		})
	}
}
