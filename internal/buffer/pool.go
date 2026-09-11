package buffer

import (
	"sync"
	"sync/atomic"
)

// Pool enforces shared RAM and sparse-disk budgets across its Buffers.
type Pool struct {
	name        string
	memBudget   atomic.Int64 // RAM ceiling across Buffers; 0 = unlimited
	diskLimit   atomic.Int64 // on-disk present bytes ceiling; 0 = unlimited
	backWindow  int64        // bytes retained behind a read head before punching
	reclaimDisk func(int64) int64

	memInUse  atomic.Int64 // sum of resident block RAM across Buffers
	memDemand atomic.Int64 // sum of MemorySize across Buffers
	diskInUse atomic.Int64 // sum of on-disk present bytes across Buffers

	mu      sync.RWMutex
	buffers map[*Buffer]struct{}

	// diskPressureMu serializes disk reservations with pressure reclamation.
	// A reservation is charged before its write, so physical bytes plus
	// in-flight writes can never collectively cross diskLimit.
	diskPressureMu sync.Mutex

	evictSig    chan struct{}
	memEvictSig chan struct{}
	stopCh      chan struct{}
	wg          sync.WaitGroup
	closed      atomic.Bool

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
	// a write would exceed it, the pool reclaims space before admitting it.
	// 0 = no disk backstop.
	DiskLimit int64

	// InitialDiskUsage accounts persistent bytes that predate this Pool. Their
	// Buffers must set InitialRangesPreaccounted when reopened.
	InitialDiskUsage int64

	// ReclaimDisk synchronously removes persistent bytes not owned by an open
	// Buffer. It receives the minimum number of bytes needed and returns the
	// number actually removed. It must not call back into this Pool.
	ReclaimDisk func(int64) int64

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
	if cfg.InitialDiskUsage < 0 {
		cfg.InitialDiskUsage = 0
	}
	if cfg.BackWindow < 0 {
		cfg.BackWindow = 0
	}
	p := &Pool{
		name:        cfg.Name,
		backWindow:  cfg.BackWindow,
		reclaimDisk: cfg.ReclaimDisk,
		buffers:     make(map[*Buffer]struct{}),
		evictSig:    make(chan struct{}, 1),
		memEvictSig: make(chan struct{}, 1),
		stopCh:      make(chan struct{}),
	}
	p.memBudget.Store(cfg.MemoryBudget)
	p.diskLimit.Store(cfg.DiskLimit)
	p.diskInUse.Store(cfg.InitialDiskUsage)
	if cfg.DiskLimit > 0 {
		p.wg.Go(p.diskEvictLoop)
	}
	if cfg.MemoryBudget > 0 {
		p.wg.Go(p.memEvictLoop)
	}
	if cfg.DiskLimit > 0 && cfg.InitialDiskUsage > cfg.DiskLimit {
		p.signalDiskEvict()
	}
	return p
}

// NewBuffer creates a Buffer bound to this pool. Its RAM and disk usage count
// against the pool's budgets.
func (p *Pool) NewBuffer(cfg Config) (*Buffer, error) {
	if p.closed.Load() {
		return nil, ErrClosed
	}
	b, err := newBuffer(p, cfg)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed.Load() {
		p.mu.Unlock()
		_ = b.Close()
		return nil, ErrClosed
	}
	p.buffers[b] = struct{}{}
	p.mu.Unlock()
	if !b.immutableDisk {
		p.memDemand.Add(b.maxBytes)
	}
	return b, nil
}

