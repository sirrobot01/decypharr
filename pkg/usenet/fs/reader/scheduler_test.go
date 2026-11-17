package reader

import (
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func zeroLead(int) time.Duration { return 0 }

// blockWorker occupies the scheduler's only worker until the returned func
// is called, so later submissions queue up.
func blockWorker(t *testing.T, s *FetchScheduler) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	if !s.submitDemand(scheduledFetch{run: func() { close(started); <-release }}) {
		t.Fatal("submit blocker")
	}
	<-started
	return func() { close(release) }
}

func TestFetchSchedulerRunsDemandBeforeQueuedPrefetch(t *testing.T) {
	s := NewFetchScheduler(1)
	t.Cleanup(s.Close)
	q := s.newQueue(zeroLead)

	release := blockWorker(t, s)
	order := make(chan string, 2)
	if !s.submitPrefetch(q, scheduledFetch{run: func() { order <- "prefetch" }}) {
		t.Fatal("submit prefetch")
	}
	if !s.submitDemand(scheduledFetch{run: func() { order <- "demand" }}) {
		t.Fatal("submit demand")
	}
	release()

	if got := []string{<-order, <-order}; !slices.Equal(got, []string{"demand", "prefetch"}) {
		t.Fatalf("execution order = %v, want [demand prefetch]", got)
	}
}

func TestFetchSchedulerServesSmallestLeadFirst(t *testing.T) {
	s := NewFetchScheduler(1)
	t.Cleanup(s.Close)
	// The "full" reader has a minute buffered ahead; the "starving" reader a second.
	full := s.newQueue(func(seg int) time.Duration { return time.Minute + time.Duration(seg) })
	starving := s.newQueue(func(seg int) time.Duration { return time.Second + time.Duration(seg) })

	release := blockWorker(t, s)
	order := make(chan string, 4)
	for seg := range 2 {
		s.submitPrefetch(full, scheduledFetch{run: func() { order <- "full" }, seg: seg})
	}
	for seg := range 2 {
		s.submitPrefetch(starving, scheduledFetch{run: func() { order <- "starving" }, seg: seg})
	}
	release()

	got := []string{<-order, <-order, <-order, <-order}
	if want := []string{"starving", "starving", "full", "full"}; !slices.Equal(got, want) {
		t.Fatalf("execution order = %v, want %v", got, want)
	}
}

func TestFetchSchedulerAlternatesEqualLeads(t *testing.T) {
	s := NewFetchScheduler(1)
	t.Cleanup(s.Close)
	a := s.newQueue(zeroLead)
	b := s.newQueue(zeroLead)

	release := blockWorker(t, s)
	order := make(chan string, 6)
	for range 3 {
		s.submitPrefetch(a, scheduledFetch{run: func() { order <- "a" }})
		s.submitPrefetch(b, scheduledFetch{run: func() { order <- "b" }})
	}
	release()

	got := make([]string, 6)
	for i := range got {
		got[i] = <-order
	}
	for i := 1; i < len(got); i++ {
		if got[i] == got[i-1] {
			t.Fatalf("execution order = %v, want queues to alternate", got)
		}
	}
}

func TestFetchSchedulerBoundsConcurrency(t *testing.T) {
	const workers = 3
	s := NewFetchScheduler(workers)
	t.Cleanup(s.Close)

	var active, peak atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		task := scheduledFetch{
			run: func() {
				n := active.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				started <- struct{}{}
				<-release
				active.Add(-1)
				wg.Done()
			},
			drop: wg.Done,
		}
		if !s.submitDemand(task) {
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

func TestFetchSchedulerKeepsAllQueuedPrefetch(t *testing.T) {
	s := NewFetchScheduler(1)
	t.Cleanup(s.Close)
	q := s.newQueue(zeroLead)

	release := blockWorker(t, s)
	const tasks = 4096
	var ran sync.WaitGroup
	for seg := range tasks {
		ran.Add(1)
		if !s.submitPrefetch(q, scheduledFetch{run: ran.Done, seg: seg}) {
			t.Fatalf("prefetch %d rejected", seg)
		}
	}
	release()
	ran.Wait()
}

func TestFetchSchedulerRemoveQueueDropsPending(t *testing.T) {
	s := NewFetchScheduler(1)
	t.Cleanup(s.Close)
	q := s.newQueue(zeroLead)

	release := blockWorker(t, s)
	defer release()
	var dropped atomic.Int32
	for range 3 {
		s.submitPrefetch(q, scheduledFetch{run: func() { t.Error("removed task ran") }, drop: func() { dropped.Add(1) }})
	}
	s.removeQueue(q)
	if got := dropped.Load(); got != 3 {
		t.Fatalf("dropped = %d, want 3", got)
	}
	if s.submitPrefetch(q, scheduledFetch{run: func() {}}) {
		t.Fatal("submit to removed queue succeeded")
	}
}

func TestFetchSchedulerCloseSettlesAcceptedTasks(t *testing.T) {
	s := NewFetchScheduler(2)
	q := s.newQueue(zeroLead)
	var accepted, settled atomic.Int64
	start := make(chan struct{})
	var submitters sync.WaitGroup
	for i := range 256 {
		submitters.Go(func() {
			<-start
			task := scheduledFetch{run: func() { settled.Add(1) }, drop: func() { settled.Add(1) }}
			var ok bool
			if i%2 == 0 {
				ok = s.submitPrefetch(q, task)
			} else {
				ok = s.submitDemand(task)
			}
			if ok {
				accepted.Add(1)
			}
		})
	}
	close(start)
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	submitters.Wait()
	<-done
	if got, want := settled.Load(), accepted.Load(); got != want {
		t.Fatalf("settled tasks = %d, accepted = %d", got, want)
	}
}
