package reader

import (
	"sync"
	"sync/atomic"
)

type extentPool struct {
	budget  int64
	inUse   atomic.Int64
	demand  atomic.Int64
	mu      sync.RWMutex
	caches  map[*SegmentCache]struct{}
	reclaim sync.Mutex
}

type extentPoolStats struct {
	MemoryInUse  int64
	MemoryBudget int64
	Caches       int
}

func newExtentPool(budget int64) *extentPool {
	return &extentPool{budget: max(budget, 0), caches: make(map[*SegmentCache]struct{})}
}

func (p *extentPool) register(cache *SegmentCache) {
	if p == nil || cache == nil {
		return
	}
	p.mu.Lock()
	p.caches[cache] = struct{}{}
	p.mu.Unlock()
	p.demand.Add(cache.memBudget)
}

func (p *extentPool) unregister(cache *SegmentCache) {
	if p == nil || cache == nil {
		return
	}
	p.mu.Lock()
	_, ok := p.caches[cache]
	delete(p.caches, cache)
	p.mu.Unlock()
	if ok {
		p.demand.Add(-cache.memBudget)
	}
}

func (p *extentPool) add(n int64) {
	if p == nil || n <= 0 {
		return
	}
	if used := p.inUse.Add(n); p.budget > 0 && used > p.budget {
		p.reclaimMemory()
	}
}

func (p *extentPool) release(n int64) {
	if p != nil && n > 0 {
		p.inUse.Add(-n)
	}
}

func (p *extentPool) shareFor(cache *SegmentCache) int64 {
	if p == nil || p.budget <= 0 || cache == nil {
		return 0
	}
	demand := p.demand.Load()
	if demand <= cache.memBudget {
		return min(cache.memBudget, p.budget)
	}
	share := int64(float64(p.budget) * float64(cache.memBudget) / float64(demand))
	return min(max(share, cache.segBytesHint), cache.memBudget)
}

func (p *extentPool) reclaimMemory() {
	if p == nil || p.budget <= 0 || p.inUse.Load() <= p.budget {
		return
	}
	p.reclaim.Lock()
	defer p.reclaim.Unlock()
	if p.inUse.Load() <= p.budget {
		return
	}

	for range 4 {
		if p.inUse.Load() <= p.budget {
			return
		}
		p.mu.RLock()
		caches := make([]*SegmentCache, 0, len(p.caches))
		for cache := range p.caches {
			caches = append(caches, cache)
		}
		p.mu.RUnlock()
		var released int64
		for _, cache := range caches {
			released += cache.trimResidentTo(p.shareFor(cache))
			if p.inUse.Load() <= p.budget {
				return
			}
		}
		if released == 0 {
			return
		}
	}
}

func (p *extentPool) stats() extentPoolStats {
	if p == nil {
		return extentPoolStats{}
	}
	p.mu.RLock()
	n := len(p.caches)
	p.mu.RUnlock()
	return extentPoolStats{MemoryInUse: p.inUse.Load(), MemoryBudget: p.budget, Caches: n}
}
