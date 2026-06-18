package ranges

import (
	"testing"
)

func TestRangeEnd(t *testing.T) {
	r := Range{Pos: 10, Size: 5}
	if r.End() != 15 {
		t.Errorf("Expected End() = 15, got %d", r.End())
	}
}

func TestRangeIsEmpty(t *testing.T) {
	tests := []struct {
		r        Range
		expected bool
	}{
		{Range{Pos: 0, Size: 0}, true},
		{Range{Pos: 0, Size: -1}, true},
		{Range{Pos: 0, Size: 1}, false},
	}
	for _, tc := range tests {
		if tc.r.IsEmpty() != tc.expected {
			t.Errorf("Range %+v IsEmpty() = %v, expected %v", tc.r, tc.r.IsEmpty(), tc.expected)
		}
	}
}

func TestRangeClip(t *testing.T) {
	r := Range{Pos: 10, Size: 20}
	r.Clip(25)
	if r.End() != 25 || r.Size != 15 {
		t.Errorf("Expected clipped to end=25, got %+v", r)
	}

	r2 := Range{Pos: 10, Size: 5}
	r2.Clip(20) // No change needed
	if r2.Size != 5 {
		t.Errorf("Expected unchanged, got %+v", r2)
	}
}

func TestRangesInsert(t *testing.T) {
	var rs Ranges

	rs.Insert(Range{Pos: 0, Size: 10})
	if len(rs) != 1 || rs[0].Pos != 0 || rs[0].Size != 10 {
		t.Errorf("Expected [(0,10)], got %+v", rs)
	}

	// Adjacent - should coalesce
	rs.Insert(Range{Pos: 10, Size: 10})
	if len(rs) != 1 || rs[0].Pos != 0 || rs[0].Size != 20 {
		t.Errorf("Expected [(0,20)], got %+v", rs)
	}

	// Gap
	rs.Insert(Range{Pos: 30, Size: 10})
	if len(rs) != 2 {
		t.Errorf("Expected 2 ranges, got %+v", rs)
	}

	// Fill gap
	rs.Insert(Range{Pos: 20, Size: 10})
	if len(rs) != 1 || rs[0].Size != 40 {
		t.Errorf("Expected single coalesced range, got %+v", rs)
	}
}

func TestRangesRemove(t *testing.T) {
	// Straddling removal splits a range into head + tail.
	var rs Ranges
	rs.Insert(Range{Pos: 0, Size: 100})
	rs.Remove(Range{Pos: 40, Size: 20}) // remove [40,60)
	if len(rs) != 2 || rs[0] != (Range{0, 40}) || rs[1] != (Range{60, 40}) {
		t.Fatalf("split: expected [(0,40),(60,40)], got %+v", rs)
	}
	if rs.Present(Range{Pos: 45, Size: 5}) {
		t.Fatalf("removed bytes still reported present")
	}

	// Fully-contained range is dropped entirely.
	rs = Ranges{}
	rs.Insert(Range{Pos: 10, Size: 10})
	rs.Insert(Range{Pos: 40, Size: 10})
	rs.Remove(Range{Pos: 0, Size: 30}) // covers the first range, not the second
	if len(rs) != 1 || rs[0] != (Range{40, 10}) {
		t.Fatalf("drop: expected [(40,10)], got %+v", rs)
	}

	// Overlapping the leading edge trims the front.
	rs = Ranges{}
	rs.Insert(Range{Pos: 50, Size: 50}) // [50,100)
	rs.Remove(Range{Pos: 30, Size: 40}) // [30,70) -> trims to [70,100)
	if len(rs) != 1 || rs[0] != (Range{70, 30}) {
		t.Fatalf("trim-front: expected [(70,30)], got %+v", rs)
	}

	// No-op cases.
	rs.Remove(Range{Pos: 0, Size: 0})
	rs.Remove(Range{Pos: 200, Size: 10})
	if len(rs) != 1 || rs[0] != (Range{70, 30}) {
		t.Fatalf("no-op removal changed ranges: %+v", rs)
	}
}

func TestRangesPresent(t *testing.T) {
	var rs Ranges
	rs.Insert(Range{Pos: 0, Size: 100})
	rs.Insert(Range{Pos: 200, Size: 100})

	tests := []struct {
		r        Range
		expected bool
	}{
		{Range{Pos: 0, Size: 50}, true},
		{Range{Pos: 50, Size: 50}, true},
		{Range{Pos: 0, Size: 100}, true},
		{Range{Pos: 0, Size: 101}, false},  // Extends into gap
		{Range{Pos: 100, Size: 50}, false}, // In gap
		{Range{Pos: 200, Size: 50}, true},
		{Range{Pos: 150, Size: 100}, false}, // Spans gap
	}

	for _, tc := range tests {
		if rs.Present(tc.r) != tc.expected {
			t.Errorf("Range %+v Present() = %v, expected %v", tc.r, rs.Present(tc.r), tc.expected)
		}
	}
}

func TestRangesFindMissing(t *testing.T) {
	var rs Ranges
	rs.Insert(Range{Pos: 0, Size: 100})
	rs.Insert(Range{Pos: 200, Size: 100})

	// Fully present
	r := rs.FindMissing(Range{Pos: 0, Size: 50})
	if !r.IsEmpty() && r.Pos != 50 {
		t.Errorf("Expected empty or Pos=50, got %+v", r)
	}

	// Partially present
	r = rs.FindMissing(Range{Pos: 50, Size: 100})
	if r.Pos != 100 || r.Size != 50 {
		t.Errorf("Expected (100,50), got %+v", r)
	}

	// Not present at all
	r = rs.FindMissing(Range{Pos: 100, Size: 50})
	if r.Pos != 100 || r.Size != 50 {
		t.Errorf("Expected (100,50), got %+v", r)
	}
}

func TestRangesSize(t *testing.T) {
	var rs Ranges
	rs.Insert(Range{Pos: 0, Size: 100})
	rs.Insert(Range{Pos: 200, Size: 50})

	if rs.Size() != 150 {
		t.Errorf("Expected size 150, got %d", rs.Size())
	}
}

func TestRangesFindAll(t *testing.T) {
	var rs Ranges
	rs.Insert(Range{Pos: 0, Size: 100})
	rs.Insert(Range{Pos: 200, Size: 100})

	frs := rs.FindAll(Range{Pos: 50, Size: 200})

	// Should be: present(50-100), absent(100-200), present(200-250)
	if len(frs) != 3 {
		t.Errorf("Expected 3 found ranges, got %d: %+v", len(frs), frs)
	}
}
