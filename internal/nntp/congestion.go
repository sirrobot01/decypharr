package nntp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/internal/bandwidth"
)

const (
	// linkTick is one measurement period of the link limit.
	linkTick = 2 * time.Second
	// linkStartLimit is the first limit. It matches the read-ahead window a
	// new stream opens with. An idle link drifts back to it.
	linkStartLimit = 16
	// linkMinLimit keeps some throughput when the link is very slow.
	linkMinLimit = 4
	// linkQueueTarget is how far the first-byte delay may rise above the
	// empty-link delay before the limit drops. LEDBAT uses the same target.
	linkQueueTarget = 100 * time.Millisecond
	// linkGainPercent is the throughput a step up must reach, as a percent of
	// the throughput before it, to keep the step.
	linkGainPercent = 110
	// linkHold is how long the limit stays fixed after a step down, before
	// the next probe for more capacity. A blocked read ends the hold early.
	linkHold = 30 * time.Second
	// linkMinSamples is how many first-byte delays a period needs before the
	// delay can lower the limit.
	linkMinSamples = 3
	// linkBaseSlots and linkBaseSlot set the window for the empty-link delay:
	// the smallest delay seen in the last ten minutes.
	linkBaseSlots = 10
	linkBaseSlot  = time.Minute
)

// linkControl limits how many NNTP connections transfer at once, so that
// decypharr does not fill the user's internet link.
//
// Two signals lower the limit:
//   - Delay. Each BODY reports how long the first byte of its reply took. The
//     smallest delay in the last ten minutes is the delay of an empty link.
//     When the smallest delay of a period is more than linkQueueTarget above
//     it, the link is queueing replies, and the limit drops by a quarter.
//   - Throughput. When work waits on the limit, the limit steps up. If the
//     step does not add linkGainPercent throughput, the link is full, and the
//     limit goes back to the last step.
//
// After a drop the limit holds for linkHold, then probes up again. A blocked
// read ends the hold: a starving stream outweighs one flat measurement. When
// no work waits, the limit drifts down to what is in use, so a later burst
// cannot open every connection at once.
//
// Streaming demand (a blocked read) is never held by the limit, but its
// connections count against it. The download speed cap applies to all reads.
type linkControl struct {
	limit    atomic.Int32
	bytes    atomic.Int64 // body bytes received in this period
	limited  atomic.Bool  // work waited on the limit in this period
	demanded atomic.Bool  // a blocked read fetched in this period
	speed    atomic.Int64 // bytes per second in the last period

	mu         sync.Mutex
	maxLimit   int
	recent     int64 // smallest first-byte delay in this period; 0 = none
	lastRecent int64
	samples    int
	base       [linkBaseSlots]int64
	baseIdx    int
	baseAt     int64
	tickAt     int64
	// Probe state.
	slowStart bool
	settle    bool // skip one period after the limit changes
	prevLimit int
	prevBytes int64 // bytes in the period at prevLimit; 0 = no baseline
	holdUntil int64
}

// newLinkControl returns a link control for maxLimit connections.
func newLinkControl(maxLimit int) *linkControl {
	maxLimit = max(maxLimit, 1)
	l := &linkControl{maxLimit: maxLimit, slowStart: true}
	l.limit.Store(int32(min(linkStartLimit, maxLimit)))
	return l
}

// received records n body bytes and waits while they pass the download speed
// cap.
func (l *linkControl) received(n int) {
	l.bytes.Add(int64(n))
	_ = bandwidth.Wait(context.Background(), n)
}

// sample records the first-byte delay of one BODY reply.
func (l *linkControl) sample(delay int64) {
	l.mu.Lock()
	if l.recent == 0 || delay < l.recent {
		l.recent = delay
	}
	l.samples++
	l.mu.Unlock()
}

