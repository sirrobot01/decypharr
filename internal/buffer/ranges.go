package buffer

import "sort"

// rangeSet is a sorted, non-overlapping set of half-open byte ranges
// [off, end). It tracks which bytes are "present" in the buffer (in either
// the in-memory tier or the disk-backing file).
//
// The implementation is a sorted slice — for the streaming workloads this
// package targets, the number of distinct ranges stays small (typically a
// single growing range for sequential writes, occasionally two or three
// across seeks), so a sorted slice with O(log n) lookup and O(n) insert is
// faster in practice than a tree.
//
// Not safe for concurrent use; the parent Buffer holds its own mutex.
type rangeSet struct {
	// rs is sorted by off; rs[i].end <= rs[i+1].off (strict, no adjacency
	// either — adjacent ranges are merged on insert).
	rs []extent
}

type extent struct {
	off, end int64
}

func newRangeSet() *rangeSet { return &rangeSet{} }

// insert adds [off, off+length) and merges overlaps/adjacencies.
func (r *rangeSet) insert(off, length int64) {
	if length <= 0 {
		return
	}
	end := off + length
	// Binary search for the first extent that ends >= off (i.e., could
	// overlap or be adjacent to the new range).
	i := sort.Search(len(r.rs), func(i int) bool { return r.rs[i].end >= off })
	if i == len(r.rs) {
		r.rs = append(r.rs, extent{off, end})
		return
	}
	// If the new range ends before the next extent starts, there is a real
	// gap and the range must be inserted before it. Adjacent ranges merge.
	if end < r.rs[i].off {
		// Gap before the new range: insert at position i.
		r.rs = append(r.rs, extent{})
		copy(r.rs[i+1:], r.rs[i:])
		r.rs[i] = extent{off, end}
		return
	}
	// Overlap or adjacency: expand r.rs[i] to cover the new range.
	if off < r.rs[i].off {
		r.rs[i].off = off
	}
	if end > r.rs[i].end {
		r.rs[i].end = end
	}
	// Merge any subsequent ranges now covered by the expanded r.rs[i].
	j := i + 1
	for j < len(r.rs) && r.rs[j].off <= r.rs[i].end {
		if r.rs[j].end > r.rs[i].end {
			r.rs[i].end = r.rs[j].end
		}
		j++
	}
	if j > i+1 {
		copy(r.rs[i+1:], r.rs[j:])
		r.rs = r.rs[:len(r.rs)-(j-i-1)]
	}
}

// remove removes [off, off+length) and splits ranges that straddle the
// boundary. Ranges fully inside the removed region are dropped.
func (r *rangeSet) remove(off, length int64) {
	if length <= 0 {
		return
	}
	end := off + length
	// Skip past ranges entirely below the removal.
	i := sort.Search(len(r.rs), func(i int) bool { return r.rs[i].end > off })
	if i == len(r.rs) {
		return
	}
	// Walk forward dropping or splitting until we pass `end`.
	for i < len(r.rs) && r.rs[i].off < end {
		ext := r.rs[i]
		switch {
		case ext.off >= off && ext.end <= end:
			// Fully removed.
			copy(r.rs[i:], r.rs[i+1:])
			r.rs = r.rs[:len(r.rs)-1]
			// i stays
		case ext.off < off && ext.end > end:
			// Removed region splits ext into two pieces.
			tail := extent{end, ext.end}
			r.rs[i].end = off
			r.rs = append(r.rs, extent{})
			copy(r.rs[i+2:], r.rs[i+1:])
			r.rs[i+1] = tail
			return
		case ext.off < off:
			// Trim trailing portion.
			r.rs[i].end = off
			i++
		case ext.end > end:
			// Trim leading portion.
			r.rs[i].off = end
			i++
		default:
			// Shouldn't happen given the switch above.
			i++
		}
	}
}

// present reports whether the entire range [off, off+length) is covered.
func (r *rangeSet) present(off, length int64) bool {
	if length <= 0 {
		return true
	}
	end := off + length
	i := sort.Search(len(r.rs), func(i int) bool { return r.rs[i].end > off })
	return i < len(r.rs) && r.rs[i].off <= off && r.rs[i].end >= end
}

// anyPresent reports whether any byte in [off, off+length) is covered.
// Used by block-load logic to decide whether to read from disk.
func (r *rangeSet) anyPresent(off, length int64) bool {
	if length <= 0 {
		return false
	}
	end := off + length
	i := sort.Search(len(r.rs), func(i int) bool { return r.rs[i].end > off })
	return i < len(r.rs) && r.rs[i].off < end
}

// presentRanges returns the subranges of [off, off+length) that are present.
// The returned slice is fresh and owned by the caller.
func (r *rangeSet) presentRanges(off, length int64) []Range {
	if length <= 0 {
		return nil
	}
	end := off + length
	var out []Range
	i := sort.Search(len(r.rs), func(i int) bool { return r.rs[i].end > off })
	for ; i < len(r.rs) && r.rs[i].off < end; i++ {
		lo := r.rs[i].off
		if lo < off {
			lo = off
		}
		hi := r.rs[i].end
		if hi > end {
			hi = end
		}
		if hi > lo {
			out = append(out, Range{Off: lo, Size: hi - lo})
		}
	}
	return out
}

// totalSize returns the sum of all range lengths — i.e., how many bytes are
// known to be present anywhere.
func (r *rangeSet) totalSize() int64 {
	var total int64
	for _, ext := range r.rs {
		total += ext.end - ext.off
	}
	return total
}
