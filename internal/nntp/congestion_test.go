package nntp

import (
	"testing"
	"time"
)

// runPeriod feeds one measurement period to l and closes it.
func runPeriod(l *linkControl, now *int64, bytes int64, delay time.Duration, limited bool) int {
	l.bytes.Add(bytes)
	l.limited.Store(limited)
	for range linkMinSamples {
		l.sample(int64(delay))
	}
	*now += int64(linkTick)
	l.tick(*now)
	return int(l.limit.Load())
}

func TestLinkControlDoublesWhileThroughputGrows(t *testing.T) {
	l := newLinkControl(100)
	var now int64
	if got := runPeriod(l, &now, 1000, 50*time.Millisecond, true); got != 32 {
		t.Fatalf("limit after first limited period = %d, want 32", got)
	}
	runPeriod(l, &now, 1000, 50*time.Millisecond, true) // settle
	if got := runPeriod(l, &now, 2000, 50*time.Millisecond, true); got != 64 {
		t.Fatalf("limit after throughput doubled = %d, want 64", got)
	}
}

func TestLinkControlReturnsToLastStepOnPlateau(t *testing.T) {
	l := newLinkControl(100)
	var now int64
	runPeriod(l, &now, 1000, 50*time.Millisecond, true) // 16 -> 32
	runPeriod(l, &now, 1000, 50*time.Millisecond, true) // settle
	if got := runPeriod(l, &now, 1050, 50*time.Millisecond, true); got != 16 {
		t.Fatalf("limit after a step with no gain = %d, want 16", got)
	}
	// The limit holds while work keeps waiting.
	runPeriod(l, &now, 1000, 50*time.Millisecond, true) // settle
	if got := runPeriod(l, &now, 1000, 50*time.Millisecond, true); got != 16 {
		t.Fatalf("limit during hold = %d, want 16", got)
	}
	// After the hold it probes with a quarter step, not a doubling.
	now += int64(linkHold)
	if got := runPeriod(l, &now, 1000, 50*time.Millisecond, true); got != 20 {
		t.Fatalf("limit after hold = %d, want 20", got)
	}
}

func TestLinkControlDropsWhenDelayRises(t *testing.T) {
	l := newLinkControl(100)
	l.limit.Store(80)
	var now int64
	runPeriod(l, &now, 1000, 200*time.Millisecond, false)
	if got := runPeriod(l, &now, 1000, 200*time.Millisecond+linkQueueTarget+time.Millisecond, false); got != 60 {
		t.Fatalf("limit after delay rose = %d, want 60", got)
	}
}

func TestLinkControlKeepsLimitWhenWorkFits(t *testing.T) {
	l := newLinkControl(100)
	var now int64
	for range 5 {
		if got := runPeriod(l, &now, 1000, 50*time.Millisecond, false); got != linkStartLimit {
			t.Fatalf("limit with no waiting work = %d, want %d", got, linkStartLimit)
		}
	}
}

func TestLinkControlNeverDropsBelowMinimum(t *testing.T) {
	l := newLinkControl(100)
	l.limit.Store(linkMinLimit)
	var now int64
	runPeriod(l, &now, 1000, 50*time.Millisecond, false)
	if got := runPeriod(l, &now, 1000, time.Second, false); got != linkMinLimit {
		t.Fatalf("limit = %d, want %d", got, linkMinLimit)
	}
}

func TestLinkLimitHoldsPrefetchButNotDemand(t *testing.T) {
	pp := newTestPool(4)
	c := newAcquireTestClient(pp)
	c.link = newLinkControl(4)
	c.link.limit.Store(1)

	if !c.tryAcquireSlot(pp, WorkloadStreamPrefetch) {
		t.Fatal("first prefetch slot refused under the limit")
	}
	if c.tryAcquireSlot(pp, WorkloadStreamPrefetch) {
		t.Fatal("prefetch passed the link limit")
	}
	if c.tryAcquireSlot(pp, WorkloadDownload) {
		t.Fatal("download passed the link limit")
	}
	if !c.link.limited.Load() {
		t.Fatal("refusal did not mark the period limited")
	}
	if !c.tryAcquireSlot(pp, WorkloadStreamDemand) {
		t.Fatal("demand held by the link limit")
	}
	if got := len(pp.slots); got != 2 {
		t.Fatalf("slots held = %d, want 2", got)
	}
}
