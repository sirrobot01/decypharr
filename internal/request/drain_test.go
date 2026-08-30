package request

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// countConns issues 20 requests whose bodies are decoded only far enough to read
// the leading JSON value, leaving padBytes unread, and reports how many distinct
// connections the server saw.
func countConns(t *testing.T, padBytes int, closeBody func(resp *http.Response)) int64 {
	t.Helper()
	var conns int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true}`+"\n"+strings.Repeat("p", padBytes))
	}))
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			atomic.AddInt64(&conns, 1)
		}
	}
	srv.Start()
	defer srv.Close()

	c := srv.Client()
	for range 20 {
		resp, err := c.Get(srv.URL)
		if err != nil {
			t.Fatal(err)
		}
		var out struct{ OK bool }
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		closeBody(resp)
	}
	return atomic.LoadInt64(&conns)
}

func bareClose(resp *http.Response)  { _ = resp.Body.Close() }
func drainClose(resp *http.Response) { DrainAndClose(resp.Body) }

// net/http only reuses a connection whose body reached EOF. Closing a body with
// an unread remainder larger than the transport's buffered read makes it drop
// the connection, so every request redials.
func TestDrainAndCloseEnablesReuse(t *testing.T) {
	const pad = 32 << 10 // unread, but well under maxDrain

	if got := countConns(t, pad, bareClose); got != 20 {
		t.Fatalf("bare Close: expected a fresh connection per request, got %d", got)
	}
	if got := countConns(t, pad, drainClose); got != 1 {
		t.Fatalf("DrainAndClose: expected a single reused connection, got %d", got)
	}
}

// A remainder that fits the initial buffered read is already at EOF by the time
// the decoder stops, so draining changes nothing. This is the common case for
// small API responses and explains why the helper is not a universal win.
func TestSmallRemainderReusesWithoutDraining(t *testing.T) {
	if got := countConns(t, 64, bareClose); got != 1 {
		t.Fatalf("expected reuse without draining, got %d", got)
	}
}

// Past maxDrain the helper gives up rather than reading an unbounded payload,
// so the connection is dropped exactly as a bare Close would drop it. This is
// what keeps DrainAndClose safe to defer on an unexpectedly large response.
func TestDrainGivesUpPastLimit(t *testing.T) {
	if got := countConns(t, 4*maxDrain, drainClose); got != 20 {
		t.Fatalf("expected the oversized remainder to be abandoned, got %d", got)
	}
}
