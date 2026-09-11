package reader

import (
	"context"
	"sync"
	"sync/atomic"
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
}

// FetchScheduler bounds NNTP work across every reader sharing a client.
// Foreground reads use the demand queue; speculative work uses prefetch.
type FetchScheduler struct {
	demand   chan scheduledFetch
	prefetch chan scheduledFetch
	stop     chan struct{}
	workers  int
	wg       sync.WaitGroup
	closed   atomic.Bool
	submitMu sync.Mutex
	submitWg sync.WaitGroup
}

// NewFetchScheduler starts workers matching the real provider concurrency.
// A zero worker scheduler is useful for state-only tests.
func NewFetchScheduler(workers int) *FetchScheduler {
	if workers < 0 {
		workers = 0
	}
	s := &FetchScheduler{
		demand:   make(chan scheduledFetch, max(64, workers*16)),
		prefetch: make(chan scheduledFetch, max(256, workers*32)),
		stop:     make(chan struct{}),
		workers:  workers,
	}
	reserved := 0
	if workers > 1 {
		reserved = 1
	}
	for i := range workers {
		s.wg.Add(1)
		go s.worker(i >= reserved)
	}
	return s
}

func (s *FetchScheduler) worker(allowPrefetch bool) {
	defer s.wg.Done()
	for {
		if !allowPrefetch {
			select {
			case <-s.stop:
				return
			case task := <-s.demand:
				task.run()
			}
			continue
		}

		select {
		case <-s.stop:
			return
		default:
		}

		// Prefer demand already waiting before accepting speculative work.
		select {
		case task := <-s.demand:
			task.run()
			continue
		default:
		}

		select {
		case <-s.stop:
			return
		case task := <-s.demand:
			task.run()
		case task := <-s.prefetch:
			task.run()
		}
	}
}

func (s *FetchScheduler) submit(ctx context.Context, priority fetchPriority, run, drop func()) bool {
	if s == nil || run == nil {
		return false
	}
	s.submitMu.Lock()
	if s.closed.Load() {
		s.submitMu.Unlock()
		return false
	}
	s.submitWg.Add(1)
	s.submitMu.Unlock()
	defer s.submitWg.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	task := scheduledFetch{run: run, drop: drop}
	if priority == priorityPrefetch {
		select {
		case s.prefetch <- task:
			return true
		case <-ctx.Done():
			return false
		case <-s.stop:
			return false
		default:
			return false
		}
	}
	if priority == priorityProbe {
		select {
		case s.demand <- task:
			return true
		case <-ctx.Done():
			return false
		case <-s.stop:
			return false
		default:
			return false
		}
	}
	select {
	case s.demand <- task:
		return true
	case <-ctx.Done():
		return false
	case <-s.stop:
		return false
	}
}

// Close stops accepting work and waits for active fetches. It is idempotent.
func (s *FetchScheduler) Close() {
	if s == nil {
		return
	}
	s.submitMu.Lock()
	if !s.closed.CompareAndSwap(false, true) {
		s.submitMu.Unlock()
		return
	}
	s.submitMu.Unlock()
	s.submitWg.Wait()
	close(s.stop)
	s.wg.Wait()
	for {
		select {
		case task := <-s.demand:
			if task.drop != nil {
				task.drop()
			}
		case task := <-s.prefetch:
			if task.drop != nil {
				task.drop()
			}
		default:
			return
		}
	}
}
