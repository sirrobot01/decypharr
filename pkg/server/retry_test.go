package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/sessions"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// TestRetryTorrent_OperationalAcceptance is a black-box operational acceptance
// test for the POST /api/torrents/{hash}/retry endpoint.
//
// The test exercises the full HTTP path:
//  1. Setup: creates a real manager (storage + queue) and a test server
//     mirroring the existing route layout.
//  2. Creates an errored entry in the queue.
//  3. Sends a POST retry request.
//  4. Asserts 200 with {"status":"retrying","hash":"..."}.
//  5. Asserts durable side effects (entry state reset).
//  6. Tests idempotency: re-retry on non-error entry → 409.
//  7. Tests 404 for non-existent hash.
//
// ACCEPTANCE: This test validates the POST /api/torrents/{hash}/retry endpoint.
func TestRetryTorrent_OperationalAcceptance(t *testing.T) {
	// -- setup: temporary directory for config + storage -------------------
	tempDir := t.TempDir()
	config.Reset()
	config.SetConfigPath(tempDir)
	cfg := config.Get()
	cfg.UseAuth = false // Disable auth for testing

	// -- setup: real manager (creates its own storage + queue) --------------
	mgr := manager.New()
	defer func() {
		if err := mgr.Stop(); err != nil {
			t.Logf("manager stop returned: %v", err)
		}
	}()

	// -- construct a test Server matching the real route layout ------------
	// We mirror the route structure from routes.go (authMiddleware + /api/torrents)
	// but without the retry route, since it has not been implemented yet.
	s := &Server{
		logger:  zerolog.Nop(),
		manager: mgr,
		cookie:  sessions.NewCookieStore([]byte("test-key")),
	}

	r := chi.NewRouter()
	r.Use(middleware.StripSlashes)
	r.Use(middleware.RedirectSlashes)

	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		r.Route("/api", func(r chi.Router) {
			// Existing torrent routes (matching routes.go lines 59-63)
			r.Get("/torrents", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Post("/torrents/refresh", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Patch("/torrents/{hash}/label", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
			r.Delete("/torrents/{category}/{hash}", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			})
		r.Delete("/torrents", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		// Retry route
		r.Post("/torrents/{hash}/retry", s.handleRetryTorrent)
		})
	})

	s.router = r
	ts := httptest.NewServer(s.router)
	defer ts.Close()

	ctx := context.Background()
	_ = ctx

	testHash := "deadbeeffeed0001deadbeeffeed0001"
	client := &http.Client{Timeout: 10 * time.Second}

	// =====================================================================
	// Test 1: Successful retry of an errored entry
	// =====================================================================
	t.Run("success_retry_errored_entry", func(t *testing.T) {
		now := time.Now()
		entry := &storage.Entry{
			Protocol:    config.ProtocolTorrent,
			InfoHash:    testHash,
			Name:        "test-errored-torrent",
			State:       storage.EntryStateError,
			LastError:   "previous download failure",
			Progress:    0.8,
			IsDownloading: true,
			AddedOn:     now,
			CreatedAt:   now,
			UpdatedAt:   now,
			SavePath:    tempDir,
		}
		if err := mgr.Queue().Add(entry); err != nil {
			t.Fatalf("failed to add entry to queue: %v", err)
		}

		// Verify entry was stored with error state
		stored, err := mgr.Queue().GetTorrent(testHash)
		if err != nil {
			t.Fatalf("failed to get queued entry: %v", err)
		}
		if stored.State != storage.EntryStateError {
			t.Fatalf("expected entry in error state, got %q", stored.State)
		}

		// Send retry request
		url := ts.URL + "/api/torrents/" + testHash + "/retry"
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Assert 200 OK response
		if resp.StatusCode != http.StatusOK {
			t.Errorf("FAIL: expected status 200 OK, got %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			// Log response body for debugging
			var debugBody map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&debugBody) == nil {
				t.Logf("response body: %v", debugBody)
			}
			return
		}

		// Parse successful response
		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode response: %v", err)
			return
		}
		if body["status"] != "retrying" {
			t.Errorf("expected status 'retrying', got %v", body["status"])
		}
		if body["hash"] != testHash {
			t.Errorf("expected hash %q, got %v", testHash, body["hash"])
		}

		// Assert durable side effects — entry state should be reset
		updated, err := mgr.Queue().GetTorrent(testHash)
		if err != nil {
			t.Fatalf("failed to get updated entry: %v", err)
		}
		if updated.State != storage.EntryStateDownloading {
			t.Errorf("expected state %q after retry, got %q", storage.EntryStateDownloading, updated.State)
		}
		if updated.LastError != "" {
			t.Errorf("expected LastError to be cleared, got %q", updated.LastError)
		}
		if updated.Progress != 0 {
			t.Errorf("expected Progress to be reset to 0, got %f", updated.Progress)
		}
		if updated.IsDownloading {
			t.Errorf("expected IsDownloading to be false, got true")
		}
	})

	// =====================================================================
	// Test 2: Idempotency — retrying a non-error entry returns 409
	// =====================================================================
	// NOTE: This test depends on Test 1 succeeding and resetting the entry
	// to downloading state. If Test 1 failed (route missing), this will
	// also hit the missing route.
	t.Run("idempotency_non_error_entry", func(t *testing.T) {
		// First ensure we have an entry in a non-error state
		now := time.Now()
		nonErrorHash := "cafebeefcafebeefcafebeefcafebeef"
		entry := &storage.Entry{
			Protocol:      config.ProtocolTorrent,
			InfoHash:      nonErrorHash,
			Name:          "test-downloading-torrent",
			State:         storage.EntryStateDownloading,
			LastError:     "",
			Progress:      0.5,
			IsDownloading: true,
			AddedOn:       now,
			CreatedAt:     now,
			UpdatedAt:     now,
			SavePath:      tempDir,
		}
		if err := mgr.Queue().Add(entry); err != nil {
			t.Fatalf("failed to add downloading entry: %v", err)
		}

		url := ts.URL + "/api/torrents/" + nonErrorHash + "/retry"
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Expect 409 Conflict for non-error entries
		if resp.StatusCode != http.StatusConflict {
			t.Errorf("FAIL: expected status 409 Conflict, got %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			var debugBody map[string]interface{}
			if json.NewDecoder(resp.Body).Decode(&debugBody) == nil {
				t.Logf("response body: %v", debugBody)
			}
			return
		}

		var body map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode response: %v", err)
			return
		}
		if body["error"] != "Entry is not in error state" {
			t.Errorf("expected error 'Entry is not in error state', got %v", body["error"])
		}
	})

	// =====================================================================
	// Test 3: Retry non-existent hash returns 404
	// =====================================================================
	t.Run("not_found", func(t *testing.T) {
		nonExistentHash := "00000000000000000000000000000000"
		url := ts.URL + "/api/torrents/" + nonExistentHash + "/retry"
		req, err := http.NewRequest(http.MethodPost, url, nil)
		if err != nil {
			t.Fatalf("failed to create request: %v", err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("request failed: %v", err)
		}
		defer resp.Body.Close()

		// Expect 404 for non-existent entry
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected status 404 Not Found, got %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
			return
		}
		t.Logf("Got 404 as expected for non-existent hash")
	})
}
