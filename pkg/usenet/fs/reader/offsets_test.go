package reader

import "testing"

func TestComputeOffsetsUsesMetadataWhenAscending(t *testing.T) {
	segs := []SegmentMeta{
		{Bytes: 100, StartOffset: 0, EndOffset: 99},
		{Bytes: 100, StartOffset: 100, EndOffset: 199},
		{Bytes: 50, StartOffset: 200, EndOffset: 249},
	}

	got := computeOffsets(segs)
	want := []int64{0, 100, 200, 250}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("offsets = %v, want %v", got, want)
		}
	}
}

func TestComputeOffsetsFallsBackOnOverlap(t *testing.T) {
	// A zero-filled slot from a .meta file written before parsing rejected
	// files with holes: its offsets sit inside the segment before it, so the
	// two cover the same bytes. Trusting them makes reads double-count.
	segs := []SegmentMeta{
		{Bytes: 100, StartOffset: 0, EndOffset: 99},
		{Bytes: 0, StartOffset: 0, EndOffset: 0},
		{Bytes: 100, StartOffset: 100, EndOffset: 199},
	}

	// The cumulative layout gives the empty slot the usual unknown-size
	// default, so the segments after it keep disjoint ranges.
	got := computeOffsets(segs)
	want := []int64{0, 100, 100 + 750*1024, 100 + 750*1024 + 100}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("offsets = %v, want cumulative %v", got, want)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i] < got[i-1] {
			t.Fatalf("offsets not ascending: %v", got)
		}
	}
}
