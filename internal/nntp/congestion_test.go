package nntp

import (
	"testing"
	"time"
)

type period struct {
	bytes    int64
	delay    time.Duration
	limited  bool
	demanded bool
	inUse    int
}

// run feeds one measurement period to l, closes it, and returns the limit.
func (p period) run(l *linkControl, now *int64) int {
	l.bytes.Add(p.bytes)
	l.limited.Store(p.limited)
	l.demanded.Store(p.demanded)
	for range linkMinSamples {
		l.sample(int64(p.delay))
	}
	*now += int64(linkTick)
	l.tick(*now, p.inUse)
	return int(l.limit.Load())
}

var (
	waiting = period{bytes: 1000, delay: 50 * time.Millisecond, limited: true}
	idle    = period{bytes: 1000, delay: 50 * time.Millisecond}
)

func TestLinkControlDoublesWhileThroughputGrows(t *testing.T) {
	l := newLinkControl(100, 0)
	var now int64
	if got := waiting.run(l, &now); got != 32 {
		t.Fatalf("limit after first limited period = %d, want 32", got)
	}
	waiting.run(l, &now) // settle
	faster := waiting
	faster.bytes = 2000
	if got := faster.run(l, &now); got != 64 {
		t.Fatalf("limit after throughput doubled = %d, want 64", got)
	}
}

func TestLinkControlReturnsToLastStepOnPlateau(t *testing.T) {
	l := newLinkControl(100, 0)
	var now int64
	waiting.run(l, &now) // 16 -> 32
	waiting.run(l, &now) // settle
	flat := waiting
	flat.bytes = 1050
	if got := flat.run(l, &now); got != 16 {
		t.Fatalf("limit after a step with no gain = %d, want 16", got)
	}
	waiting.run(l, &now)
	if got := waiting.run(l, &now); got != 16 {
		t.Fatalf("limit during hold = %d, want 16", got)
	}
	// After the hold it probes with a quarter step, not a doubling.
	now += int64(linkHold)
	if got := waiting.run(l, &now); got != 20 {
		t.Fatalf("limit after hold = %d, want 20", got)
	}
}

func TestLinkControlBlockedReadEndsHold(t *testing.T) {
	l := newLinkControl(100, 0)
	var now int64
	waiting.run(l, &now) // 16 -> 32
	waiting.run(l, &now) // settle
	flat := waiting
	flat.bytes = 1000
	flat.run(l, &now) // back to 16, hold
	waiting.run(l, &now)
	blocked := waiting
	blocked.demanded = true
	if got := blocked.run(l, &now); got != 20 {
		t.Fatalf("limit with a blocked read during hold = %d, want 20", got)
	}
}

func TestLinkControlDropsWhenDelayRises(t *testing.T) {
	l := newLinkControl(100, 0)
	l.limit.Store(80)
	var now int64
	steady := idle
	steady.delay, steady.inUse = 200*time.Millisecond, 80
	steady.run(l, &now)
	queued := steady
	queued.delay += linkQueueTarget + time.Millisecond
	if got := queued.run(l, &now); got != 60 {
		t.Fatalf("limit after delay rose = %d, want 60", got)
	}
}

func TestLinkControlDriftsDownWhenIdle(t *testing.T) {
	l := newLinkControl(100, 0)
	l.limit.Store(80)
	l.slowStart = false
	var now int64
	busy := idle
	busy.inUse = 70
	if got := busy.run(l, &now); got != 70 {
		t.Fatalf("limit with 70 in use = %d, want 70", got)
	}
	for range 10 {
		idle.run(l, &now)
	}
	if got := int(l.limit.Load()); got != linkStartLimit {
		t.Fatalf("limit after idle = %d, want %d", got, linkStartLimit)
	}
	if !l.slowStart {
		t.Fatal("idle link did not return to slow start")
	}
}

func TestLinkControlNeverDropsBelowMinimum(t *testing.T) {
	l := newLinkControl(100, 0)
	l.limit.Store(linkMinLimit)
	var now int64
	idle.run(l, &now)
	slow := idle
	slow.delay = time.Second
	if got := slow.run(l, &now); got != linkMinLimit {
		t.Fatalf("limit = %d, want %d", got, linkMinLimit)
	}
}

func TestLinkControlReportsSpeed(t *testing.T) {
	l := newLinkControl(100, 0)
	var now int64
	idle.run(l, &now) // first period has no start time
	fast := idle
	fast.bytes = 4 << 20
	fast.run(l, &now)
	if got, want := l.speed.Load(), int64(2<<20); got != want {
		t.Fatalf("speed = %d, want %d", got, want)
	}
}

func TestLinkControlCapsSpeed(t *testing.T) {
	const speed = 1 << 20
	l := newLinkControl(100, speed)
	start := time.Now()
	for range 6 {
		l.received(256 << 10) // 1.5MB in all; the burst covers 256KB
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("1.5MB at a 1MB/s cap took %s, want at least 1s", elapsed)
	}
}

func TestLinkLimitHoldsPrefetchButNotDemand(t *testing.T) {
	pp := newTestPool(4)
	c := newAcquireTestClient(pp)
	c.link = newLinkControl(4, 0)
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
