package nntp

import (
	"bufio"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// newPipeConnection builds a Connection backed by net.Pipe and starts a fake
// server goroutine. respond=true answers every DATE with 111; respond=false
// closes the server side immediately so pings fail.
func newPipeConnection(t *testing.T, respond bool) *Connection {
	t.Helper()
	clientSide, serverSide := net.Pipe()
	c := &Connection{
		conn:   clientSide,
		reader: bufio.NewReader(clientSide),
		writer: bufio.NewWriter(clientSide),
	}
	t.Cleanup(func() { _ = c.Close() })

	if !respond {
		_ = serverSide.Close()
		return c
	}

	go func() {
		defer serverSide.Close()
		buf := bufio.NewReader(serverSide)
		for {
			if _, err := buf.ReadString('\n'); err != nil {
				return
			}
			if _, err := serverSide.Write([]byte("111 20260720000000\r\n")); err != nil {
				return
			}
		}
	}()
	return c
}

func newReaperTestClient(pp *ProviderPool) *Client {
	return &Client{
		pools:          map[string]*ProviderPool{"test": pp},
		logger:         zerolog.Nop(),
		idleTimeout:    5 * time.Minute,
		staleThreshold: 60 * time.Second,
		pingInterval:   30 * time.Second,
		pingTimeout:    1500 * time.Millisecond,
		// Deadlines come off utils.Now(), which the cached clock only
		// refreshes every 500ms — a budget near that granularity is already
		// expired when it is set and times out instantly. Production uses
		// KeepalivePingTimeout.
		keepalivePing: 1500 * time.Millisecond,
	}
}

func newTestPool(max int) *ProviderPool {
	return &ProviderPool{
		conns:  make([]*connectionEntry, 0, max),
		slots:  make(chan struct{}, max),
		max:    max,
		config: config.UsenetProvider{Host: "test"},
	}
}

func poolEntry(pp *ProviderPool, conn *Connection, idleFor time.Duration) *connectionEntry {
	entry := acquireConnectionEntry(conn, pp.config, utils.Now().Add(-idleFor))
	pp.conns = append(pp.conns, entry)
	return entry
}

func TestReaperKeepsAndPingsIdleConnection(t *testing.T) {
	pp := newTestPool(4)
	conn := newPipeConnection(t, true)
	// Idle past pingInterval (30s) but well inside idleTimeout (5m).
	poolEntry(pp, conn, 40*time.Second)
	c := newReaperTestClient(pp)

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 1 {
		t.Fatalf("expected connection kept in pool, got %d entries", len(pp.conns))
	}
	entry := pp.conns[0]
	if entry.lastPing.IsZero() {
		t.Error("expected lastPing to be set after keepalive")
	}
	if entry.conn.IsClosed() {
		t.Error("expected connection to stay open after successful ping")
	}
	if len(pp.slots) != 0 {
		t.Errorf("expected all slots released, %d still held", len(pp.slots))
	}
}

func TestReaperSkipsRecentlyActiveConnection(t *testing.T) {
	pp := newTestPool(4)
	conn := newPipeConnection(t, true)
	entry := poolEntry(pp, conn, 5*time.Second) // fresher than pingInterval
	c := newReaperTestClient(pp)

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 1 || pp.conns[0] != entry {
		t.Fatal("expected untouched entry to remain pooled")
	}
	if !entry.lastPing.IsZero() {
		t.Error("expected no keepalive ping for recently used connection")
	}
}

func TestReaperClosesExpiredConnection(t *testing.T) {
	pp := newTestPool(4)
	conn := newPipeConnection(t, true)
	poolEntry(pp, conn, 6*time.Minute) // past idleTimeout
	c := newReaperTestClient(pp)

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 0 {
		t.Fatalf("expected expired connection removed, got %d entries", len(pp.conns))
	}
	if !conn.IsClosed() {
		t.Error("expected expired connection to be closed")
	}
}

func TestReaperClosesConnectionOnFailedPing(t *testing.T) {
	pp := newTestPool(4)
	conn := newPipeConnection(t, false) // server side closed: ping fails
	poolEntry(pp, conn, 40*time.Second)
	c := newReaperTestClient(pp)

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 0 {
		t.Fatalf("expected dead connection removed, got %d entries", len(pp.conns))
	}
	if !conn.IsClosed() {
		t.Error("expected dead connection to be closed")
	}
	if len(pp.slots) != 0 {
		t.Errorf("expected slot released after failed ping, %d still held", len(pp.slots))
	}
}

func TestReaperSkipsPingWhenPoolBusy(t *testing.T) {
	pp := newTestPool(1)
	pp.slots <- struct{}{} // all slots taken: pool fully busy
	conn := newPipeConnection(t, true)
	entry := poolEntry(pp, conn, 40*time.Second)
	c := newReaperTestClient(pp)

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 1 || pp.conns[0] != entry {
		t.Fatal("expected entry kept without ping when no slot is free")
	}
	if !entry.lastPing.IsZero() {
		t.Error("expected no ping when pool is busy")
	}
}

func TestNormalizeTimeoutsPingInterval(t *testing.T) {
	got := normalizeTimeouts(TimeoutConfig{})
	if got.PingInterval != 30*time.Second {
		t.Errorf("default PingInterval = %v, want 30s", got.PingInterval)
	}
	if got.IdleTimeout != 5*time.Minute {
		t.Errorf("default IdleTimeout = %v, want 5m", got.IdleTimeout)
	}

	// PingInterval must stay inside the idle window.
	got = normalizeTimeouts(TimeoutConfig{IdleTimeout: 20 * time.Second, PingInterval: time.Minute})
	if got.PingInterval != 10*time.Second {
		t.Errorf("clamped PingInterval = %v, want 10s", got.PingInterval)
	}
}

// TestReaperFlushesPoolWhenEveryPingTimesOut: when the path to a provider
// goes dark, one timed-out ping condemns the whole sweep. The rest of the
// batch is discarded unpinged instead of paying the ping budget each, and
// the idle pool is flushed — the rule checkout already applies.
func TestReaperFlushesPoolWhenEveryPingTimesOut(t *testing.T) {
	pp := newTestPool(16)
	c := newReaperTestClient(pp)

	// Enough entries to need several rounds through the 4 ping workers.
	silent := make([]*Connection, 12)
	for i := range silent {
		silent[i] = newSilentPipeConnection(t)
		poolEntry(pp, silent[i], 40*time.Second)
	}
	// Idle but not yet ping-due: only the flush can reach these.
	fresh := newPipeConnection(t, true)
	poolEntry(pp, fresh, 5*time.Second)

	start := time.Now()
	c.reapIdleConnections()
	elapsed := time.Since(start)

	// 12 sequential pings over 4 workers would be 3 × keepalivePing; the
	// short-circuit keeps it to the one round that discovered the outage.
	if elapsed > 2*c.keepalivePing {
		t.Errorf("sweep took %v, want under %v: batch did not short-circuit", elapsed, 2*c.keepalivePing)
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != 0 {
		t.Fatalf("expected idle pool flushed, got %d entries", len(pp.conns))
	}
	if !fresh.IsClosed() {
		t.Error("expected the flush to close the not-yet-due entry too")
	}
	if len(pp.slots) != 0 {
		t.Errorf("expected all slots released, %d still held", len(pp.slots))
	}
}

// TestReaperKeepsPoolWhenSomePingsAnswer: a timeout next to a live reply is
// one wedged session, not a dead path. The dead entry goes; the connections
// that answered stay pooled.
func TestReaperKeepsPoolWhenSomePingsAnswer(t *testing.T) {
	// max 16 so maxPing (max/4) covers the whole set in one batch.
	pp := newTestPool(16)
	c := newReaperTestClient(pp)

	// Dead entry first, so its timeout is the batch's last word rather than
	// its first: the live replies land in microseconds and must beat it.
	dead := newSilentPipeConnection(t)
	poolEntry(pp, dead, 40*time.Second)
	live := make([]*Connection, 3)
	for i := range live {
		live[i] = newPipeConnection(t, true)
		poolEntry(pp, live[i], 40*time.Second)
	}

	c.reapIdleConnections()

	pp.mu.Lock()
	defer pp.mu.Unlock()
	if len(pp.conns) != len(live) {
		t.Fatalf("expected %d live connections kept, got %d", len(live), len(pp.conns))
	}
	if !dead.IsClosed() {
		t.Error("expected the silent connection closed")
	}
	for i, conn := range live {
		if conn.IsClosed() {
			t.Errorf("live conn %d was closed: pool should not have been flushed", i)
		}
	}
	if len(pp.slots) != 0 {
		t.Errorf("expected all slots released, %d still held", len(pp.slots))
	}
}

func TestNormalizeTimeoutsKeepalivePing(t *testing.T) {
	got := normalizeTimeouts(TimeoutConfig{})
	if got.KeepalivePingTimeout != 5*time.Second {
		t.Errorf("default KeepalivePingTimeout = %v, want 5s", got.KeepalivePingTimeout)
	}
	if got.PingTimeout != 1500*time.Millisecond {
		t.Errorf("default PingTimeout = %v, want 1.5s", got.PingTimeout)
	}

	// A keepalive ping may not outlast the cadence it is issued at.
	got = normalizeTimeouts(TimeoutConfig{IdleTimeout: 20 * time.Second, KeepalivePingTimeout: time.Minute})
	if got.PingInterval != 10*time.Second {
		t.Fatalf("PingInterval = %v, want 10s", got.PingInterval)
	}
	if got.KeepalivePingTimeout != 10*time.Second {
		t.Errorf("clamped KeepalivePingTimeout = %v, want 10s", got.KeepalivePingTimeout)
	}
}
