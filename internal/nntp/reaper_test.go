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
