package arr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestAPIRoot(t *testing.T) {
	tests := []struct {
		kind Type
		want string
	}{
		{Lidarr, APIv1},
		{Readarr, APIv1},
		{Chaptarr, APIv1},
		{Sonarr, APIv3},
		{Radarr, APIv3},
		{Prowlarr, APIv3},
		{Others, APIv3},
	}
	for _, tt := range tests {
		t.Run(string(tt.kind), func(t *testing.T) {
			if got := (Arr{Type: tt.kind}).APIRoot(); got != tt.want {
				t.Fatalf("APIRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

// Chaptarr serves v1 and has to be recognised from its name or host, so the
// queue cleanup reaches it without any per-Arr setting.
func TestChaptarrIsDetected(t *testing.T) {
	tests := []struct {
		name string
		host string
	}{
		{"Chaptarr", "http://127.0.0.1:8789"},
		{"Books", "http://chaptarr:8789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := fromConfig(config.Arr{Name: tt.name, Host: tt.host, Token: "secret"})
			if instance.Type != Chaptarr {
				t.Fatalf("type = %q, want %q", instance.Type, Chaptarr)
			}
			if got := instance.APIRoot(); got != APIv1 {
				t.Fatalf("APIRoot() = %q, want %q", got, APIv1)
			}
		})
	}
}

// A v1 instance only answers on api/v1: the queue must be read there.
func TestQueueReadsTheVersionOfTheInstance(t *testing.T) {
	for _, tt := range []struct {
		kind Type
		path string
	}{
		{Chaptarr, "/api/v1/queue"},
		{Sonarr, "/api/v3/queue"},
	} {
		t.Run(string(tt.kind), func(t *testing.T) {
			var hits atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tt.path {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
					return
				}
				hits.Add(1)
				_, _ = fmt.Fprint(w, `{"totalRecords":0,"records":[]}`)
			}))
			t.Cleanup(server.Close)

			s := testService(Arr{Host: server.URL, Token: "secret", Type: tt.kind})
			if _, err := s.Queue(t.Context(), "arr"); err != nil {
				t.Fatalf("Queue: %v", err)
			}
			if got := hits.Load(); got != 1 {
				t.Fatalf("%s hits = %d, want 1", tt.path, got)
			}
		})
	}
}

// Probe reads the status endpoint on the right version, so a v1 application is
// identified by the appName it reports.
func TestProbeReadsTheStatusOfAV1Instance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/system/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprint(w, `{"appName":"Chaptarr"}`)
	}))
	t.Cleanup(server.Close)

	instance := fromConfig(config.Arr{Name: "Chaptarr", Host: server.URL, Token: "secret"})
	kind, err := New().Probe(t.Context(), instance)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if kind != Chaptarr {
		t.Fatalf("Probe = %q, want %q", kind, Chaptarr)
	}
}

// The other calls that used to hardcode api/v3 follow the same root.
func TestSettingsAndCommandsUseTheVersionOfTheInstance(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/config/downloadclient":
			_, _ = fmt.Fprint(w, `{"id":1,"enable":true}`)
		case "/api/v1/command":
			_, _ = fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Chaptarr})
	if _, err := s.DownloadClientConfig(t.Context(), "arr"); err != nil {
		t.Fatalf("DownloadClientConfig: %v", err)
	}
	if _, err := s.Commands(t.Context(), "arr"); err != nil {
		t.Fatalf("Commands: %v", err)
	}
}
