package torbox

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"go.uber.org/ratelimit"
)

// countingLimiter records how many requests were charged to it, so a test can
// see which bucket a client actually spends.
type countingLimiter struct {
	takes atomic.Int64
}

func (l *countingLimiter) Take() time.Time {
	l.takes.Add(1)
	return time.Now()
}

// spendBothLanes issues one request on the list client and one on the submit
// client, so each lane charges whichever limiter it was wired to.
func spendBothLanes(t *testing.T, dc config.Debrid, limits map[string]ratelimit.Limiter) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	tb, err := New(dc, limits)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tb.Host = server.URL

	for _, client := range []*struct {
		name string
		c    func() error
	}{
		{"list", func() error {
			var res TorrentsListResponse
			_, err := tb.doGetWithClient(context.Background(), tb.client, "/api/torrents/mylist", nil, &res)
			return err
		}},
		{"submit", func() error {
			var res TorrentsListResponse
			_, err := tb.doGetWithClient(context.Background(), tb.submitClient, "/api/torrents/mylist", nil, &res)
			return err
		}},
	} {
		if err := client.c(); err != nil {
			t.Fatalf("%s lane request error = %v", client.name, err)
		}
	}
}

// TorBox counts its 300 req/min against the API key, so both lanes must spend
// one bucket. Two buckets would let decypharr emit twice the configured limit
// and produce exactly the 429 storms the limiter is there to prevent.
func TestLanesShareOneLimiterWhenDownloadKeyIsTheMainKey(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	main := &countingLimiter{}
	download := &countingLimiter{}
	dc := config.Debrid{
		Name:            "torbox",
		APIKey:          "key-1",
		DownloadAPIKeys: []string{"key-1"},
	}

	spendBothLanes(t, dc, map[string]ratelimit.Limiter{"main": main, "download": download})

	if got := main.takes.Load(); got != 2 {
		t.Errorf("main limiter takes = %d, want 2 (both lanes share one key's budget)", got)
	}
	if got := download.takes.Load(); got != 0 {
		t.Errorf("download limiter takes = %d, want 0 (a second bucket doubles the key's rate)", got)
	}
}

// A genuinely separate download key has its own budget at TorBox, so splitting
// the buckets is correct and must not be collapsed.
func TestLanesKeepSeparateLimitersForDistinctDownloadKey(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	main := &countingLimiter{}
	download := &countingLimiter{}
	dc := config.Debrid{
		Name:            "torbox",
		APIKey:          "key-1",
		DownloadAPIKeys: []string{"key-2"},
	}

	spendBothLanes(t, dc, map[string]ratelimit.Limiter{"main": main, "download": download})

	if got := main.takes.Load(); got != 1 {
		t.Errorf("main limiter takes = %d, want 1", got)
	}
	if got := download.takes.Load(); got != 1 {
		t.Errorf("download limiter takes = %d, want 1 (distinct key has its own budget)", got)
	}
}

func TestOnlyUsesKey(t *testing.T) {
	tests := []struct {
		name         string
		downloadKeys []string
		want         bool
	}{
		{"defaulted to the main key", []string{"key-1"}, true},
		{"empty list", nil, true},
		{"blank entries ignored", []string{"", "key-1"}, true},
		{"distinct key", []string{"key-2"}, false},
		{"main key plus a distinct key", []string{"key-1", "key-2"}, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := onlyUsesKey(test.downloadKeys, "key-1"); got != test.want {
				t.Errorf("onlyUsesKey(%q) = %t, want %t", test.downloadKeys, got, test.want)
			}
		})
	}
}
