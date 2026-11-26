package arr

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestManualImportSendsEachRequestOnce(t *testing.T) {
	var lookups, commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/manualimport":
			lookups.Add(1)
			if got := r.URL.Query().Get("downloadId"); got != "release-1" {
				t.Errorf("downloadId = %q, want release-1", got)
			}
			_, _ = io.WriteString(w, `[{"path":"/downloads/release/episode.mkv","folderName":"release","series":{"id":10},"seasonNumber":1,"episodes":[{"id":20}]}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			commands.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	if err := s.ManualImport(t.Context(), "arr", "release-1"); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 || commands.Load() != 1 {
		t.Fatalf("lookups=%d commands=%d, want 1 and 1", lookups.Load(), commands.Load())
	}
}

func TestManualImportLookupIsNotRetried(t *testing.T) {
	var lookups atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/manualimport" {
			lookups.Add(1)
		}
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	start := time.Now()
	if err := s.ManualImport(t.Context(), "arr", "release-1"); err == nil {
		t.Fatal("ManualImport returned nil error for a 503 lookup")
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("lookups = %d, want 1 (no retries)", got)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("ManualImport took %s, want no retry backoff", elapsed)
	}
}

func TestManualImportGuardCooldowns(t *testing.T) {
	var g manualImportGuard
	start := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)

	if ok, _ := g.reserve("sonarr", "a", start); !ok {
		t.Fatal("first import was deferred")
	}
	if ok, wait := g.reserve("sonarr", "b", start.Add(time.Minute)); ok || wait != 4*time.Minute {
		t.Fatalf("second download inside instance cooldown: ok=%v wait=%s, want false 4m", ok, wait)
	}
	if ok, _ := g.reserve("radarr", "b", start.Add(time.Minute)); !ok {
		t.Fatal("another instance was blocked by sonarr's cooldown")
	}
	if ok, _ := g.reserve("sonarr", "b", start.Add(manualImportInstanceCooldown)); !ok {
		t.Fatal("new download deferred after the instance cooldown")
	}
	at := start.Add(2 * manualImportInstanceCooldown)
	if ok, wait := g.reserve("sonarr", "a", at); ok || wait != manualImportDownloadCooldown-2*manualImportInstanceCooldown {
		t.Fatalf("same download inside its cooldown: ok=%v wait=%s", ok, wait)
	}
	if ok, _ := g.reserve("sonarr", "a", start.Add(manualImportDownloadCooldown)); !ok {
		t.Fatal("download still deferred after its cooldown")
	}
}
