package nntp

import (
	"context"
	"errors"
	"sync"
)

// repairDefaultPercent is used when cfg.Repair.NNTPConnectionPercent is unset.
const repairDefaultPercent = 20

// repairPoolMinWorkers is the floor for the worker count. Even with no
// repair-mode budget configured (BatchStat called by non-repair paths) we
// keep a small pool around so the caller doesn't have to branch.
const repairPoolMinWorkers = 2

// RepairPool is a process-wide worker pool for BatchStat chunks. It
// replaces the previous RepairBank counting-semaphore design where each
// BatchStat call spawned its own pool sized to bank.Capacity — under M
// concurrent BatchStat calls that produced M × bank.Capacity goroutines
// all racing for the same bank.Capacity tokens, with most blocked.
//
// The new shape: one shared pool sized to bank.Capacity, BatchStat
// submits chunks onto the shared task channel, workers pull them off in
// FIFO order. Total goroutines is exactly len(workers), period, no
// matter how many BatchStat calls run concurrently.
type RepairPool struct {
	tasks   chan repairTask
	workers int
	wg      sync.WaitGroup
	quit    chan struct{}
	once    sync.Once
}

// repairTask describes one chunk of work the pool will execute. done is
// invoked exactly once — either with the per-chunk results (on success or
// connection error from batchStatAcrossProviders) or with a non-nil err
// when the caller's context expires before a worker picks the task up.
type repairTask struct {
	ctx    context.Context
	msgIDs []string
	done   func(results []StatResult, err error)
}

// errRepairPoolClosed is returned by Submit after Stop has been called.
var errRepairPoolClosed = errors.New("repair pool closed")

// newRepairPool starts a worker pool sized to `percent` of the client's
// total provider connections, with a small floor so non-repair BatchStat
// callers still have a couple of workers available.
func (c *Client) newRepairPool(percent int) *RepairPool {
	if c == nil {
		return nil
	}
	if percent <= 0 {
		percent = repairDefaultPercent
	}
	if percent > 100 {
		percent = 100
	}
	total := c.TotalConnections()
	capacity := repairPoolMinWorkers
	if total > 0 {
		sized := (total*percent + 99) / 100
		if sized > capacity {
			capacity = sized
		}
		if capacity > total {
			capacity = total
		}
	}

	p := &RepairPool{
		// Buffered = capacity so a burst of chunk submissions from a single
		// BatchStat doesn't immediately block on the first send. Once the
		// buffer fills, the submitter naturally backpressures.
		tasks:   make(chan repairTask, capacity),
		workers: capacity,
		quit:    make(chan struct{}),
	}
	p.wg.Add(capacity)
	for i := 0; i < capacity; i++ {
		go p.worker(c)
	}
	return p
}

// pickStatBatchSize picks the per-chunk batch size for a BatchStat call.
//
// Default is the ceiling (statBatchSize). For small inputs vs. the
// available worker pool, we shrink the chunk size so a single call can
// keep the whole pool busy — otherwise a 100-ID call against a 30-worker
// pool would only fan out to 2 chunks (chunks=ceil(100/50)) and leave
// 28 workers idle. Floors at minSize so the per-chunk overhead doesn't
// dominate when input is tiny.
//
// overcommit > 1 means we aim for more chunks than workers so a worker
// finishing a fast chunk has a queued one waiting rather than idling
// while the rest of the pool finishes slower chunks.
func pickStatBatchSize(totalIDs, workers, ceilSize, minSize int) int {
	if workers <= 0 || totalIDs <= 0 {
		return ceilSize
	}
	const overcommit = 3
	want := (totalIDs + workers*overcommit - 1) / (workers * overcommit)
	if want >= ceilSize {
		return ceilSize
	}
	if want < minSize {
		return minSize
	}
	return want
}

// TotalConnections is the sum of MaxConnections across configured providers.
func (c *Client) TotalConnections() int {
	if c == nil {
		return 0
	}
	total := 0
	for _, p := range c.providers {
		if p.MaxConnections > 0 {
			total += p.MaxConnections
		}
	}
	return total
}

// Capacity returns the number of concurrent workers in the pool.
func (p *RepairPool) Capacity() int {
	if p == nil {
		return 0
	}
	return p.workers
}

// Submit hands a chunk to the pool. done is invoked on a worker goroutine
// exactly once when the chunk has been processed (or rejected). Returns
// an error and does NOT call done when the caller's ctx expires before a
// worker takes the task, or when the pool has been stopped.
func (p *RepairPool) Submit(ctx context.Context, msgIDs []string, done func([]StatResult, error)) error {
	if p == nil {
		return errRepairPoolClosed
	}
	task := repairTask{ctx: ctx, msgIDs: msgIDs, done: done}
	// quit takes priority: once Stop closes it, refuse new work even if
	// the buffered tasks channel still has room.
	select {
	case <-p.quit:
		return errRepairPoolClosed
	default:
	}
	select {
	case p.tasks <- task:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-p.quit:
		return errRepairPoolClosed
	}
}

// Stop signals workers to drain and exit. Blocks until all in-flight
// chunks finish. Pending tasks still in the channel are abandoned —
// their done callbacks never fire, which is acceptable because Stop is
// only called during Client teardown when callers' contexts are already
// being cancelled upstream.
func (p *RepairPool) Stop() {
	if p == nil {
		return
	}
	p.once.Do(func() { close(p.quit) })
	p.wg.Wait()
}

// worker pulls chunks until the pool stops. Each task is processed by
// calling batchStatAcrossProviders directly; bank-token accounting is no
// longer needed because the pool's worker count IS the concurrency cap.
func (p *RepairPool) worker(c *Client) {
	defer p.wg.Done()
	for {
		select {
		case <-p.quit:
			return
		case t := <-p.tasks:
			if t.done == nil {
				continue
			}
			// Caller may have cancelled while we were waiting. Surface
			// it as the task's error without doing the work.
			if err := t.ctx.Err(); err != nil {
				t.done(nil, err)
				continue
			}
			results, err := c.batchStatAcrossProviders(t.ctx, t.msgIDs)
			t.done(results, err)
		}
	}
}