// remove unregisters a Buffer from the pool. Called from Buffer.Close.
func (p *Pool) remove(b *Buffer) {
	p.mu.Lock()
	_, known := p.buffers[b]
	delete(p.buffers, b)
	p.mu.Unlock()
	if known && !b.immutableDisk {
		p.memDemand.Add(-b.maxBytes)
	}
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
	if p.diskLimit.Load() > 0 || p.memBudget.Load() > 0 {
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

// shareFor divides a constrained budget in proportion to each Buffer's ask.
func (p *Pool) shareFor(b *Buffer) int64 {
	budget := p.memBudget.Load()
	if budget <= 0 {
		return 0
	}
	demand := p.memDemand.Load()
	if demand <= b.maxBytes {
		// Sole claimant (or accounting not yet settled): it may have the whole
		// budget, but not more than it — without the clamp one stream asking
		// for more than the budget was never trimmed at all.
		return min(b.maxBytes, budget)
	}
	share := int64(float64(budget) * (float64(b.maxBytes) / float64(demand)))
	return min(max(share, int64(blockSize)), b.maxBytes)
}

func (p *Pool) addBlock() {
	if v := p.memInUse.Add(int64(blockSize)); p.overMemBudget(v) {
		p.signalMemEvict()
	}
}

func (p *Pool) overMemBudget(inUse int64) bool {
	b := p.memBudget.Load()
	return b > 0 && inUse > b
}

func (p *Pool) signalMemEvict() {
	select {
	case p.memEvictSig <- struct{}{}:
	default:
	}
}

func (p *Pool) memEvictLoop() {
	for {
		select {
		case <-p.stopCh:
			return
		case <-p.memEvictSig:
			p.reclaimMemory()
		}
	}
}

// reclaimMemory takes RAM back from the Buffers holding more than their fair
// share. Reclaiming here rather than in the write path is the point: a
// Buffer only evicts inline when *it* is allocating, so under pool pressure
// the actively-streaming Buffer paid for the idle ones — which is exactly
// backwards. This worker walks every Buffer instead, so a paused stream or a
// played-out RAR volume gives its window back and the stream still feeding a
// player keeps its own.
func (p *Pool) reclaimMemory() {
	for pass := 0; pass < 4 && p.overMemBudget(p.memInUse.Load()); pass++ {
		p.mu.RLock()
		bufs := make([]*Buffer, 0, len(p.buffers))
		for b := range p.buffers {
			bufs = append(bufs, b)
		}
		p.mu.RUnlock()

		var reclaimed int64
		for _, b := range bufs {
			share := p.shareFor(b)
			if share <= 0 {
				continue
			}
			reclaimed += b.trimTo(share)
			if !p.overMemBudget(p.memInUse.Load()) {
				return
			}
		}
		if reclaimed == 0 {
			// Everyone is already at or under their share (or every block
			// is pinned by an in-flight read). Accept the bounded overshoot
			// rather than evicting bytes a reader is about to copy out.
			return
		}
	}
}

func (p *Pool) dropBytes(n int64) {
	if n > 0 {
		p.memInUse.Add(-n)
	}
}

// --- Disk accounting + backstop ---------------------------------------------

// addDisk records bytes that are already physically present. New writes must
// use reserveDisk before touching the file; this path is for initial ranges
// that were not pre-accounted and for restoring accounting after a failed
// reclaim operation.
func (p *Pool) addDisk(n int64) {
	if n <= 0 {
		return
	}
	v := p.diskInUse.Add(n)
	if limit := p.diskLimit.Load(); limit > 0 && v > limit {
		p.signalDiskEvict()
	}
}

// reserveDisk reserves physical capacity before a write. Reservations and
// synchronous pressure reclamation are serialized so concurrent writers
// cannot all observe the same remaining capacity.
func (p *Pool) reserveDisk(n int64) error {
	if n <= 0 {
		return nil
	}
	limit := p.diskLimit.Load()
	if limit <= 0 {
		p.diskInUse.Add(n)
		return nil
	}
	if n > limit {
		return ErrDiskLimit
	}

	p.diskPressureMu.Lock()
	defer p.diskPressureMu.Unlock()

	target := limit - n
	p.reclaimDiskTo(target)
	if used := p.diskInUse.Load(); used > target && p.reclaimDisk != nil {
		p.subDisk(p.reclaimDisk(used - target))
	}
	if p.diskInUse.Load() > target {
		return ErrDiskLimit
	}
	p.diskInUse.Add(n)
	return nil
}

// reclaimForWrite retries pressure reclamation outside a Buffer lock. A
// writer calls this after a reservation was denied, allowing the worker to
// reclaim history from that same Buffer before the write is attempted again.
func (p *Pool) reclaimForWrite(n int64) bool {
	limit := p.diskLimit.Load()
	if limit <= 0 {
		return true
	}
	n = min(max(n, 0), limit)
	target := limit - n

	p.diskPressureMu.Lock()
	defer p.diskPressureMu.Unlock()
	p.reclaimDiskTo(target)
	if used := p.diskInUse.Load(); used > target && p.reclaimDisk != nil {
		p.subDisk(p.reclaimDisk(used - target))
	}
	return p.diskInUse.Load() <= target
}

// subDisk records on-disk bytes that are no longer present (punched, discarded,
// or a closed Buffer's residual).
func (p *Pool) subDisk(n int64) {
	for n > 0 {
		used := p.diskInUse.Load()
		if used <= 0 {
			return
		}
		drop := min(n, used)
		if p.diskInUse.CompareAndSwap(used, used-drop) {
			return
		}
	}
}

// ReleaseDisk records persistent bytes removed by the owner after their
// Buffer was closed.
func (p *Pool) ReleaseDisk(n int64) {
	p.subDisk(n)
}

func (p *Pool) signalDiskEvict() {
	select {
	case p.evictSig <- struct{}{}:
	default:
	}
}

func (p *Pool) diskEvictLoop() {
	for {
		select {
		case <-p.stopCh:
			return
		case <-p.evictSig:
			p.diskPressureMu.Lock()
			p.reclaimDiskTo(p.diskLimit.Load())
			p.diskPressureMu.Unlock()
		}
	}
}

// reclaimDisk punches holes behind every Buffer's read head (keeping the
// BackWindow) until the pool is back under DiskLimit or there is nothing left
// to safely reclaim. Each pass snapshots the buffer set so it never holds the
// pool lock across a punch syscall.
func (p *Pool) reclaimDiskTo(target int64) {
	if target < 0 {
		return
	}
	for p.diskInUse.Load() > target {
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
			if p.diskInUse.Load() <= target {
				return
			}
		}
		if reclaimed == 0 {
			// All remaining data is within the buffers' back-windows; nothing
			// can be reclaimed without disrupting active playback. Stop until
			// a read head advances or another write retries admission.
			return
		}
	}
}
