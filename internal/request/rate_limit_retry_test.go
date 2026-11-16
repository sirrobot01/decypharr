package request

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type countingLimiter struct{ calls atomic.Int32 }

func (l *countingLimiter) Take() time.Time {
	l.calls.Add(1)
	return time.Now()
}

func TestRetriesEachSpendRateLimitToken(t *testing.T) {
	var wireCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		wireCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	limiter := &countingLimiter{}
	client := New(
		WithMaxRetries(1),
		WithRateLimiter(limiter),
		WithRetryableStatus(http.StatusBadGateway),
	)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, _ := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if got := wireCalls.Load(); got != 2 {
		t.Fatalf("wire calls = %d, want 2", got)
	}
	if got := limiter.calls.Load(); got != wireCalls.Load() {
		t.Fatalf("rate-limit tokens = %d, wire calls = %d", got, wireCalls.Load())
	}
}

func TestSuccessfulRequestSpendsOneRateLimitToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	limiter := &countingLimiter{}
	client := New(WithRateLimiter(limiter))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if got := limiter.calls.Load(); got != 1 {
		t.Fatalf("rate-limit tokens = %d, want 1", got)
	}
}
