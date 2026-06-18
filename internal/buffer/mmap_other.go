//go:build !linux

package buffer

// mmapAlloc allocates a heap-backed block on platforms without mmap. Here
// "returning" memory is left to the Go GC and scavenger — munmapBlock just
// drops the reference. The deterministic-release behaviour mmapAlloc gives
// on Linux is the optimization that's unavailable here, not a correctness
// requirement.
func mmapAlloc(size int) *[]byte {
	return new(make([]byte, size))
}

// munmapBlock drops a heap-backed block. The GC reclaims it; nothing to
// unmap.
func munmapBlock(_ *[]byte) {}
