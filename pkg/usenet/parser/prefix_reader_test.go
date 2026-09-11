package parser

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestReadFilePrefixReusesObservedBodies(t *testing.T) {
	backend := &fakeArticleBackend{bodySize: 8}
	broker := newArticleBroker(backend, 2, 1<<20)
	p := NewParserWithSource(broker, 2, zerolog.Nop())
	file := &storage.NZBFile{
		Name:     "movie.mkv",
		Size:     16,
		FileType: storage.NZBFileTypeMedia,
		Segments: []storage.NZBSegment{
			{Number: 1, MessageID: "abcdefgh", Bytes: 8, StartOffset: 0, EndOffset: 7},
			{Number: 2, MessageID: "ijklmnop", Bytes: 8, StartOffset: 8, EndOffset: 15},
		},
	}
	if _, err := broker.Header(t.Context(), "abcdefgh", 4); err != nil {
		t.Fatal(err)
	}
	prefix, err := p.ReadFilePrefix(t.Context(), file, 12)
	if err != nil {
		t.Fatal(err)
	}
	if string(prefix) != "abcdefghijkl" {
		t.Fatalf("prefix = %q", prefix)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("network bodies = %d, want 2", got)
	}
	if _, err := p.ReadFilePrefix(t.Context(), file, 12); err != nil {
		t.Fatal(err)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("cached prefix caused another fetch: %d", got)
	}
}

func TestReadFilePrefixDefersTransformsAndRejectsGaps(t *testing.T) {
	backend := &fakeArticleBackend{bodySize: 8}
	p := NewParserWithSource(newArticleBroker(backend, 1, 1<<20), 1, zerolog.Nop())
	encrypted := &storage.NZBFile{Name: "encrypted.mkv", FileType: storage.NZBFileTypeRar, IsStored: true, IsEncrypted: true}
	if _, err := p.ReadFilePrefix(t.Context(), encrypted, 8); !errors.Is(err, ErrPrefixReadUnsupported) {
		t.Fatalf("encrypted prefix error = %v", err)
	}
	gapped := &storage.NZBFile{
		Name: "gap.mkv", Size: 8, FileType: storage.NZBFileTypeMedia,
		Segments: []storage.NZBSegment{{MessageID: "abcdefgh", Bytes: 4, StartOffset: 4, EndOffset: 7}},
	}
	if _, err := p.ReadFilePrefix(t.Context(), gapped, 8); err == nil {
		t.Fatal("gapped prefix unexpectedly succeeded")
	}
}

// ReadFilePrefix only sorts when it has to, so it must never reorder the
// caller's slice in the process - those segments are shared with the stored
// NZB file the caller is still holding.
func TestReadFilePrefixLeavesCallerSegmentsAlone(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		segments []storage.NZBSegment
	}{
		{
			name: "sorted",
			segments: []storage.NZBSegment{
				{Number: 1, MessageID: "abcdefgh", Bytes: 8, StartOffset: 0, EndOffset: 7},
				{Number: 2, MessageID: "ijklmnop", Bytes: 8, StartOffset: 8, EndOffset: 15},
			},
		},
		{
			name: "out of order",
			segments: []storage.NZBSegment{
				{Number: 2, MessageID: "ijklmnop", Bytes: 8, StartOffset: 8, EndOffset: 15},
				{Number: 1, MessageID: "abcdefgh", Bytes: 8, StartOffset: 0, EndOffset: 7},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			backend := &fakeArticleBackend{bodySize: 8}
			p := NewParserWithSource(newArticleBroker(backend, 2, 1<<20), 2, zerolog.Nop())
			file := &storage.NZBFile{
				Name:     "movie.mkv",
				Size:     16,
				FileType: storage.NZBFileTypeMedia,
				Segments: testCase.segments,
			}
			original := slices.Clone(file.Segments)

			prefix, err := p.ReadFilePrefix(t.Context(), file, 12)
			if err != nil {
				t.Fatal(err)
			}
			if string(prefix) != "abcdefghijkl" {
				t.Fatalf("prefix = %q", prefix)
			}
			if !reflect.DeepEqual(file.Segments, original) {
				t.Fatalf("caller's segments were reordered: %#v", file.Segments)
			}
		})
	}
}

// BenchmarkReadFilePrefix shows the cost the sorted fast path avoids: without
// it every head read copies and sorts the whole segment list.
func BenchmarkReadFilePrefix(b *testing.B) {
	const segmentCount = 20000
	segments := make([]storage.NZBSegment, segmentCount)
	var offset int64
	for index := range segments {
		segments[index] = storage.NZBSegment{
			Number:      index + 1,
			MessageID:   fmt.Sprintf("segment-%06d@example", index),
			Bytes:       8,
			StartOffset: offset,
			EndOffset:   offset + 7,
		}
		offset += 8
	}
	file := &storage.NZBFile{
		Name:     "movie.mkv",
		Size:     offset,
		FileType: storage.NZBFileTypeMedia,
		Segments: segments,
	}
	p := NewParserWithSource(newArticleBroker(&fakeArticleBackend{bodySize: 8}, 2, 1<<20), 2, zerolog.Nop())

	b.ReportAllocs()
	for b.Loop() {
		if _, err := p.ReadFilePrefix(b.Context(), file, 16); err != nil {
			b.Fatal(err)
		}
	}
}
