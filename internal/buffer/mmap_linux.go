//go:build linux

package buffer

import "golang.org/x/sys/unix"

// mmapAlloc returns a size-length buffer backed by an anonymous, private
// mmap. Unlike a heap slice, freeing it with munmapBlock returns the pages
// to the OS immediately — it does not wait for the Go GC to collect the
// slice or for the runtime scavenger to lazily MADV_FREE it. That is what
// lets a Buffer give RAM back deterministically the moment its working set
// shrinks (eviction, idle handle, Close), which is the behaviour the
// many-concurrent-streams workload needs to stay within memory.
//
// If mmap fails it falls back to a heap slice; munmapBlock tolerates that.
func mmapAlloc(size int) *[]byte {
	b, err := unix.Mmap(-1, 0, size,
		unix.PROT_READ|unix.PROT_WRITE,
		unix.MAP_ANON|unix.MAP_PRIVATE)
	if err != nil {
		h := make([]byte, size)
		return &h
	}
	return &b
}

// munmapBlock unmaps a buffer returned by mmapAlloc, returning its pages to
// the OS. It is safe to call on a heap-fallback slice: golang.org/x/sys/unix
// tracks the mappings it created and returns an error (ignored here) for a
// slice it didn't map, leaving that slice for the GC.
func munmapBlock(p *[]byte) {
	if p == nil || *p == nil {
		return
	}
	_ = unix.Munmap(*p)
}
