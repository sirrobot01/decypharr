package buffer

import "sync"

// Background unmapper. munmap is a TLB-shootdown syscall; on Linux it can stall
// for tens of microseconds and, worse, the eviction path calls put() while the
// owning Buffer holds its exclusive lock (dropBlockLocked runs under b.mu).
// Running the syscall there stalls every waiting reader — the tail-latency
// regression vs the old sync.Pool, whose Put never syscalls. Handing overflow
// blocks to a shared goroutine keeps the lock holder off the syscall: the pages
// are still released promptly (the queue drains continuously), only the *timing*
// moves off the critical path, so deterministic RAM release is preserved.
var unmapCh = make(chan *[]byte, 256)

func init() {
	go func() {
		for p := range unmapCh {
			munmapBlock(p)
		}
	}()
}

// releaseBlock unmaps p off the caller's goroutine when possible, falling back
// to an inline unmap only if the queue is saturated (rare churn burst).
func releaseBlock(p *[]byte) {
	if p == nil {
		return
	}
	select {
	case unmapCh <- p:
	default:
		munmapBlock(p)
	}
}

// maxReuseBlocks caps how many freed blocks a blockAllocator keeps on hand
// for reuse. The free list exists only to absorb steady-state churn —
// where every eviction is immediately followed by a load of the next block
// — so it can stay small. Anything freed beyond this is unmapped at once,
// which is what makes a shrinking working set actually return RAM. 8 blocks
// = 8 MB of reuse headroom per Buffer, more than enough for sequential
// streaming's evict-then-load cadence.
const maxReuseBlocks = 8

// blockAllocator hands out fixed blockSize buffers and returns them to the
// OS when they are no longer needed.
//
// It replaces the per-Buffer sync.Pool. A sync.Pool only releases its
// contents lazily — entries survive until a GC, and the freed heap pages
// return to the OS later still, on the scavenger's schedule (MADV_FREE).
// Under many concurrent streams that lag let RSS climb into OOM territory.
// This allocator instead frees via munmapBlock the moment a block falls out
// of the small reuse window, so memory tracks the live working set closely
// without relying on GC pacing or GOMEMLIMIT.
//
// Get/Put are called both with and without the owning Buffer's mu held
// (the promote path allocates before locking), so the allocator carries its
// own mutex. Lock order is always Buffer.mu -> blockAllocator.mu; the
// allocator never reaches back for Buffer.mu, so this can't deadlock.
type blockAllocator struct {
	mu      sync.Mutex
	free    []*[]byte
	maxFree int
}

// get returns a blockSize buffer, reusing a freed one when available.
func (a *blockAllocator) get() *[]byte {
	a.mu.Lock()
	if n := len(a.free); n > 0 {
		p := a.free[n-1]
		a.free[n-1] = nil
		a.free = a.free[:n-1]
		a.mu.Unlock()
		return p
	}
	a.mu.Unlock()
	return mmapAlloc(blockSize)
}

// put hands a buffer back. It is retained for reuse if the free list has
// room, otherwise unmapped immediately so the pages return to the OS.
func (a *blockAllocator) put(p *[]byte) {
	if p == nil {
		return
	}
	a.mu.Lock()
	if len(a.free) < a.maxFree {
		a.free = append(a.free, p)
		a.mu.Unlock()
		return
	}
	a.mu.Unlock()
	// Off the caller's goroutine: put() is called under the Buffer's exclusive
	// lock on the eviction path, and munmap must not stall readers there.
	releaseBlock(p)
}

// drain unmaps every buffer held for reuse. Call on Buffer.Close.
func (a *blockAllocator) drain() {
	a.mu.Lock()
	free := a.free
	a.free = nil
	a.mu.Unlock()
	for _, p := range free {
		munmapBlock(p)
	}
}
