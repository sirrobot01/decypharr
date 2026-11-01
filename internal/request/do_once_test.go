package request

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestDoOnceDoesNotRetry(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("X-Test"); got != "set" {
			t.Errorf("X-Test header = %q, want client headers applied", got)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	client := New(
		WithMaxRetries(3),
		WithRetryableStatus(http.StatusTooManyRequests),
		WithHeaders(map[string]string{"X-Test": "set"}),
	)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.DoOnce(req)
	if err != nil {
		t.Fatal(err)
	}
	DrainAndClose(resp.Body)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 returned to the caller", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want exactly 1", got)
	}
}
