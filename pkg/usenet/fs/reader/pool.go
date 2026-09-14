package reader

import (
	"errors"
	"sync"

	"github.com/sirrobot01/decypharr/internal/buffer"
)

// Pools holds the shared cache budgets for one Usenet service run.
type Pools struct {
	buffers *buffer.Pool
	extents *extentPool
	mu      sync.RWMutex
	closed  bool
}

func NewPools(memoryBudget int64) *Pools {
	return &Pools{
		buffers: buffer.NewPool(buffer.PoolConfig{Name: "usenet", MemoryBudget: memoryBudget}),
		extents: newExtentPool(memoryBudget),
	}
}

// Stats reports allocated buffer bytes. These are not process RSS values.
func (p *Pools) Stats() map[string]any {
	blocks := p.buffers.Stats()
	extents := p.extents.stats()
	return map[string]any{
		"memory_in_use":    blocks.MemoryInUse + extents.MemoryInUse,
		"memory_allocated": blocks.MemoryAllocated + extents.MemoryInUse,
		"block_budget":     blocks.MemoryBudget,
		"extent_budget":    extents.MemoryBudget,
		"buffers":          blocks.Buffers,
		"extent_caches":    extents.Caches,
	}
}

// Close releases the pools after the service stops its readers.
func (p *Pools) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	p.mu.Unlock()
	p.extents.mu.RLock()
	caches := make([]*SegmentCache, 0, len(p.extents.caches))
	for cache := range p.extents.caches {
		caches = append(caches, cache)
	}
	p.extents.mu.RUnlock()
	var err error
	for _, cache := range caches {
		err = errors.Join(err, cache.Close())
	}
	return errors.Join(err, p.buffers.Close())
}
