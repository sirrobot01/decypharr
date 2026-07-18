package arr

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

func queueItem(id int, status, trackedStatus, message string) QueueSchema {
	q := QueueSchema{
		Id:                    id,
		DownloadId:            "download-id",
		Status:                status,
		TrackedDownloadStatus: trackedStatus,
	}
	q.StatusMessages = []struct {
		Title    string   `json:"title"`
		Messages []string `json:"messages"`
	}{
		{Title: "Import failed", Messages: []string{message}},
	}
	return q
}

func cleanupPolicy(ruleID, action string) config.QueueCleanup {
	return config.QueueCleanup{
		Rules: []config.QueueCleanupRule{
			{ID: ruleID, Action: action},
		},
		ConfirmationSweeps: 3,
		ConfirmationDelay:  "5m",
	}
}

func newCleanupTestArr(host string, enabled bool) *Arr {
	return NewWithOptions("radarr", host, "token", Options{
		Cleanup: enabled,
		Source:  SourceManual,
	})
}

func TestConfirmedDecisionsRequiresSweepsAndDelay(t *testing.T) {
	a := newCleanupTestArr("", true)
	policy := cleanupPolicy("no_eligible_files", string(QueueActionBlocklistResearch))
	q := queueItem(42, "completed", "warning", "No files found are eligible for import")
	start := time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)

	checks := []struct {
		at   time.Time
		want int
	}{
		{start, 0},
		{start.Add(time.Minute), 0},
		{start.Add(4 * time.Minute), 0},
		{start.Add(5 * time.Minute), 1},
		{start.Add(6 * time.Minute), 0}, // one-shot until the condition clears
	}
	for i, check := range checks {
		if got := len(a.confirmedDecisions([]QueueSchema{q}, policy, check.at)); got != check.want {
			t.Fatalf("observation %d: got %d decisions, want %d", i+1, got, check.want)
		}
	}
}

func TestConfirmedDecisionsResetWhenConditionDisappears(t *testing.T) {
	a := newCleanupTestArr("", true)
	policy := cleanupPolicy("failed_download", string(QueueActionBlocklistResearch))
	q := queueItem(7, "failed", "error", "download failed")
	start := time.Now()

	_ = a.confirmedDecisions([]QueueSchema{q}, policy, start)
	_ = a.confirmedDecisions([]QueueSchema{q}, policy, start.Add(time.Minute))
	_ = a.confirmedDecisions(nil, policy, start.Add(2*time.Minute))

	if got := a.confirmedDecisions([]QueueSchema{q}, policy, start.Add(10*time.Minute)); len(got) != 0 {
		t.Fatalf("reappearing condition was not reset: %+v", got)
	}
}

func TestConfirmedDecisionsResetWhenConditionChanges(t *testing.T) {
	a := newCleanupTestArr("", true)
	policy := config.QueueCleanup{
		Rules: []config.QueueCleanupRule{
			{ID: "no_eligible_files", Action: string(QueueActionBlocklistResearch)},
			{ID: "unable_to_parse", Action: string(QueueActionBlocklistResearch)},
		},
		ConfirmationSweeps: 2,
		ConfirmationDelay:  "1m",
	}
	start := time.Now()
	first := queueItem(9, "completed", "warning", "No files found are eligible for import")
	changed := queueItem(9, "completed", "warning", "Unable to parse download")

	_ = a.confirmedDecisions([]QueueSchema{first}, policy, start)
	if got := a.confirmedDecisions([]QueueSchema{changed}, policy, start.Add(2*time.Minute)); len(got) != 0 {
		t.Fatalf("changed rule should start a new confirmation window: %+v", got)
	}
	if got := a.confirmedDecisions([]QueueSchema{changed}, policy, start.Add(3*time.Minute)); len(got) != 1 {
		t.Fatalf("stable changed rule should eventually confirm: %+v", got)
	}
}

