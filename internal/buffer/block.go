package buffer

// block is a fixed-size in-memory cache entry covering a single blockSize
// region of the buffer. Blocks are aligned: blk.off % blockSize == 0.
//
// Dirty tracking uses a single contiguous range [dirtyLo, dirtyHi). For
// strictly-sequential writes within a block (the common case for streaming
// downloads) this stays accurate without over-flushing. Non-contiguous
// writes trigger an early flush of the current dirty range before the
// new write so we never persist arbitrary block contents — only bytes
// that have actually been written.
//
// Block memory comes from the package-level blockPool (sync.Pool).
type block struct {
	off  int64  // block-aligned start offset within the buffer
	data []byte // exactly blockSize bytes

	// bufPtr is the exact *[]byte returned by blockPool.Get() so that
	// dropBlockLocked can Put it back unambiguously. Storing it avoids
	// the &blk.data trick, which puts a pointer-to-struct-field into
	// the pool and only happens to work because the underlying array
	// stays alive — fragile to layout changes.
	bufPtr *[]byte

	// Dirty tracking. -1/-1 means clean (nothing to flush).
	// Otherwise [dirtyLo, dirtyHi) is the byte range inside data that
	// needs to be persisted to disk.
	dirtyLo, dirtyHi int

	// LRU doubly-linked list pointers. Managed only by the Buffer under
	// b.mu — never inspect from outside the cache layer.
	prev, next *block
}

// isClean reports whether the block has no pending disk write.
func (blk *block) isClean() bool { return blk.dirtyLo < 0 }

// addDirty merges [lo, hi) into the block's dirty range. Returns false if
// the new range is not contiguous with the existing dirty range — the
// caller must flush the existing dirty range before applying the new write,
// otherwise the flush would have to write bytes that were never touched.
func (blk *block) addDirty(lo, hi int) (contiguous bool) {
	if blk.dirtyLo < 0 {
		blk.dirtyLo = lo
		blk.dirtyHi = hi
		return true
	}
	// Adjacent or overlapping (allow touching boundaries: lo == dirtyHi or
	// hi == dirtyLo merges cleanly).
	if lo <= blk.dirtyHi && hi >= blk.dirtyLo {
		if lo < blk.dirtyLo {
			blk.dirtyLo = lo
		}
		if hi > blk.dirtyHi {
			blk.dirtyHi = hi
		}
		return true
	}
	return false
}

// clearDirty resets the dirty range to clean. Call after a successful flush.
func (blk *block) clearDirty() {
	blk.dirtyLo = -1
	blk.dirtyHi = -1
}
