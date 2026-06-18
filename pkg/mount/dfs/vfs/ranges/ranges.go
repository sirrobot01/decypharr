// Package ranges provides the Ranges type for keeping track of byte
// ranges which may or may not be present in an object.
// Ported from rclone/lib/ranges with modifications.
package ranges

import (
	"sort"
)

// Range describes a single byte range
type Range struct {
	Pos  int64
	Size int64
}

// End returns the end of the Range (exclusive)
func (r Range) End() int64 {
	return r.Pos + r.Size
}

// IsEmpty returns true if the range has no size
func (r Range) IsEmpty() bool {
	return r.Size <= 0
}

// Clip ensures r.End() <= offset by modifying r.Size if necessary.
// If r.Pos > offset then an empty Range will be returned.
func (r *Range) Clip(offset int64) {
	if r.End() <= offset {
		return
	}
	r.Size -= r.End() - offset
	if r.Size < 0 {
		r.Pos = 0
		r.Size = 0
	}
}

// Intersection returns the common Range for two Ranges.
// If there is no intersection then the Range returned will have IsEmpty() true.
func (r Range) Intersection(b Range) (intersection Range) {
	if (r.Pos >= b.Pos && r.Pos < b.End()) || (b.Pos >= r.Pos && b.Pos < r.End()) {
		intersection.Pos = max(r.Pos, b.Pos)
		intersection.Size = min(r.End(), b.End()) - intersection.Pos
	}
	return
}

// Ranges describes a number of Range segments. These should only be
// added with the Ranges.Insert function. The Ranges are kept sorted
// and coalesced to the minimum size.
type Ranges []Range

// merge the Range new into dest if possible.
// dst.Pos must be >= src.Pos.
// Returns true if merged.
func merge(new, dst *Range) bool {
	if new.End() < dst.Pos {
		return false
	}
	if new.End() > dst.End() {
		dst.Size = new.Size
	} else {
		dst.Size += dst.Pos - new.Pos
	}
	dst.Pos = new.Pos
	return true
}

// coalesce ranges assuming an element has been inserted at i
func (rs *Ranges) coalesce(i int) {
	ranges := *rs
	var j int
	startChop := i
	endChop := i
	// look at previous element too
	if i > 0 && merge(&ranges[i-1], &ranges[i]) {
		startChop = i - 1
	}
	for j = i; j < len(ranges)-1; j++ {
		if !merge(&ranges[j], &ranges[j+1]) {
			break
		}
		endChop = j + 1
	}
	if endChop > startChop {
		// chop the unneeded ranges out
		copy(ranges[startChop:], ranges[endChop:])
		*rs = ranges[:len(ranges)-endChop+startChop]
	}
}

// search finds the first Range in rs that has Pos >= r.Pos.
// The return takes on values 0..len(rs) so may point beyond the end of the slice.
func (rs Ranges) search(r Range) int {
	return sort.Search(len(rs), func(i int) bool {
		return rs[i].Pos >= r.Pos
	})
}

// Insert the new Range into a sorted and coalesced slice of Ranges.
// The result will be sorted and coalesced.
func (rs *Ranges) Insert(r Range) {
	if r.IsEmpty() {
		return
	}
	ranges := *rs
	if len(ranges) == 0 {
		ranges = append(ranges, r)
		*rs = ranges
		return
	}
	i := ranges.search(r)
	if i == len(ranges) || !merge(&r, &ranges[i]) {
		// insert into the range
		ranges = append(ranges, Range{})
		copy(ranges[i+1:], ranges[i:])
		ranges[i] = r
		*rs = ranges
	}
	rs.coalesce(i)
}