func TestNoEligibleFilesPreservesDownloadClientData(t *testing.T) {
	a := newCleanupTestArr("", true)
	policy := config.QueueCleanup{
		Rules: []config.QueueCleanupRule{
			{ID: "no_eligible_files", Action: string(QueueActionBlocklistResearch)},
		},
		ConfirmationSweeps: 1,
		ConfirmationDelay:  "1ns",
	}
	q := queueItem(3, "completed", "warning", "No files found are eligible for import")
	start := time.Now()
	_ = a.confirmedDecisions([]QueueSchema{q}, policy, start)
	got := a.confirmedDecisions([]QueueSchema{q}, policy, start.Add(time.Nanosecond))
	if len(got) != 1 {
		t.Fatalf("got %d decisions, want 1", len(got))
	}
	if got[0].RemoveFromClient {
		t.Fatal("no-eligible-files cleanup must preserve download-client data")
	}
}

func TestFailedCleanupActionIsRearmed(t *testing.T) {
	a := newCleanupTestArr("", true)
	policy := config.QueueCleanup{
		Rules: []config.QueueCleanupRule{
			{ID: "failed_download", Action: string(QueueActionBlocklistResearch)},
		},
		ConfirmationSweeps: 1,
		ConfirmationDelay:  "1ns",
	}
	q := queueItem(88, "failed", "error", "download failed")
	start := time.Now()
	_ = a.confirmedDecisions([]QueueSchema{q}, policy, start)
	if got := a.confirmedDecisions([]QueueSchema{q}, policy, start.Add(time.Nanosecond)); len(got) != 1 {
		t.Fatalf("initial confirmed action = %+v", got)
	}

	observation := a.cleanupObservations[q.Id]
	a.retryCleanupDecisions(map[int]cleanupAttempt{q.Id: {
		Condition: observation.Condition,
		FirstSeen: observation.FirstSeen,
	}})
	if got := a.confirmedDecisions([]QueueSchema{q}, policy, start.Add(2*time.Nanosecond)); len(got) != 1 {
		t.Fatalf("failed action was not rearmed: %+v", got)
	}
}

func TestCleanupQueueDisabledMakesNoRequests(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, false)
	if err := a.CleanupQueue(); err != nil {
		t.Fatalf("CleanupQueue: %v", err)
	}
	if requests != 0 {
		t.Fatalf("disabled cleanup made %d HTTP requests", requests)
	}
}

func TestRemoveQueueItemsCanPreserveClientData(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	var gotRemove, gotBlocklist, gotResearch string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s, want DELETE", r.Method)
		}
		gotRemove = r.URL.Query().Get("removeFromClient")
		gotBlocklist = r.URL.Query().Get("blocklist")
		gotResearch = r.URL.Query().Get("skipRedownload")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if err := a.removeQueueItems(map[int]cleanupAttempt{123: {}}, false, true, false); err != nil {
		t.Fatalf("removeQueueItems: %v", err)
	}
	if gotRemove != "false" || gotBlocklist != "true" || gotResearch != "false" {
		t.Fatalf("query = removeFromClient:%s blocklist:%s skipRedownload:%s", gotRemove, gotBlocklist, gotResearch)
	}
}

func TestRemoveQueueItemsRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "invalid request", http.StatusBadRequest)
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if err := a.removeQueueItems(map[int]cleanupAttempt{123: {}}, true, true, false); err == nil {
		t.Fatal("removeQueueItems accepted an HTTP 400 response")
	}
}

func TestRetryCleanupDecisionIgnoresStaleObservation(t *testing.T) {
	a := newCleanupTestArr("", true)
	current := cleanupObservation{
		Condition: "new-condition",
		FirstSeen: time.Now(),
		Sweeps:    3,
		Acted:     true,
	}
	a.cleanupObservations[42] = current

	a.retryCleanupDecisions(map[int]cleanupAttempt{42: {
		Condition: "old-condition",
		FirstSeen: current.FirstSeen.Add(-time.Minute),
	}})

	if got := a.cleanupObservations[42]; !got.Acted {
		t.Fatal("stale async failure rearmed a newer cleanup observation")
	}
}

func TestGetQueueRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if queue, err := a.GetQueue(); err == nil || queue != nil {
		t.Fatalf("GetQueue accepted an HTTP 400 response: queue=%+v err=%v", queue, err)
	}
}

