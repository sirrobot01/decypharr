package parser

import (
	"io"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

func TestArticleReaderAtReadsAcrossSlicedSegments(t *testing.T) {
	t.Parallel()

	backend := &fakeArticleBackend{bodySize: 8}
	broker := newArticleBroker(backend, 2, 1<<20)
	volumes := []*types.Volume{{
		Index: 0,
		Name:  "archive.bin",
		Size:  10,
		Segments: []storage.NZBSegment{
			{MessageID: "abcdefgh", Bytes: 6, SegmentDataStart: 1},
			{MessageID: "ijklmnop", Bytes: 4, SegmentDataStart: 2},
		},
	}}

	reader, size, err := newArticleReaderAt(t.Context(), broker, volumes)
	if err != nil {
		t.Fatal(err)
	}
	if size != 10 {
		t.Fatalf("reader size = %d, want 10", size)
	}
	buffer := make([]byte, 6)
	if _, err := reader.ReadAt(buffer, 4); err != nil {
		t.Fatal(err)
	}
	if got, want := string(buffer), "fgklmn"; got != want {
		t.Fatalf("cross-segment read = %q, want %q", got, want)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("network fetches = %d, want 2", got)
	}
	if _, err := reader.ReadAt(buffer[:2], 4); err != nil {
		t.Fatal(err)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("cached read network fetches = %d, want 2", got)
	}
	if n, err := reader.ReadAt(make([]byte, 2), 9); n != 1 || err != io.EOF {
		t.Fatalf("tail read = (%d, %v), want (1, EOF)", n, err)
	}
}

func TestArticleReaderAtRejectsInconsistentVolume(t *testing.T) {
	t.Parallel()

	broker := newArticleBroker(&fakeArticleBackend{}, 1, 1<<20)
	_, _, err := newArticleReaderAt(t.Context(), broker, []*types.Volume{{
		Name: "bad", Size: 2, Segments: []storage.NZBSegment{{MessageID: "one", Bytes: 1}},
	}})
	if err == nil {
		t.Fatal("inconsistent volume succeeded")
	}
}