// Find searches for r in rs and returns the next present or absent Range.
// It returns:
//   - curr: the Range found
//   - next: the Range which should be presented to Find next
//   - present: whether curr is present or absent
//
// If !next.IsEmpty() then Find should be called again with r = next
// to retrieve the next Range.
func (rs Ranges) Find(r Range) (curr, next Range, present bool) {
	if r.IsEmpty() {
		return r, next, false
	}
	var intersection Range
	i := rs.search(r)
	if i > 0 {
		prev := rs[i-1]
		// we know prev.Pos < r.Pos so intersection.Pos == r.Pos
		intersection = prev.Intersection(r)
		if !intersection.IsEmpty() {
			r.Pos = intersection.End()
			r.Size -= intersection.Size
			return intersection, r, true
		}
	}
	if i >= len(rs) {
		return r, Range{}, false
	}
	found := rs[i]
	intersection = found.Intersection(r)
	if intersection.IsEmpty() {
		return r, Range{}, false
	}
	if r.Pos < intersection.Pos {
		curr = Range{
			Pos:  r.Pos,
			Size: intersection.Pos - r.Pos,
		}
		r.Pos = curr.End()
		r.Size -= curr.Size
		return curr, r, false
	}
	r.Pos = intersection.End()
	r.Size -= intersection.Size
	return intersection, r, true
}

// FoundRange is returned from FindAll
type FoundRange struct {
	R       Range
	Present bool
}

// FindAll repeatedly calls Find searching for r in rs and returning
// present or absent ranges.
func (rs Ranges) FindAll(r Range) (frs []FoundRange) {
	for !r.IsEmpty() {
		var fr FoundRange
		fr.R, r, fr.Present = rs.Find(r)
		frs = append(frs, fr)
	}
	return frs
}

// Present returns whether r can be satisfied by rs
func (rs Ranges) Present(r Range) bool {
	if r.IsEmpty() {
		return true
	}
	_, next, present := rs.Find(r)
	if !present {
		return false
	}
	if next.IsEmpty() {
		return true
	}
	return false
}

// Intersection works out which ranges out of rs are entirely
// contained within r and returns a new Ranges
func (rs Ranges) Intersection(r Range) (newRs Ranges) {
	if len(rs) == 0 {
		return rs
	}
	for !r.IsEmpty() {
		var curr Range
		var found bool
		curr, r, found = rs.Find(r)
		if found {
			newRs.Insert(curr)
		}
	}
	return newRs
}

// Equal returns true if rs == bs
func (rs Ranges) Equal(bs Ranges) bool {
	if len(rs) != len(bs) {
		return false
	}
	if rs == nil || bs == nil {
		return true
	}
	for i := range rs {
		if rs[i] != bs[i] {
			return false
		}
	}
	return true
}

// Size returns the total size of all the segments
func (rs Ranges) Size() (size int64) {
	for _, r := range rs {
		size += r.Size
	}
	return size
}

// FindMissing finds the initial part of r that is not in rs.
// If r is entirely present in rs then an empty Range will be returned.
// For all returns rout.End() == r.End()
func (rs Ranges) FindMissing(r Range) (rout Range) {
	rout = r
	if r.IsEmpty() {
		return rout
	}
	curr, _, present := rs.Find(r)
	if !present {
		// Initial block is not present
		return rout
	}
	rout.Size -= curr.End() - rout.Pos
	rout.Pos = curr.End()
	return rout
}

// Remove subtracts r from rs: any present bytes within [r.Pos, r.End()) are
// dropped, splitting a straddling range into the surviving head/tail. Used when
// the buffer pool punches a hole behind the read head so the persisted metadata
// stops claiming bytes that are no longer on disk.
func (rs *Ranges) Remove(r Range) {
	if r.IsEmpty() || len(*rs) == 0 {
		return
	}
	end := r.End()
	// Fresh slice: a straddling segment splits into two outputs, so we can't
	// safely reuse the backing array (it could overwrite unread input).
	out := make(Ranges, 0, len(*rs)+1)
	for _, seg := range *rs {
		// No overlap: keep the segment whole.
		if seg.End() <= r.Pos || seg.Pos >= end {
			out = append(out, seg)
			continue
		}
		// Surviving head [seg.Pos, r.Pos).
		if seg.Pos < r.Pos {
			out = append(out, Range{Pos: seg.Pos, Size: r.Pos - seg.Pos})
		}
		// Surviving tail [end, seg.End()).
		if seg.End() > end {
			out = append(out, Range{Pos: end, Size: seg.End() - end})
		}
	}
	*rs = out
}

// Clear removes all ranges
func (rs *Ranges) Clear() {
	*rs = (*rs)[:0]
}