func TestGetQueueRejectsIncompletePagination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := QueueResponseScheme{Page: 2, PageSize: 200, TotalRecords: 2}
		if r.URL.Query().Get("page") == "1" {
			response.Page = 1
			response.Records = []QueueSchema{{Id: 1}}
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if queue, err := a.GetQueue(); err == nil || queue != nil {
		t.Fatalf("GetQueue returned a partial sweep: queue=%+v err=%v", queue, err)
	}
}

func TestManualImportItemsRejectsLookupHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v3/manualimport" {
			http.Error(w, "invalid lookup", http.StatusBadRequest)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if err := a.ManualImportItems(map[string]bool{"download-id": true}); err == nil {
		t.Fatal("ManualImportItems accepted an HTTP 400 lookup response")
	}
}

func TestManualImportItemsRejectsCommandHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/manualimport":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			http.Error(w, "invalid import", http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	if err := a.ManualImportItems(map[string]bool{"download-id": true}); err == nil {
		t.Fatal("ManualImportItems accepted an HTTP 400 command response")
	}
}

func TestCleanupQueueRearmsOnlyFailedDownloadImport(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	liveConfig := config.Get()
	originalConfig := *liveConfig
	originalConfig.QueueCleanup.Rules = append([]config.QueueCleanupRule(nil), liveConfig.QueueCleanup.Rules...)
	policy := config.QueueCleanup{
		Rules: []config.QueueCleanupRule{{
			ID:     "failed_download",
			Action: string(QueueActionImport),
		}},
		ConfirmationSweeps: 1,
		ConfirmationDelay:  "1ns",
	}
	updatedConfig := originalConfig
	updatedConfig.QueueCleanup = policy
	liveConfig.ApplyRuntime(&updatedConfig)
	defer liveConfig.ApplyRuntime(&originalConfig)

	failed := queueItem(301, "failed", "error", "download failed")
	failed.DownloadId = "failed-download"
	succeeded := queueItem(302, "failed", "error", "download failed")
	succeeded.DownloadId = "successful-download"
	queue := []QueueSchema{failed, succeeded}

	processed := make(chan string, len(queue))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/queue":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(QueueResponseScheme{
				Page:         1,
				PageSize:     200,
				TotalRecords: len(queue),
				Records:      queue,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/manualimport":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]ImportResponseSchema{{Path: "/download/file.mkv"}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			var request ManualImportRequestSchema
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Files) != 1 {
				http.Error(w, "invalid command", http.StatusBadRequest)
				return
			}
			downloadID := request.Files[0].DownloadId
			if downloadID == failed.DownloadId {
				http.Error(w, "invalid import", http.StatusBadRequest)
			} else {
				w.WriteHeader(http.StatusOK)
			}
			processed <- downloadID
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	a := newCleanupTestArr(server.URL, true)
	_ = a.confirmedDecisions(queue, policy, time.Now().Add(-time.Second))
	if err := a.CleanupQueue(); err != nil {
		t.Fatalf("CleanupQueue: %v", err)
	}

	seen := make(map[string]bool, len(queue))
	for len(seen) < len(queue) {
		select {
		case downloadID := <-processed:
			seen[downloadID] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for manual imports: seen=%v", seen)
		}
	}

	deadline := time.Now().Add(time.Second)
	for {
		a.cleanupMu.Lock()
		failedActed := a.cleanupObservations[failed.Id].Acted
		succeededActed := a.cleanupObservations[succeeded.Id].Acted
		a.cleanupMu.Unlock()
		if !failedActed && succeededActed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected rearm state: failed acted=%v successful acted=%v", failedActed, succeededActed)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestCleanupConfirmationPolicySafeDefaults(t *testing.T) {
	sweeps, delay := cleanupConfirmationPolicy(config.QueueCleanup{})
	if sweeps != 3 {
		t.Fatalf("sweeps = %d, want 3", sweeps)
	}
	if delay != 5*time.Minute {
		t.Fatalf("delay = %s, want 5m", delay)
	}
}
