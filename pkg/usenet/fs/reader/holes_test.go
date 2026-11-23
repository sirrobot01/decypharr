package reader

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

const holeTestSegSize = 64 * 1024

// newHoleTestFetcher builds a 400-segment (25MB) file. Segments that present
// rejects are missing on the server. A nil present gives no server.
func newHoleTestFetcher(t *testing.T, present func(int) bool) (*SegmentFetcher, *ReaderStats) {
	t.Helper()
	const segCount = 400
	segments := make([]SegmentMeta, segCount)
	var client *nntp.Client
	var srv *nntpd.Server
	if present != nil {
		var err error
		if srv, err = nntpd.New(nntpd.Config{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(srv.Close)
	}
	for i := range segments {
		messageID := fmt.Sprintf("<hole-%d@nntpd>", i)
		offset := int64(i * holeTestSegSize)
		if srv != nil && present(i) {
			srv.AddArticle(messageID, nntpd.Encode(nntpd.Pattern(offset, holeTestSegSize), "hole.bin", i+1, segCount*holeTestSegSize, offset))
		}
		segments[i] = SegmentMeta{
			MessageID:   messageID,
			Number:      i + 1,
			Bytes:       holeTestSegSize,
			StartOffset: offset,
			EndOffset:   offset + holeTestSegSize - 1,
		}
	}
	if srv != nil {
		host, port := srv.Addr()
		var err error
		client, err = nntp.NewClient(&config.Config{Usenet: config.Usenet{Providers: []config.UsenetProvider{{
			Host: host, Port: port, MaxConnections: 2,
		}}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = client.Close() })
	}

	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.MaxConnections = 2
	stats := &ReaderStats{}
	cache, err := NewSegmentCache(t.Context(), segments, cfg, stats, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	sf := NewSegmentFetcher(t.Context(), client, cache, cfg, stats, zerolog.Nop())
	t.Cleanup(sf.Close)
	return sf, stats
}

func TestFillHoleLimits(t *testing.T) {
	sf, _ := newHoleTestFetcher(t, nil)
	notFound := &nntp.Error{Type: nntp.ErrorTypeArticleNotFound}
	fill := func(seg int) bool {
		sf.cache.MarkFetching(seg)
		return sf.fillHole(seg, notFound)
	}

	// The first and last 8MB (128 segments each) are never filled.
	if fill(10) || fill(390) {
		t.Fatal("filled a segment in the head or tail")
	}
	if sf.fillHole(200, errors.New("connection reset")) {
		t.Fatal("filled a segment that failed for another reason")
	}
	// Four in a row fit; a fifth does not.
	for seg := 150; seg < 154; seg++ {
		if !fill(seg) {
			t.Fatalf("segment %d refused inside the limits", seg)
		}
	}
	if fill(154) {
		t.Fatal("filled a fifth missing segment in a row")
	}
	// 2% of 25MB is 8 segments: four more apart fit, then the budget ends.
	for _, seg := range []int{170, 180, 190, 200} {
		if !fill(seg) {
			t.Fatalf("segment %d refused inside the limits", seg)
		}
	}
	if fill(210) {
		t.Fatal("filled past 2% of the file")
	}
	if got := sf.cache.GetState(150); got != StateOnDisk {
		t.Fatalf("filled segment state = %s, want %s", got, StateOnDisk)
	}
}

// A missing article in the middle of a file is served as zeros; one in the
// head still fails the read.
func TestMissingArticleIsFilledInTheMiddleOnly(t *testing.T) {
	sf, stats := newHoleTestFetcher(t, func(i int) bool { return i != 200 && i != 5 })
	ctx := t.Context()

	if err := sf.EnsureSegments(ctx, 199, 201); err != nil {
		t.Fatalf("read across a missing middle article: %v", err)
	}
	if got := stats.FilledHoles.Load(); got != 1 {
		t.Fatalf("filled holes = %d, want 1", got)
	}
	buf := make([]byte, holeTestSegSize)
	if n, ok := sf.cache.ReadRangeInto(200, 0, holeTestSegSize, buf); !ok || n != holeTestSegSize {
		t.Fatalf("read filled segment = %d, %v", n, ok)
	}
	for i, b := range buf {
		if b != 0 {
			t.Fatalf("filled segment byte %d = %d, want 0", i, b)
		}
	}

	done := make(chan error, 1)
	go func() { done <- sf.EnsureSegments(ctx, 5, 5) }()
	select {
	case err := <-done:
		if !nntp.IsArticleNotFoundError(err) {
			t.Fatalf("missing head article error = %v, want article not found", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("missing head article did not fail")
	}
}
