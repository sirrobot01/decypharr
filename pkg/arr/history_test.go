package arr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFindGrabHistoryID(t *testing.T) {
	for _, test := range []struct {
		name     string
		arrType  Type
		queryKey string
		mediaID  int
	}{
		{name: "sonarr episode", arrType: Sonarr, queryKey: "episodeId", mediaID: 41},
		{name: "radarr movie", arrType: Radarr, queryKey: "movieIds", mediaID: 73},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v3/history" {
					t.Errorf("path = %q", r.URL.Path)
				}
				if got := r.URL.Query().Get(test.queryKey); got != fmt.Sprint(test.mediaID) {
					t.Errorf("%s = %q", test.queryKey, got)
				}
				if got := r.Header.Get("X-Api-Key"); got != "arr-secret" {
					t.Errorf("API key = %q", got)
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"page":1,"totalRecords":1,"records":[{"id":99,"downloadId":"download-1","eventType":"grabbed"}]}`)
			}))
			defer server.Close()

			s := testService(Arr{Host: server.URL, Token: "arr-secret", Type: test.arrType})
			id, downloadID, err := s.LatestGrabID(t.Context(), "arr", test.mediaID)
			if err != nil {
				t.Fatal(err)
			}
			if id != 99 || downloadID != "download-1" {
				t.Fatalf("history = (%d, %q), want (99, %q)", id, downloadID, "download-1")
			}
		})
	}
}

func TestFindGrabHistoryIDReturnsEmptyWhenNoGrabExists(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"page":1,"totalRecords":0,"records":[]}`)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	id, downloadID, err := s.LatestGrabID(t.Context(), "arr", 3)
	if err != nil || id != 0 || downloadID != "" {
		t.Fatalf("history = (%d, %q, %v), want empty", id, downloadID, err)
	}
}

func TestHistoryByDownloadIDPaginatesAndFiltersEvent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodGet || r.URL.Path != "/api/v3/history" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("downloadId"); got != "download-1" {
			t.Errorf("downloadId = %q", got)
		}
		if got := r.Header.Get("X-Api-Key"); got != "secret" {
			t.Errorf("API key = %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "1":
			_, _ = fmt.Fprint(w, `{"page":1,"pageSize":1,"totalRecords":2,"records":[{"id":21,"downloadId":"download-1","eventType":"grabbed"}]}`)
		case "2":
			_, _ = fmt.Fprint(w, `{"page":2,"pageSize":1,"totalRecords":2,"records":[{"id":22,"downloadId":"download-1","eventType":"downloadFailed"}]}`)
		default:
			t.Errorf("unexpected page %q", r.URL.Query().Get("page"))
		}
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	record, found, err := s.GrabHistory(t.Context(), "arr", "download-1")
	if err != nil {
		t.Fatal(err)
	}
	if !found || record.ID != 21 {
		t.Fatalf("record = %#v, found = %v", record, found)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
}

func TestMarkHistoryFailedCtxValidatesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v3/history/failed/42" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Radarr})
	if err := s.FailHistory(t.Context(), "arr", 42); err == nil {
		t.Fatal("expected non-success status error")
	}
}
