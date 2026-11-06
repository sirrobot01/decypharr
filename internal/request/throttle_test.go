package request

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

type countingLimiter struct{ takes atomic.Int64 }

func (l *countingLimiter) Take() time.Time {
	l.takes.Add(1)
	return time.Now()
}

func throttledServer(t *testing.T, retryAfter int, calls *atomic.Int64) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)
	return server
}

func get(t *testing.T, client *Client, url string) error {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err == nil {
		DrainAndClose(resp.Body)
	}
	return err
}

func TestLongRetryAfterParksEveryClientOfTheKey(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	var calls atomic.Int64
	server := throttledServer(t, 300, &calls)
	throttle := new(Throttle)
	first := New(WithMaxRetries(3), WithThrottle(throttle))
	second := New(WithMaxRetries(3), WithThrottle(throttle))

	begin := time.Now()
	err := get(t, first, server.URL)
	throttled, ok := errors.AsType[*ThrottledError](err)
	if !ok {
		t.Fatalf("error = %v, want ThrottledError", err)
	}
	if throttled.RetryAfter != 300*time.Second {
		t.Fatalf("RetryAfter = %s", throttled.RetryAfter)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, a lockout must not be retried", calls.Load())
	}

	err = get(t, second, server.URL)
	if _, ok := errors.AsType[*ThrottledError](err); !ok {
		t.Fatalf("second client error = %v, want ThrottledError", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, the cooldown must stop the second client", calls.Load())
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("throttled requests took %s, want fail fast", elapsed)
	}
}

func TestRetriesPassTheRateLimiterAndKeepTheStatus(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	var calls atomic.Int64
	server := throttledServer(t, 0, &calls)
	limiter := new(countingLimiter)
	client := New(WithMaxRetries(1), WithRateLimiter(limiter))

	err := get(t, client, server.URL)
	if _, ok := errors.AsType[*ThrottledError](err); !ok {
		t.Fatalf("error = %v, want ThrottledError", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
	if limiter.takes.Load() != calls.Load() {
		t.Fatalf("limiter charged %d times for %d requests", limiter.takes.Load(), calls.Load())
	}
}

func TestTripNeverShortensACooldown(t *testing.T) {
	throttle := new(Throttle)
	throttle.Trip(time.Hour)
	if throttle.Trip(time.Minute) {
		t.Fatal("a shorter trip moved the deadline")
	}
	if got := throttle.Remaining(); got < 59*time.Minute {
		t.Fatalf("Remaining() = %s", got)
	}
	throttle.Trip(24 * time.Hour)
	if got := throttle.Remaining(); got > maxThrottle {
		t.Fatalf("Remaining() = %s, want at most %s", got, maxThrottle)
	}
}
