package ranges

import (
	"math/rand"
	"testing"
)

// referenceRemove is the pre-optimization allocating implementation, kept as
// the behavioral oracle for the in-place Remove.
func referenceRemove(rs Ranges, r Range) Ranges {
	if r.IsEmpty() || len(rs) == 0 {
		return rs
	}
	end := r.End()
	out := make(Ranges, 0, len(rs)+1)
	for _, seg := range rs {
		if seg.End() <= r.Pos || seg.Pos >= end {
			out = append(out, seg)
			continue
		}
		if seg.Pos < r.Pos {
			out = append(out, Range{Pos: seg.Pos, Size: r.Pos - seg.Pos})
		}
		if seg.End() > end {
			out = append(out, Range{Pos: end, Size: seg.End() - end})
		}
	}
	return out
}

func TestRemoveMatchesReference(t *testing.T) {
	cases := []struct {
		name string
		rs   Ranges
		r    Range
	}{
		{"no overlap before", Ranges{{100, 50}}, Range{0, 50}},
		{"no overlap after", Ranges{{0, 50}}, Range{100, 50}},
		{"no overlap between", Ranges{{0, 50}, {200, 50}}, Range{100, 50}},
		{"exact segment", Ranges{{0, 50}, {100, 50}}, Range{100, 50}},
		{"head trim", Ranges{{100, 100}}, Range{50, 100}},
		{"tail trim", Ranges{{100, 100}}, Range{150, 100}},
		{"split", Ranges{{0, 300}}, Range{100, 100}},
		{"span several", Ranges{{0, 50}, {60, 50}, {120, 50}, {200, 50}}, Range{40, 150}},
		{"remove all", Ranges{{0, 50}, {60, 50}}, Range{0, 200}},
		{"empty removal", Ranges{{0, 50}}, Range{10, 0}},
		{"empty set", Ranges{}, Range{0, 100}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := append(Ranges(nil), tc.rs...)
			got.Remove(tc.r)
			want := referenceRemove(tc.rs, tc.r)
			if !got.Equal(want) {
				t.Fatalf("Remove(%+v) on %+v:\n got %+v\nwant %+v", tc.r, tc.rs, got, want)
			}
		})
	}
}

func TestRemoveMatchesReferenceRandomized(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 5000; i++ {
		var rs Ranges
		for j := 0; j < rng.Intn(8); j++ {
			rs.Insert(Range{Pos: int64(rng.Intn(1000)), Size: int64(1 + rng.Intn(100))})
		}
		r := Range{Pos: int64(rng.Intn(1100)), Size: int64(rng.Intn(300))}

		got := append(Ranges(nil), rs...)
		got.Remove(r)
		want := referenceRemove(rs, r)
		if !got.Equal(want) {
			t.Fatalf("case %d: Remove(%+v) on %+v:\n got %+v\nwant %+v", i, r, rs, got, want)
		}
	}
}

func TestFindAllIntoMatchesFindAll(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 2000; i++ {
		var rs Ranges
		for j := 0; j < rng.Intn(6); j++ {
			rs.Insert(Range{Pos: int64(rng.Intn(1000)), Size: int64(1 + rng.Intn(100))})
		}
		r := Range{Pos: int64(rng.Intn(1000)), Size: int64(1 + rng.Intn(300))}

		want := rs.FindAll(r)
		var scratch [8]FoundRange
		got := rs.FindAllInto(r, scratch[:0])
		if len(got) != len(want) {
			t.Fatalf("case %d: len mismatch got %d want %d", i, len(got), len(want))
		}
		for k := range got {
			if got[k] != want[k] {
				t.Fatalf("case %d idx %d: got %+v want %+v", i, k, got[k], want[k])
			}
		}
	}
}
