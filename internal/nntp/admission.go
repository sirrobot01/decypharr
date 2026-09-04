package nntp

import (
	"fmt"
	"slices"
	"sync/atomic"
)

// Workload describes why a caller needs an NNTP connection. Lower values
// are admitted first when a provider is saturated.
type Workload uint8

const (
	// WorkloadStreamDemand is latency-sensitive playback traffic required to
	// satisfy a reader that is currently blocked.
	WorkloadStreamDemand Workload = iota
	// WorkloadStreamPrefetch is bounded speculative read-ahead for an active
	// stream. It stays ahead of bulk work without delaying current demand.
	WorkloadStreamPrefetch
	// WorkloadDownload is throughput-oriented foreground work: complete NZB
	// downloads, imports, and archive parsing.
	WorkloadDownload
	// WorkloadBackground is maintenance work such as availability scans,
	// repairs, and speed tests.
	WorkloadBackground

	workloadCount
)

func (w Workload) valid() bool {
	return w < workloadCount
}

func (w Workload) String() string {
	switch w {
	case WorkloadStreamDemand:
		return "stream_demand"
	case WorkloadStreamPrefetch:
		return "stream_prefetch"
	case WorkloadDownload:
		return "download"
	case WorkloadBackground:
		return "background"
	default:
		return fmt.Sprintf("workload(%d)", w)
	}
}

// slotWaiter is one parked acquirer waiting for a slot on any compatible
// provider. Waiters are FIFO within a workload class.
type slotWaiter struct {
	workload Workload
	pools    []*ProviderPool
	handoff  chan *ProviderPool
	started  int64
	prev     *slotWaiter
	next     *slotWaiter
	queued   bool
}

type admissionOutcome uint8

const (
	admissionSucceeded admissionOutcome = iota
	admissionCanceled
	admissionFailed
)

type admissionMetrics struct {
	queued      atomic.Uint64
	admitted    atomic.Uint64
	canceled    atomic.Uint64
	failed      atomic.Uint64
	handoffs    atomic.Uint64
	waitTotalNS atomic.Uint64
	waitMaxNS   atomic.Uint64
}

type admissionSnapshot struct {
	queued      uint64
	admitted    uint64
	canceled    uint64
	failed      uint64
	handoffs    uint64
	waitTotalNS uint64
	waitMaxNS   uint64
}

type waiterQueue struct {
	head *slotWaiter
	tail *slotWaiter
	len  int
}

func newSlotWaiter(workload Workload, pools []*ProviderPool) *slotWaiter {
	return &slotWaiter{
		workload: workload,
		pools:    pools,
		handoff:  make(chan *ProviderPool, 1),
	}
}

func (c *Client) newQueuedWaiter(workload Workload, pools []*ProviderPool) *slotWaiter {
	w := newSlotWaiter(workload, pools)
	w.started = nanotimeNow()
	c.admission[workload].queued.Add(1)
	return w
}

func (c *Client) finishWait(w *slotWaiter, outcome admissionOutcome) {
	if w.started == 0 {
		return
	}
	metrics := &c.admission[w.workload]
	switch outcome {
	case admissionSucceeded:
		metrics.admitted.Add(1)
	case admissionCanceled:
		metrics.canceled.Add(1)
	case admissionFailed:
		metrics.failed.Add(1)
	}
	waitNS := uint64(max(nanotimeNow()-w.started, 0))
	metrics.waitTotalNS.Add(waitNS)
	for previous := metrics.waitMaxNS.Load(); waitNS > previous; previous = metrics.waitMaxNS.Load() {
		if metrics.waitMaxNS.CompareAndSwap(previous, waitNS) {
			break
		}
	}
	w.started = 0
}

func (m *admissionMetrics) snapshot() admissionSnapshot {
	return admissionSnapshot{
		queued:      m.queued.Load(),
		admitted:    m.admitted.Load(),
		canceled:    m.canceled.Load(),
		failed:      m.failed.Load(),
		handoffs:    m.handoffs.Load(),
		waitTotalNS: m.waitTotalNS.Load(),
		waitMaxNS:   m.waitMaxNS.Load(),
	}
}

func (s admissionSnapshot) stats(waiting int) map[string]any {
	meanWaitMS := 0.0
	completed := s.admitted + s.canceled + s.failed
	if completed != 0 {
		meanWaitMS = float64(s.waitTotalNS) / float64(completed) / 1e6
	}
	return map[string]any{
		"waiting":        waiting,
		"queued_total":   s.queued,
		"admitted_total": s.admitted,
		"canceled_total": s.canceled,
		"failed_total":   s.failed,
		"handoffs_total": s.handoffs,
		"wait_mean_ms":   meanWaitMS,
		"wait_max_ms":    float64(s.waitMaxNS) / 1e6,
	}
}

