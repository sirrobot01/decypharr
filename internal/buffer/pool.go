package buffer

import (
	"sync"
	"sync/atomic"
	"time"
)

func nowNano() int64 { return time.Now().UnixNano() }

// Pool is the buffer service: it owns a RAM budget and a disk limit shared
// across every Buffer it hands out, plus the eviction policy that enforces
// them. Instantiate one per workload (e.g. one for DFS, one for usenet) so the
// two have independent budgets and can't starve each other.
//
// Memory: per-Buffer Config.MemorySize is a ceiling on one stream's hot
// working set; the Pool's MemoryBudget caps the *sum* of resident block RAM
// across all its Buffers. When the budget is exhausted a Buffer evicts its own
// trailing (LRU) blocks before caching new ones, and writes fall through to
// disk rather than growing RAM (self-eviction; no cross-Buffer LRU).
//
// Disk: the Pool tracks the total on-disk present bytes across its Buffers.
// When that exceeds DiskLimit a background worker punches holes behind each
// Buffer's read head (keeping a BackWindow of recent history) until the total
// is back under the limit. This is what bounds a cache directory even when a
// single huge file is being streamed and never closed — the case whole-file
// eviction can't handle. DiskLimit == 0 disables the disk backstop entirely
// (usenet relies on its own playback-aware sliding window instead).
type Pool struct {
	name       string
	memBudget  atomic.Int64 // RAM ceiling across Buffers; 0 = unlimited
	diskLimit  atomic.Int64 // on-disk present bytes ceiling; 0 = unlimited
	backWindow int64        // bytes retained behind a read head before punching

	memInUse  atomic.Int64 // sum of resident block RAM across Buffers
	diskInUse atomic.Int64 // sum of on-disk present bytes across Buffers

	mu      sync.RWMutex
	buffers map[*Buffer]struct{}

	evictSig chan struct{}
	stopCh   chan struct{}
	wg       sync.WaitGroup
	closed   atomic.Bool

	statsPunches   atomic.Int64
	statsReclaimed atomic.Int64
}

// PoolConfig configures a Pool.
type PoolConfig struct {
	// Name labels the pool for logging/metrics ("dfs", "usenet").
	Name string

	// MemoryBudget caps the sum of resident block RAM across all Buffers in
	// this pool. 0 = unlimited.
	MemoryBudget int64

	// DiskLimit caps the sum of on-disk present bytes across all Buffers. When
	// exceeded, the pool punches holes behind read heads to reclaim disk.
	// 0 = no disk backstop.
	DiskLimit int64

	// BackWindow is how many bytes behind a Buffer's read head the disk
	// backstop preserves before punching (short seek-backs stay local). Only
	// meaningful when DiskLimit > 0.
	BackWindow int64
}

// PoolStats reports pool-wide counters.
type PoolStats struct {
	MemoryInUse    int64
	MemoryBudget   int64
	DiskInUse      int64
	DiskLimit      int64
	Buffers        int
	DiskPunches    int64
	BytesReclaimed int64
}

// NewPool creates a Pool. If DiskLimit > 0 it starts a background worker that
// reclaims disk by punching behind read heads when the limit is breached.
func NewPool(cfg PoolConfig) *Pool {
	if cfg.MemoryBudget < 0 {
		cfg.MemoryBudget = 0
	}
	if cfg.DiskLimit < 0 {
		cfg.DiskLimit = 0
	}
	if cfg.BackWindow < 0 {
		cfg.BackWindow = 0
	}
	p := &Pool{
		name:       cfg.Name,
		backWindow: cfg.BackWindow,
		buffers:    make(map[*Buffer]struct{}),
		evictSig:   make(chan struct{}, 1),
		stopCh:     make(chan struct{}),
	}
	p.memBudget.Store(cfg.MemoryBudget)
	p.diskLimit.Store(cfg.DiskLimit)
	if cfg.DiskLimit > 0 {
		p.wg.Add(1)
		go p.diskEvictLoop()
	}
	return p
}

