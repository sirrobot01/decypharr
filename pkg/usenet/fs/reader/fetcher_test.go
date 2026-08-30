package reader

import (
	"context"
	"fmt"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
)

// newTestFetcher builds a cache+fetcher pair with no NNTP client. maxConns=1
// keeps the prefetch worker count at zero, so queued hints stay queued and
// nothing ever dereferences the nil client.
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
	if got := len(sf.prefetchCh); got != 4 {
		t.Fatalf("expected 4 queued hints, got %d", got)
	}

	sf.CancelPendingPrefetch()

	if got := len(sf.prefetchCh); got != 0 {
		t.Errorf("expected empty queue after cancel, got %d hints", got)
	}
	if got := sf.stats.PrefetchCancelled.Load(); got != 4 {
		t.Errorf("PrefetchCancelled = %d, want 4", got)
	}

	// The dedup bits must be cleared so the same segments can be re-hinted
	// for the new window.
	sf.QueuePrefetch(3)
	if got := len(sf.prefetchCh); got != 1 {
		t.Errorf("expected segment re-queueable after cancel, queue len = %d", got)
	}
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