// register queues w before its final availability scan. The per-provider
// counters keep lower-priority fast paths from barging ahead without putting
// the uncontended stream path behind a mutex.
func (c *Client) register(w *slotWaiter) {
	c.waitMu.Lock()
	queue := &c.waiters[w.workload]
	w.prev = queue.tail
	w.next = nil
	w.queued = true
	if queue.tail == nil {
		queue.head = w
	} else {
		queue.tail.next = w
	}
	queue.tail = w
	queue.len++
	for _, pp := range w.pools {
		pp.waiting[w.workload]++
		if pp.waiting[w.workload] == 1 {
			pp.waitingMask.Or(1 << w.workload)
		}
	}
	c.waitMu.Unlock()
}

func (c *Client) removeWaiterLocked(w *slotWaiter) bool {
	if !w.queued {
		return false
	}
	queue := &c.waiters[w.workload]
	if w.prev == nil {
		queue.head = w.next
	} else {
		w.prev.next = w.next
	}
	if w.next == nil {
		queue.tail = w.prev
	} else {
		w.next.prev = w.prev
	}
	queue.len--
	w.prev = nil
	w.next = nil
	w.queued = false
	for _, pp := range w.pools {
		pp.waiting[w.workload]--
		if pp.waiting[w.workload] == 0 {
			pp.waitingMask.And(^(uint32(1) << w.workload))
		}
	}
	return true
}

// deregister removes w from the admission queue. If a releaser won the race
// and already assigned a slot, pass that slot on rather than leaking it.
func (c *Client) deregister(w *slotWaiter) {
	c.waitMu.Lock()
	found := c.removeWaiterLocked(w)
	c.waitMu.Unlock()
	if !found {
		c.releaseSlot(<-w.handoff)
	}
}

// higherPriorityWaiting reports whether admitting workload on pp would barge
// ahead of latency-sensitive work. Same-class callers remain approximately
// FIFO through direct handoff, while their uncontended path stays lock-free.
func (pp *ProviderPool) higherPriorityWaiting(workload Workload) bool {
	higherPriorityMask := uint32(1<<workload) - 1
	return pp.waitingMask.Load()&higherPriorityMask != 0
}

// tryAcquireSlot takes a provider semaphore slot without blocking. After a
// lower-priority caller succeeds, it checks whether a higher-priority waiter
// already registered. If so, it immediately hands over the slot and reports
// failure. This post-acquisition check defines a clean ordering boundary and
// keeps the latency-sensitive stream path to the original semaphore send.
func (c *Client) tryAcquireSlot(pp *ProviderPool, workload Workload) bool {
	select {
	case pp.slots <- struct{}{}:
		if workload != WorkloadStreamDemand && pp.higherPriorityWaiting(workload) {
			c.releaseSlot(pp)
			return false
		}
		return true
	default:
		return false
	}
}

// releaseSlot frees one held slot, preferring a direct handoff to the oldest
// compatible waiter in the highest-priority non-empty workload class.
func (c *Client) releaseSlot(pp *ProviderPool) {
	if c.handoffSlot(pp) {
		return
	}
	<-pp.slots
}

func (c *Client) handoffSlot(pp *ProviderPool) bool {
	if !pp.hasIdle() && pp.inDialCooldown() {
		return false
	}
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	for workload := WorkloadStreamDemand; workload < workloadCount; workload++ {
		for w := c.waiters[workload].head; w != nil; w = w.next {
			if !slices.Contains(w.pools, pp) {
				continue
			}
			c.removeWaiterLocked(w)
			c.admission[w.workload].handoffs.Add(1)
			w.handoff <- pp
			return true
		}
	}
	return false
}

func (c *Client) waitingByWorkload() [workloadCount]int {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	var waiting [workloadCount]int
	for workload := WorkloadStreamDemand; workload < workloadCount; workload++ {
		waiting[workload] = c.waiters[workload].len
	}
	return waiting
}

func (c *Client) providerWaiting(pp *ProviderPool) map[string]int {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return map[string]int{
		WorkloadStreamDemand.String():   pp.waiting[WorkloadStreamDemand],
		WorkloadStreamPrefetch.String(): pp.waiting[WorkloadStreamPrefetch],
		WorkloadDownload.String():       pp.waiting[WorkloadDownload],
		WorkloadBackground.String():     pp.waiting[WorkloadBackground],
	}
}
