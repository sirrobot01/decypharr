package buffer

// block is a fixed-size in-memory cache entry covering a single blockSize
// region of the buffer. Blocks are aligned: blk.off % blockSize == 0.
//
// Dirty tracking retains exact byte ranges so interleaved writers can update
// separate parts of one block without forcing synchronous intermediate
// flushes. Only bytes actually written are persisted.
//
// Block memory comes from the owning Buffer's blockAllocator (mmap-backed
// on Linux; see alloc.go).
type block struct {
	off  int64  // block-aligned start offset within the buffer
	data []byte // exactly blockSize bytes

	// bufPtr is the exact *[]byte returned by blockAllocator.get() so that
	// dropBlockLocked can put it back unambiguously.
	bufPtr *[]byte

	// Sorted, non-overlapping dirty byte ranges. Streaming blocks normally
	// need at most two: the tail of one segment and the head of the next. Keep
	// those inline so admitting a block does not add a heap allocation.
	dirtyInline [2]dirtyExtent
	dirty       []dirtyExtent

	// LRU doubly-linked list pointers. Managed only by the Buffer under
	// b.mu — never inspect from outside the cache layer.
	prev, next *block
}

type dirtyExtent struct {
	lo, hi int
}

// isClean reports whether the block has no pending disk write.
func (blk *block) isClean() bool { return len(blk.dirty) == 0 }

func (blk *block) initDirty() { blk.dirty = blk.dirtyInline[:0] }

// addDirty inserts [lo, hi) and merges overlaps and adjacent extents.
func (blk *block) addDirty(lo, hi int) {
	if hi <= lo {
		return
	}
	for i := range blk.dirty {
		ext := &blk.dirty[i]
		if hi < ext.lo {
			blk.dirty = append(blk.dirty, dirtyExtent{})
			copy(blk.dirty[i+1:], blk.dirty[i:])
			blk.dirty[i] = dirtyExtent{lo: lo, hi: hi}
			return
		}
		if lo > ext.hi {
			continue
		}
		if lo < ext.lo {
			ext.lo = lo
		}
		if hi > ext.hi {
			ext.hi = hi
		}
		j := i + 1
		for j < len(blk.dirty) && blk.dirty[j].lo <= ext.hi {
			if blk.dirty[j].hi > ext.hi {
				ext.hi = blk.dirty[j].hi
			}
			j++
		}
		if j > i+1 {
			copy(blk.dirty[i+1:], blk.dirty[j:])
			blk.dirty = blk.dirty[:len(blk.dirty)-(j-i-1)]
		}
		return
	}
	blk.dirty = append(blk.dirty, dirtyExtent{lo: lo, hi: hi})
}

// removeDirty removes [lo, hi) from the dirty ranges.
func (blk *block) removeDirty(lo, hi int) {
	for i := 0; i < len(blk.dirty) && blk.dirty[i].lo < hi; {
		ext := blk.dirty[i]
		switch {
		case ext.hi <= lo:
			i++
		case ext.lo >= lo && ext.hi <= hi:
			copy(blk.dirty[i:], blk.dirty[i+1:])
			blk.dirty = blk.dirty[:len(blk.dirty)-1]
		case ext.lo < lo && ext.hi > hi:
			blk.dirty[i].hi = lo
			blk.dirty = append(blk.dirty, dirtyExtent{})
			copy(blk.dirty[i+2:], blk.dirty[i+1:])
			blk.dirty[i+1] = dirtyExtent{lo: hi, hi: ext.hi}
			return
		case ext.lo < lo:
			blk.dirty[i].hi = lo
			i++
		default:
			blk.dirty[i].lo = hi
			i++
		}
	}
}

// clearDirty resets the dirty ranges while retaining their storage.
func (blk *block) clearDirty() { blk.dirty = blk.dirty[:0] }
