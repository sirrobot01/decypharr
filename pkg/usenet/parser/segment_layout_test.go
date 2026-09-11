package parser

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestSegmentLayoutSlicesAcrossBoundaries(t *testing.T) {
	base := []storage.NZBSegment{
		{Number: 1, MessageID: "one", Bytes: 5, Group: "alt.test", SegmentDataStart: 2},
		{Number: 2, MessageID: "two", Bytes: 7, Group: "alt.test", SegmentDataStart: 3},
		{Number: 3, MessageID: "three", Bytes: 4, Group: "alt.test"},
	}
	layout, err := newSegmentLayout(base)
	if err != nil {
		t.Fatal(err)
	}

	segments, err := layout.slice(4, 9, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(segments))
	}
	want := []storage.NZBSegment{
		{Number: 1, MessageID: "one", Bytes: 1, StartOffset: 0, EndOffset: 0, Group: "alt.test", SegmentDataStart: 6},
		{Number: 2, MessageID: "two", Bytes: 7, StartOffset: 1, EndOffset: 7, Group: "alt.test", SegmentDataStart: 3},
		{Number: 3, MessageID: "three", Bytes: 1, StartOffset: 8, EndOffset: 8, Group: "alt.test"},
	}
	for index := range want {
		if segments[index] != want[index] {
			t.Fatalf("segment %d = %#v, want %#v", index, segments[index], want[index])
		}
	}
}

func TestSegmentLayoutRejectsInvalidRangesAndVolumes(t *testing.T) {
	layout, err := newSegmentLayout([]storage.NZBSegment{
		{MessageID: "one", Bytes: 5},
		{MessageID: "two", Bytes: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		offset int64
		length int64
	}{
		{offset: -1, length: 1},
		{offset: 9, length: 2},
		{offset: math.MaxInt64 - 1, length: 4},
	} {
		if _, err := layout.slice(test.offset, test.length, true); err == nil {
			t.Fatalf("slice(%d, %d) unexpectedly succeeded", test.offset, test.length)
		}
	}
	if err := layout.validateVolumes([]storage.ArchiveVolumeInfo{
		{Name: "one", Size: 6, SegmentStart: 0, SegmentEnd: 1},
		{Name: "two", Size: 4, SegmentStart: 1, SegmentEnd: 2},
	}); err == nil {
		t.Fatal("mismatched volume sizes unexpectedly validated")
	}
}

func TestSegmentLayoutMatchesLegacyRangeMapping(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	segments := make([]storage.NZBSegment, 2_000)
	var total int64
	for index := range segments {
		size := int64(random.IntN(2_048) + 1)
		segments[index] = storage.NZBSegment{
			Number:           index + 1,
			MessageID:        "article",
			Bytes:            size,
			Group:            "alt.test",
			SegmentDataStart: int64(random.IntN(32)),
		}
		total += size
	}
	layout, err := newSegmentLayout(segments)
	if err != nil {
		t.Fatal(err)
	}
	for range 2_000 {
		offset := random.Int64N(total)
		length := random.Int64N(total-offset) + 1
		got, err := layout.slice(offset, length, true)
		if err != nil {
			t.Fatal(err)
		}
		want := legacyRangeMapping(segments, offset, length)
		if !slices.Equal(got, want) {
			t.Fatalf("range [%d, %d) differs\ngot:  %#v\nwant: %#v", offset, offset+length, got, want)
		}
	}
}

func legacyRangeMapping(segments []storage.NZBSegment, offset, length int64) []storage.NZBSegment {
	targetEnd := offset + length - 1
	var result []storage.NZBSegment
	var sourcePosition int64
	var outputPosition int64
	for _, segment := range segments {
		sourceBytes := segment.Bytes
		segmentStart := sourcePosition
		segmentEnd := sourcePosition + sourceBytes - 1
		if segmentEnd < offset {
			sourcePosition += sourceBytes
			continue
		}
		if segmentStart > targetEnd {
			break
		}
		overlapStart := max(segmentStart, offset)
		overlapEnd := min(segmentEnd, targetEnd)
		bytesToRead := overlapEnd - overlapStart + 1
		segment.SegmentDataStart += overlapStart - segmentStart
		segment.Bytes = bytesToRead
		segment.StartOffset = outputPosition
		segment.EndOffset = outputPosition + bytesToRead - 1
		result = append(result, segment)
		outputPosition += bytesToRead
		if overlapEnd == targetEnd {
			break
		}
		sourcePosition += sourceBytes
	}
	return result
}

func BenchmarkSegmentLayoutSlice(b *testing.B) {
	segments := benchmarkSegments()
	layout, err := newSegmentLayout(segments)
	if err != nil {
		b.Fatal(err)
	}
	offset := int64(9_000) * 750_000
	b.ReportAllocs()
	for b.Loop() {
		if _, err := layout.slice(offset, 1_500_000, true); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkLegacySegmentRangeSlice(b *testing.B) {
	segments := benchmarkSegments()
	offset := int64(9_000) * 750_000
	b.ReportAllocs()
	for b.Loop() {
		_ = legacyRangeMapping(segments, offset, 1_500_000)
	}
}

func benchmarkSegments() []storage.NZBSegment {
	segments := make([]storage.NZBSegment, 20_000)
	for index := range segments {
		segments[index] = storage.NZBSegment{MessageID: "article", Bytes: 750_000}
	}
	return segments
}
