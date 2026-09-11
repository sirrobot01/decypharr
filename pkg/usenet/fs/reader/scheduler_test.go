package reader

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFetchSchedulerRunsDemandBeforeQueuedPrefetch(t *testing.T) {
	scheduler := NewFetchScheduler(1)
	t.Cleanup(scheduler.Close)

	started := make(chan struct{})
	release := make(chan struct{})
	order := make(chan string, 3)
	if !scheduler.submit(context.Background(), priorityPrefetch, func() {
		close(started)
		<-release
		order <- "active-prefetch"
	}, nil) {
		t.Fatal("submit active prefetch")
	}
	<-started
	if !scheduler.submit(context.Background(), priorityPrefetch, func() { order <- "queued-prefetch" }, nil) {
		t.Fatal("submit queued prefetch")
	}
	if !scheduler.submit(context.Background(), priorityDemand, func() { order <- "demand" }, nil) {
		t.Fatal("submit demand")
	}
	close(release)

	got := []string{<-order, <-order, <-order}
	want := []string{"active-prefetch", "demand", "queued-prefetch"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("execution order = %v, want %v", got, want)
		}
	}
}

func TestFetchSchedulerBoundsConcurrency(t *testing.T) {
	const workers = 3
	scheduler := NewFetchScheduler(workers)
	t.Cleanup(scheduler.Close)

	var active, peak atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		if !scheduler.submit(context.Background(), priorityDemand, func() {
			n := active.Add(1)
			for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			wg.Done()
		}, func() { wg.Done() }) {
			t.Fatal("submit demand")
		}
	}
	for range workers {
		<-started
	}
	close(release)
	wg.Wait()
	if got := peak.Load(); got != workers {
		t.Fatalf("peak concurrency = %d, want %d", got, workers)
	}
}

func TestFetchSchedulerCloseSettlesAcceptedTasks(t *testing.T) {
	scheduler := NewFetchScheduler(2)
	var accepted, settled atomic.Int64
	start := make(chan struct{})
	var submitters sync.WaitGroup
	for range 256 {
		submitters.Add(1)
		go func() {
			defer submitters.Done()
			<-start
			if scheduler.submit(context.Background(), priorityPrefetch,
				func() { settled.Add(1) }, func() { settled.Add(1) }) {
				accepted.Add(1)
			}
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() {
		scheduler.Close()
		close(done)
	}()
	submitters.Wait()
	<-done
	if got, want := settled.Load(), accepted.Load(); got != want {
		t.Fatalf("settled tasks = %d, accepted = %d", got, want)
	}
}