// NewBuffer creates a Buffer bound to this pool. Its RAM and disk usage count
// against the pool's budgets.
func (p *Pool) NewBuffer(cfg Config) (*Buffer, error) {
	b, err := newBuffer(p, cfg)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.buffers[b] = struct{}{}
	p.mu.Unlock()
	return b, nil
}

// remove unregisters a Buffer from the pool. Called from Buffer.Close.
func (p *Pool) remove(b *Buffer) {
	p.mu.Lock()
	delete(p.buffers, b)
	p.mu.Unlock()
}

// Stats returns a snapshot of the pool's counters.
func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	n := len(p.buffers)
	p.mu.RUnlock()
	return PoolStats{
		MemoryInUse:    p.memInUse.Load(),
		MemoryBudget:   p.memBudget.Load(),
		DiskInUse:      p.diskInUse.Load(),
		DiskLimit:      p.diskLimit.Load(),
		Buffers:        n,
		DiskPunches:    p.statsPunches.Load(),
		BytesReclaimed: p.statsReclaimed.Load(),
	}
}

// Close stops the disk worker and closes every Buffer still registered. Safe
// to call when callers already own their Buffers' lifetimes — Buffer.Close is
// idempotent.
func (p *Pool) Close() error {
	if !p.closed.CompareAndSwap(false, true) {
		return nil
	}
	if p.diskLimit.Load() > 0 {
		close(p.stopCh)
		p.wg.Wait()
	}
	p.mu.Lock()
	bufs := make([]*Buffer, 0, len(p.buffers))
	for b := range p.buffers {
		bufs = append(bufs, b)
	}
	p.mu.Unlock()
	var firstErr error
	for _, b := range bufs {
		if err := b.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// --- RAM admission ----------------------------------------------------------

// wouldExceedMemory reports whether caching one more block would push the
// pool's resident total past the budget.
func (p *Pool) wouldExceedMemory() bool {
	b := p.memBudget.Load()
	return b > 0 && p.memInUse.Load()+int64(blockSize) > b
}

func (p *Pool) addBlock()         { p.memInUse.Add(int64(blockSize)) }
func (p *Pool) dropBytes(n int64) {
	if n > 0 {
		p.memInUse.Add(-n)
	}
}

// --- Disk accounting + backstop ---------------------------------------------

// addDisk records newly-present on-disk bytes and pokes the backstop if the
// limit is now exceeded.
func (p *Pool) addDisk(n int64) {
	if n <= 0 {
		return
	}
	v := p.diskInUse.Add(n)
	if limit := p.diskLimit.Load(); limit > 0 && v > limit {
		p.signalDiskEvict()
	}
}

// subDisk records on-disk bytes that are no longer present (punched, discarded,
// or a closed Buffer's residual).
func (p *Pool) subDisk(n int64) {
	if n > 0 {
		p.diskInUse.Add(-n)
	}
}

func (p *Pool) signalDiskEvict() {
	select {
	case p.evictSig <- struct{}{}:
	default:
	}
}

func (p *Pool) diskEvictLoop() {
	defer p.wg.Done()
	for {
		select {
		case <-p.stopCh:
			return
		case <-p.evictSig:
			p.reclaimDisk()
		}
	}
}

// reclaimDisk punches holes behind every Buffer's read head (keeping the
// BackWindow) until the pool is back under DiskLimit or there is nothing left
// to safely reclaim. Each pass snapshots the buffer set so it never holds the
// pool lock across a punch syscall.
func (p *Pool) reclaimDisk() {
	limit := p.diskLimit.Load()
	if limit <= 0 {
		return
	}
	for p.diskInUse.Load() > limit {
		p.mu.RLock()
		bufs := make([]*Buffer, 0, len(p.buffers))
		for b := range p.buffers {
			bufs = append(bufs, b)
		}
		p.mu.RUnlock()
		if len(bufs) == 0 {
			return
		}
		var reclaimed int64
		for _, b := range bufs {
			reclaimed += b.punchBehindWindow(p.backWindow)
			if p.diskInUse.Load() <= limit {
				return
			}
		}
		if reclaimed == 0 {
			// All remaining data is within the buffers' back-windows; nothing
			// can be reclaimed without disrupting active playback. Accept the
			// bounded overshoot and stop until the next signal.
			return
		}
	}
}
