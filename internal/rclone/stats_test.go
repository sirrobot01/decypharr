package rclone

import (
	"encoding/json"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sirrobot01/decypharr/internal/request"
)

func TestStatsPreservesCountersAndPartialResults(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var mu sync.Mutex
	calls := make(map[string]int)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/core/stats":
			_, _ = fmt.Fprint(w, `{"bytes":9007199254740993}`)
		case "/core/memstats":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/core/bwlimit":
			_, _ = fmt.Fprint(w, `{"bytesPerSecond":123,"rate":"123B"}`)
		case "/core/version":
			_, _ = fmt.Fprint(w, `{"version":"test"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := &Client{baseURL: server.URL, client: request.New(request.WithMaxRetries(0))}
	stats := client.Stats(t.Context())
	core, ok := stats["core"].(CoreStatsResponse)
	if !ok || core.Bytes != 9007199254740993 {
		t.Fatalf("core stats lost type or precision: %#v", stats["core"])
	}
	if memory, ok := stats["memory"].(MemoryStats); !ok || memory != (MemoryStats{}) {
		t.Fatalf("failed memory section = %#v", stats["memory"])
	}
	if stats["bandwidth"].(BandwidthStats).BytesPerSecond != 123 || stats["version"].(VersionResponse).Version != "test" {
		t.Fatal("successful sections were lost")
	}
	mu.Lock()
	defer mu.Unlock()
	for _, endpoint := range []string{"/core/stats", "/core/memstats", "/core/bwlimit", "/core/version"} {
		if calls[endpoint] != 1 {
			t.Errorf("%s called %d times", endpoint, calls[endpoint])
		}
	}
	data, err := json.Marshal(stats)
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Core CoreStatsResponse `json:"core"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Core.Bytes != core.Bytes {
		t.Fatal("serialized counter lost precision")
	}
}
