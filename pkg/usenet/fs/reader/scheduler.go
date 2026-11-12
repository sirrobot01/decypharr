package reader

import (
	"sync"
)

type fetchPriority uint8

const (
	priorityPrefetch fetchPriority = iota
	priorityProbe
	priorityDemand
)

type scheduledFetch struct {
	run  func()
	drop func()
	seg  int // first segment of a prefetch task; orders it within its queue
}

// prefetchQueue holds one reader's pending read-ahead tasks.
type prefetchQueue struct {
	tasks []scheduledFetch
	// lead reports how many bytes of the file lie between the reader's
	// consumer and seg. A small lead means the consumer is close to a stall.
	lead       func(seg int) int64
	lastServed uint64
	removed    bool
}

// FetchScheduler bounds NNTP work across every reader sharing a client.
//
// Demand fetches (a read that is blocked now) run first, in arrival order.
// Read-ahead waits in one queue per reader. A free worker takes the next task
// of the reader whose consumer is closest to its next missing segment, so a
// stream near a stall is served before a stream with a full buffer. One
// reader alone can use every worker; several readers share them by need.
type FetchScheduler struct {
	mu sync.Mutex
	// demandWake wakes the demand-only worker; anyWake wakes the others.
	demandWake sync.Cond
	anyWake    sync.Cond
	demand     []scheduledFetch
	queues     []*prefetchQueue
	served     uint64
	closed     bool
	workers    int
	wg         sync.WaitGroup
}

// NewFetchScheduler starts workers matching the streaming connection budget.
// A zero worker scheduler is useful for state-only tests.
func NewFetchScheduler(workers int) *FetchScheduler {
	workers = max(workers, 0)
	s := &FetchScheduler{workers: workers}
	s.demandWake.L = &s.mu
	s.anyWake.L = &s.mu
	// With more than one worker, keep one for demand so a blocked read never
	// waits behind a full set of read-ahead fetches.
	for i := range workers {
		prefetch := workers == 1 || i > 0
		s.wg.Go(func() { s.worker(prefetch) })
	}
	return s
}

func (s *FetchScheduler) worker(prefetch bool) {
	for {
		task, ok := s.next(prefetch)
		if !ok {
			return
		}
		task.run()
	}
}

func (s *FetchScheduler) next(prefetch bool) (scheduledFetch, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.closed {
		if len(s.demand) > 0 {
			task := s.demand[0]
			s.demand[0] = scheduledFetch{}
			s.demand = s.demand[1:]
			return task, true
		}
		if prefetch {
			if q := s.mostUrgentLocked(); q != nil {
				task := q.tasks[0]
				q.tasks[0] = scheduledFetch{}
				q.tasks = q.tasks[1:]
				s.served++
				q.lastServed = s.served
				return task, true
			}
			s.anyWake.Wait()
		} else {
			s.demandWake.Wait()
		}
	}
	return scheduledFetch{}, false
}

// mostUrgentLocked returns the queue with the smallest lead. Ties go to the
// queue served least recently, so equal streams take turns.
func (s *FetchScheduler) mostUrgentLocked() *prefetchQueue {
	var best *prefetchQueue
	var bestLead int64
	for _, q := range s.queues {
		if len(q.tasks) == 0 {
			continue
		}
		lead := q.lead(q.tasks[0].seg)
		if best == nil || lead < bestLead || (lead == bestLead && q.lastServed < best.lastServed) {
			best, bestLead = q, lead
		}
	}
	return best
}

// submitDemand queues a fetch ahead of all read-ahead.
func (s *FetchScheduler) submitDemand(task scheduledFetch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.demand = append(s.demand, task)
	s.demandWake.Signal()
	s.anyWake.Signal()
	return true
}

// submitPrefetch queues a read-ahead fetch on q.
func (s *FetchScheduler) submitPrefetch(q *prefetchQueue, task scheduledFetch) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || q.removed {
		return false
	}
	q.tasks = append(q.tasks, task)
	s.anyWake.Signal()
	return true
}

// newQueue registers a read-ahead queue for one reader.
func (s *FetchScheduler) newQueue(lead func(seg int) int64) *prefetchQueue {
	q := &prefetchQueue{lead: lead}
	s.mu.Lock()
	s.queues = append(s.queues, q)
	s.mu.Unlock()
	return q
}

// clearQueue drops the tasks pending on q.
func (s *FetchScheduler) clearQueue(q *prefetchQueue) {
	s.mu.Lock()
	tasks := q.tasks
	q.tasks = nil
	s.mu.Unlock()
	dropAll(tasks)
}

// removeQueue unregisters q and drops its pending tasks.
func (s *FetchScheduler) removeQueue(q *prefetchQueue) {
	s.mu.Lock()
	for i, queued := range s.queues {
		if queued == q {
			s.queues[i] = s.queues[len(s.queues)-1]
			s.queues[len(s.queues)-1] = nil
			s.queues = s.queues[:len(s.queues)-1]
			break
		}
	}
	q.removed = true
	tasks := q.tasks
	q.tasks = nil
	s.mu.Unlock()
	dropAll(tasks)
}

// Close stops accepting work, waits for running fetches, and drops the rest.
// It is idempotent.
func (s *FetchScheduler) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.demandWake.Broadcast()
	s.anyWake.Broadcast()
	s.mu.Unlock()
	s.wg.Wait()

	s.mu.Lock()
	tasks := s.demand
	s.demand = nil
	for _, q := range s.queues {
		tasks = append(tasks, q.tasks...)
		q.tasks = nil
	}
	s.mu.Unlock()
	dropAll(tasks)
}

func dropAll(tasks []scheduledFetch) {
	for _, task := range tasks {
		if task.drop != nil {
			task.drop()
		}
	}
}
