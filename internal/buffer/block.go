package buffer

// block is a fixed-size in-memory cache entry covering a single blockSize
// region of a Buffer. Blocks are aligned: blk.off % blockSize == 0.
// Which bytes of a block are valid is tracked solely by the Buffer's
// rangeSet — the block itself is just storage.
//
// Block memory comes from the owning Buffer's blockAllocator (mmap-backed
// on Linux; see alloc.go).
type block struct {
	off  int64  // block-aligned start offset within the buffer
	data []byte // exactly blockSize bytes

	// bufPtr is the exact *[]byte returned by blockAllocator.get() so that
	// dropBlockLocked can put it back unambiguously.
	bufPtr *[]byte
}
