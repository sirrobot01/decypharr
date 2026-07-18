package arr

import (
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

func TestConfirmedDecisionsRequiresSweepsAndDelay(t *testing.T) {
	a := New("radarr", "", "", true, false, nil, "", "manual")
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
	a := New("radarr", "", "", true, false, nil, "", "manual")
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
	a := New("radarr", "", "", true, false, nil, "", "manual")
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
	a := New("radarr", "", "", true, false, nil, "", "manual")
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
	a := New("radarr", "", "", true, false, nil, "", "manual")
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

	a.retryCleanupDecisions(map[int]bool{q.Id: true})
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

	a := New("radarr", server.URL, "token", false, false, nil, "", "manual")
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

	a := New("radarr", server.URL, "token", true, false, nil, "", "manual")
	if err := a.removeQueueItems(map[int]bool{123: true}, false, true, false); err != nil {
		t.Fatalf("removeQueueItems: %v", err)
	}
	if gotRemove != "false" || gotBlocklist != "true" || gotResearch != "false" {
		t.Fatalf("query = removeFromClient:%s blocklist:%s skipRedownload:%s", gotRemove, gotBlocklist, gotResearch)
	}
}

func TestRemoveQueueItemsRejectsHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "temporary failure", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	a := New("radarr", server.URL, "token", true, false, nil, "", "manual")
	if err := a.removeQueueItems(map[int]bool{123: true}, true, true, false); err == nil {
		t.Fatal("removeQueueItems accepted an HTTP 503 response")
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
