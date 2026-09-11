package nntp

import (
	"bufio"
	"context"
	"net"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// closedPort returns a loopback port with nothing listening, so dials to it
// are refused immediately.
func closedPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestDialCooldownReroutesAroundDeadProvider: after the primary's first dial
// failure, subsequent acquisitions must go straight to the healthy secondary
// without re-dialing the dead primary.
func TestDialCooldownReroutesAroundDeadProvider(t *testing.T) {
	dead := config.UsenetProvider{Host: "127.0.0.1", Port: closedPort(t), MaxConnections: 2, Priority: 1}
	deadPool := &ProviderPool{
		conns:  make([]*connectionEntry, 0, dead.MaxConnections),
		slots:  make(chan struct{}, dead.MaxConnections),
		max:    dead.MaxConnections,
		config: dead,
	}

	healthy := config.UsenetProvider{Host: "healthy", Port: 119, MaxConnections: 2, Priority: 2}
	healthyPool := newTestPool(2)
	healthyPool.config = healthy
	conn := newPipeConnection(t, true)
	poolEntry(healthyPool, conn, 0)

	c := &Client{
		pools:          map[string]*ProviderPool{dead.ID(): deadPool, healthy.ID(): healthyPool},
		orderedPools:   []*ProviderPool{deadPool, healthyPool},
		providers:      []config.UsenetProvider{dead, healthy},
		logger:         zerolog.Nop(),
		idleTimeout:    time.Hour,
		staleThreshold: time.Hour,
		pingInterval:   time.Hour,
	}

	got, prov, err := c.getAnyAvailableConnection(context.Background(), providerExclusions{})
	if err != nil {
		t.Fatalf("first acquisition failed: %v", err)
	}
	if prov.Host != healthy.Host || got != conn {
		t.Fatalf("expected healthy provider's pooled conn, got provider %s", prov.Host)
	}
	if !deadPool.inDialCooldown() {
		t.Fatal("expected dead provider to enter dial cooldown after failure")
	}
	c.put(got, prov)

	// Subsequent acquisitions must not dial the dead provider again.
	for range 5 {
		got, prov, err = c.getAnyAvailableConnection(context.Background(), providerExclusions{})
		if err != nil {
			t.Fatalf("acquisition during cooldown failed: %v", err)
		}
		if prov.Host != healthy.Host {
			t.Fatalf("expected healthy provider during cooldown, got %s", prov.Host)
		}
		c.put(got, prov)
	}
	if streak := deadPool.dialFailStreak.Load(); streak != 1 {
		t.Fatalf("dead provider was re-dialed during cooldown: streak=%d", streak)
	}
}

// TestDialCooldownKeepsSingleProviderFailFast: with no alternative provider,
// a cooldown must not delay or swallow dial errors — acquisitions keep
// dialing and keep failing fast.
func TestDialCooldownKeepsSingleProviderFailFast(t *testing.T) {
	dead := config.UsenetProvider{Host: "127.0.0.1", Port: closedPort(t), MaxConnections: 2}
	deadPool := &ProviderPool{
		conns:  make([]*connectionEntry, 0, dead.MaxConnections),
		slots:  make(chan struct{}, dead.MaxConnections),
		max:    dead.MaxConnections,
		config: dead,
	}
	c := &Client{
		pools:          map[string]*ProviderPool{dead.ID(): deadPool},
		orderedPools:   []*ProviderPool{deadPool},
		providers:      []config.UsenetProvider{dead},
		logger:         zerolog.Nop(),
		idleTimeout:    time.Hour,
		staleThreshold: time.Hour,
		pingInterval:   time.Hour,
	}

	for i := range 3 {
		start := time.Now()
		_, _, err := c.getAnyAvailableConnection(context.Background(), providerExclusions{})
		if err == nil {
			t.Fatalf("attempt %d: expected dial error", i)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("attempt %d: cooldown delayed fail-fast dial: %v", i, elapsed)
		}
	}
	// Each acquisition dials twice — the phase-1 scan and the phase-2
	// last-resort scan — matching the pre-cooldown behavior for a lone
	// provider.
	if streak := deadPool.dialFailStreak.Load(); streak != 6 {
		t.Fatalf("expected 6 dial attempts, streak=%d", streak)
	}
}

// TestHandoffFIFO: with the pool saturated and two parked acquirers,
// released slots must go to waiters in arrival order.
func TestHandoffFIFO(t *testing.T) {
	pp := newTestPool(1)
	c := newAcquireTestClient(pp)
	pp.slots <- struct{}{} // saturate

	acquire := func() chan *Connection {
		ch := make(chan *Connection, 1)
		go func() {
			got, prov, err := c.getAnyAvailableConnection(context.Background(), providerExclusions{})
			if err != nil {
				t.Error(err)
				close(ch)
				return
			}
			c.put(got, prov) // pass the slot on for the next waiter
			ch <- got
		}()
		return ch
	}

	chA := acquire()
	time.Sleep(100 * time.Millisecond) // A parks before B arrives
	chB := acquire()
	time.Sleep(100 * time.Millisecond)

	// Return a healthy connection: the slot must reach A first, then B via
	// A's put.
	conn := newPipeConnection(t, true)
	pp.mu.Lock()
	pp.conns = append(pp.conns, acquireConnectionEntry(conn, pp.config, utils.Now()))
	pp.mu.Unlock()
	c.releaseSlot(pp)

	for i, ch := range []chan *Connection{chA, chB} {
		select {
		case got := <-ch:
			if got != conn {
				t.Fatalf("waiter %d: expected the pooled connection", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("waiter %d never acquired", i)
		}
	}
}

// TestDeregisterDrainsPendingHandoff: a waiter that exits after a releaser
// already handed it a slot must re-release that slot, not leak it.
func TestDeregisterDrainsPendingHandoff(t *testing.T) {
	pp := newTestPool(1)
	c := newAcquireTestClient(pp)
	pp.slots <- struct{}{} // slot held; hasIdle=false but no cooldown → handoff allowed

	w := newSlotWaiter([]*ProviderPool{pp})
	c.register(w)
	if !c.handoffSlot(pp) {
		t.Fatal("expected handoff to the registered waiter")
	}
	// Waiter bails (e.g. ctx cancelled) without consuming the handoff.
	c.deregister(w)

	if len(pp.slots) != 0 {
		t.Fatalf("slot leaked after deregister: %d held", len(pp.slots))
	}
	c.waitMu.Lock()
	defer c.waitMu.Unlock()
	if len(c.waiters) != 0 {
		t.Fatalf("waiter queue not empty: %d", len(c.waiters))
	}
}

// newSilentPipeConnection builds a Connection whose peer stays open but
// never reads or answers: a ping's write blocks until its deadline and
// surfaces as a net timeout — the flush trigger.
func newSilentPipeConnection(t *testing.T) *Connection {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	c := &Connection{
		conn:   clientSide,
		reader: bufio.NewReader(clientSide),
		writer: bufio.NewWriter(clientSide),
	}
	t.Cleanup(func() { _ = c.Close(); _ = serverSide.Close() })
	return c
}

// TestSameHostProvidersGetDistinctPools: a dual-account setup (same host,
// different usernames) must produce one pool per account — host keying used
// to silently merge them, dropping one account's connection cap — and a
// returned connection must land in its own account's pool.
func TestSameHostProvidersGetDistinctPools(t *testing.T) {
	providers := []config.UsenetProvider{
		{Host: "news.example.com", Port: 563, Username: "alice", MaxConnections: 3, Priority: 1},
		{Host: "news.example.com", Port: 563, Username: "bob", MaxConnections: 7, Priority: 2},
	}
	pools, orderedPools := buildPools(providers)
	c := &Client{
		pools:          pools,
		orderedPools:   orderedPools,
		providers:      providers,
		logger:         zerolog.Nop(),
		idleTimeout:    time.Hour,
		staleThreshold: time.Hour,
		pingInterval:   time.Hour,
	}

	if len(c.pools) != 2 {
		t.Fatalf("expected 2 pools for dual-account setup, got %d", len(c.pools))
	}
	for _, p := range providers {
		pp, ok := c.pools[p.ID()]
		if !ok {
			t.Fatalf("no pool for %s", p.ID())
		}
		if pp.max != p.MaxConnections {
			t.Fatalf("pool %s max = %d, want %d", p.ID(), pp.max, p.MaxConnections)
		}
	}

	bob := providers[1]
	conn := newPipeConnection(t, true)
	bobPool := c.pools[bob.ID()]
	conn.pool = bobPool
	bobPool.slots <- struct{}{} // the slot put() releases
	c.put(conn, bob)
	if !bobPool.hasIdle() {
		t.Fatal("connection did not return to its own pool")
	}
	if c.pools[providers[0].ID()].hasIdle() {
		t.Fatal("connection leaked into the same-host sibling pool")
	}
}

// TestSpeedTestRespectsPoolAccounting: the speed test must acquire through
// the pool (it used to dial directly, exceeding max_connections by one) and
// return the warm connection afterwards with the slot released.
func TestSpeedTestRespectsPoolAccounting(t *testing.T) {
	pp := newTestPool(1)
	c := newAcquireTestClient(pp)
	c.speedTestResults = xsync.NewMap[string, SpeedTestResult]()
	conn := newPipeConnection(t, true) // answers DATE, so ping succeeds
	poolEntry(pp, conn, 0)

	res := c.SpeedTest(context.Background(), pp.config.ID(), "")
	if res.Error != "" {
		t.Fatalf("speed test failed: %s", res.Error)
	}
	if len(pp.slots) != 0 {
		t.Fatalf("slot leaked: %d held after test", len(pp.slots))
	}
	if !pp.hasIdle() {
		t.Fatal("connection not returned to the pool")
	}
	if _, ok := c.speedTestResults.Load(pp.config.ID()); !ok {
		t.Fatal("result not stored under the canonical provider ID")
	}
}

// TestCheckoutFlushesPoolOnPingTimeout: when the freshest stale entry's
// verify ping times out at checkout, the older idle entries are flushed in
// one sweep. Without the flush, checkout would pop the next (responsive)
// entry and hand it out — so a dial error plus every connection closed
// proves the flush ran.
func TestCheckoutFlushesPoolOnPingTimeout(t *testing.T) {
	pp := newTestPool(4)
	c := newAcquireTestClient(pp)
	// Point dials at a closed port so the post-flush dial fails fast.
	pp.config = config.UsenetProvider{Host: "127.0.0.1", Port: closedPort(t)}

	// Two responsive-but-stale entries below, one silent entry on top of
	// the LIFO stack (popped first). Idle past staleThreshold (60s) so the
	// verify ping runs, but inside idleTimeout (5m) — beyond it the idle
	// path closes every entry before any ping and the flush never runs.
	lower := []*Connection{newPipeConnection(t, true), newPipeConnection(t, true)}
	for _, conn := range lower {
		poolEntry(pp, conn, 90*time.Second)
	}
	silent := newSilentPipeConnection(t)
	poolEntry(pp, silent, 90*time.Second)

	pp.slots <- struct{}{} // hold the slot the checkout owns
	_, err := c.getOrCreateFromPool(context.Background(), pp, pp.config, true)
	if err == nil {
		t.Fatal("expected checkout to fail: flush should skip the responsive entries and the dial is refused")
	}
	if !silent.IsClosed() {
		t.Fatal("silent connection not closed")
	}
	for i, conn := range lower {
		if !conn.IsClosed() {
			t.Fatalf("lower conn %d not closed by flush", i)
		}
	}
	if pp.hasIdle() {
		t.Fatal("expected idle pool flushed")
	}
}
