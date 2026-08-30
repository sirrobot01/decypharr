package nntp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

func newAcquireTestClient(pp *ProviderPool) *Client {
	return &Client{
		pools:          map[string]*ProviderPool{"test": pp},
		providers:      []config.UsenetProvider{{Host: "test"}},
		logger:         zerolog.Nop(),
		slotFreed:      make(chan struct{}, 1),
		idleTimeout:    5 * time.Minute,
		staleThreshold: 60 * time.Second,
		pingInterval:   30 * time.Second,
	}
}

// TestWaitForConnectionUnblocksOnRelease: with the pool saturated, an
// acquirer parks; releasing a slot (returning a healthy pooled connection)
// wakes exactly that acquirer, with no goroutine fan-out.
func TestWaitForConnectionUnblocksOnRelease(t *testing.T) {
	pp := newTestPool(1)
	c := newAcquireTestClient(pp)

	// Saturate the pool: the one slot is taken by a fictitious user.
	pp.slots <- struct{}{}

	conn := newPipeConnection(t, true)

	type result struct {
		conn *Connection
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		got, _, err := c.getAnyAvailableConnection(context.Background(), providerExclusions{})
		resCh <- result{got, err}
	}()

	// The acquirer must be blocked while the pool is saturated.
	select {
	case r := <-resCh:
		t.Fatalf("acquisition returned while pool was saturated: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}

	// The fictitious user returns a healthy connection to the pool.
	pp.mu.Lock()
	pp.conns = append(pp.conns, acquireConnectionEntry(conn, pp.config, utils.Now()))
	pp.mu.Unlock()
	c.releaseSlot(pp)

	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("acquisition failed after release: %v", r.err)
		}
		if r.conn != conn {
			t.Fatal("expected the pooled connection to be handed out")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquirer never woke after slot release")
	}
}

// TestWaitForConnectionCtxCancelUnblocks: a parked acquirer honors context
// cancellation promptly.
func TestWaitForConnectionCtxCancelUnblocks(t *testing.T) {
	pp := newTestPool(1)
	c := newAcquireTestClient(pp)
	pp.slots <- struct{}{} // saturate

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, _, err := c.getAnyAvailableConnection(ctx, providerExclusions{})
		errCh <- err
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquirer did not honor cancellation")
	}
}

// TestBodyJanitorClosesStalledConn: an armed body copy that stops
// progressing gets its connection closed; a disarmed connection is left
// alone.
func TestBodyJanitorClosesStalledConn(t *testing.T) {
	stalledConn := newPipeConnection(t, true)
	idleConn := newPipeConnection(t, true)

	bodyIdleJanitor.add(stalledConn)
	bodyIdleJanitor.add(idleConn)
	defer bodyIdleJanitor.remove(stalledConn)
	defer bodyIdleJanitor.remove(idleConn)

	// Armed with a tiny idle deadline and stale progress.
	stalledConn.idleNS.Store(int64(time.Millisecond))
	stalledConn.lastProgressNS.Store(nanotimeNow() - int64(time.Second))
	// Disarmed (no body copy in flight) — must be left alone regardless of
	// how old its progress mark is.
	idleConn.idleNS.Store(0)
	idleConn.lastProgressNS.Store(nanotimeNow() - int64(time.Hour))

	bodyIdleJanitor.sweep()

	// The stalled connection's socket must be closed (a read fails
	// immediately instead of blocking).
	if _, err := stalledConn.conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("stalled connection was not closed by the janitor")
	}
	// The disarmed connection's socket must still be open: a read blocks
	// (nothing to deliver) rather than failing.
	readErr := make(chan error, 1)
	go func() {
		_, err := idleConn.conn.Read(make([]byte, 1))
		readErr <- err
	}()
	select {
	case err := <-readErr:
		t.Fatalf("disarmed connection was closed by the janitor: %v", err)
	case <-time.After(100 * time.Millisecond):
		// still open and blocked — correct; Cleanup closes it.
	}
}