// baseLocked returns the smallest delay in the base window, or 0.
func (l *linkControl) baseLocked() int64 {
	var base int64
	for _, d := range l.base {
		if d > 0 && (base == 0 || d < base) {
			base = d
		}
	}
	return base
}

// tick closes one period and sets the limit for the next one. inUse is the
// number of connections held now.
func (l *linkControl) tick(now int64, inUse int) {
	bytes := l.bytes.Swap(0)
	limited := l.limited.Swap(false)
	demanded := l.demanded.Swap(false)

	l.mu.Lock()
	defer l.mu.Unlock()
	if period := now - l.tickAt; l.tickAt != 0 && period > 0 {
		l.speed.Store(bytes * int64(time.Second) / period)
	}
	l.tickAt = now
	recent, samples := l.recent, l.samples
	l.recent, l.samples = 0, 0
	if recent > 0 {
		l.lastRecent = recent
	}

	if now-l.baseAt >= int64(linkBaseSlot) {
		l.baseIdx = (l.baseIdx + 1) % linkBaseSlots
		l.base[l.baseIdx] = 0
		l.baseAt = now
	}
	if recent > 0 && (l.base[l.baseIdx] == 0 || recent < l.base[l.baseIdx]) {
		l.base[l.baseIdx] = recent
	}
	base := l.baseLocked()

	limit := int(l.limit.Load())
	next := limit
	switch {
	case samples >= linkMinSamples && recent-base > int64(linkQueueTarget):
		next = max(linkMinLimit, limit*3/4)
		l.slowStart = false
		l.prevBytes = 0
		l.holdUntil = now + int64(linkHold)
	case !limited:
		// The work fits under the limit. Drift down to what is in use.
		next = min(limit, max(linkStartLimit, inUse, limit*3/4))
		if next <= linkStartLimit {
			l.slowStart = true
		}
		l.prevBytes = 0
		l.settle = false
	case now < l.holdUntil && !demanded:
		l.prevBytes = 0
		l.settle = false
	case l.settle:
		// New connections are still starting. This period does not show
		// what the limit can do.
		l.settle = false
		return
	case l.prevBytes == 0 || bytes*100 >= l.prevBytes*linkGainPercent:
		l.holdUntil = 0
		l.prevLimit, l.prevBytes = limit, bytes
		if l.slowStart {
			next = min(l.maxLimit, limit*2)
		} else {
			next = min(l.maxLimit, limit+max(limit/4, 1))
		}
	default:
		// The step did not add throughput. The link is full.
		next = l.prevLimit
		l.slowStart = false
		l.prevBytes = 0
		l.holdUntil = now + int64(linkHold)
	}
	if next != limit {
		l.settle = true
		l.limit.Store(int32(next))
	}
}

// stats reports the limit and the signals it uses, for Client.Stats.
func (l *linkControl) stats() map[string]any {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]any{
		"limit":           l.limit.Load(),
		"max_limit":       l.maxLimit,
		"speed":           l.speed.Load(),
		"base_delay_ms":   float64(l.baseLocked()) / 1e6,
		"recent_delay_ms": float64(l.lastRecent) / 1e6,
		"slow_start":      l.slowStart,
		"holding":         nanotimeNow() < l.holdUntil,
	}
}

func (c *Client) linkLoop() {
	ticker := time.NewTicker(linkTick)
	defer ticker.Stop()
	for range ticker.C {
		if c.closed.Load() {
			return
		}
		c.link.tick(nanotimeNow(), c.slotsInUse())
	}
}

func (c *Client) slotsInUse() int {
	inUse := 0
	for _, pp := range c.orderedPools {
		inUse += len(pp.slots)
	}
	return inUse
}

// overLinkLimit reports whether the held slots exceed the link limit. It
// marks the period as limited when they do. A client without a link control
// has no limit.
func (c *Client) overLinkLimit() bool {
	if c.link == nil || c.slotsInUse() <= int(c.link.limit.Load()) {
		return false
	}
	c.link.limited.Store(true)
	return true
}
