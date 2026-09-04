package nntp

import (
	"fmt"
	"slices"
)

// Workload describes why a caller needs an NNTP connection. Lower values
// are admitted first when a provider is saturated.
type Workload uint8

const (
	// WorkloadStream is latency-sensitive playback traffic, including the
	// bounded read-ahead needed to keep an active stream fed.
	WorkloadStream Workload = iota
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
	case WorkloadStream:
		return "stream"
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
	prev     *slotWaiter
	next     *slotWaiter
	queued   bool
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
		if workload != WorkloadStream && pp.higherPriorityWaiting(workload) {
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
	for workload := WorkloadStream; workload < workloadCount; workload++ {
		for w := c.waiters[workload].head; w != nil; w = w.next {
			if !slices.Contains(w.pools, pp) {
				continue
			}
			c.removeWaiterLocked(w)
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
	for workload := WorkloadStream; workload < workloadCount; workload++ {
		waiting[workload] = c.waiters[workload].len
	}
	return waiting
}

func (c *Client) providerWaiting(pp *ProviderPool) map[string]int {
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	return map[string]int{
		WorkloadStream.String():     pp.waiting[WorkloadStream],
		WorkloadDownload.String():   pp.waiting[WorkloadDownload],
		WorkloadBackground.String(): pp.waiting[WorkloadBackground],
	}
}
