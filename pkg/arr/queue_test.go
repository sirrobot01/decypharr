package arr

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCleanupQueueImportsEachDownloadOnce(t *testing.T) {
	// A season pack appears once per episode, all with the same downloadId.
	record := `{"id":%d,"status":"completed","trackedDownloadStatus":"warning","downloadId":"pack-1","statusMessages":[{"title":"Release","messages":["Found matching series via grab history, but release was matched to series by ID. Automatic import is not possible."]}]}`
	records := make([]string, 22)
	for i := range records {
		records[i] = fmt.Sprintf(record, i+1)
	}

	var lookups atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v3/queue":
			_, _ = fmt.Fprintf(w, `{"totalRecords":%d,"records":[%s]}`, len(records), strings.Join(records, ","))
		case "GET /api/v3/manualimport":
			lookups.Add(1)
			_, _ = fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	if err := s.CleanupQueue(t.Context(), "arr"); err != nil {
		t.Fatal(err)
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("manual import lookups = %d, want 1", got)
	}
}

func TestCleanupQueueBacksOffWhenNothingIsImportable(t *testing.T) {
	// A partial season pack: every file is imported, but Sonarr keeps the item
	// queued because the pack lacks some episodes it expected.
	queue := `{"totalRecords":1,"records":[{"id":1,"status":"completed","trackedDownloadStatus":"warning","downloadId":"pack-1","statusMessages":[{"title":"Release","messages":["Found matching series via grab history, but release was matched to series by ID. Automatic import is not possible."]}]}]}`
	var queued atomic.Bool
	queued.Store(true)
	var lookups, posts atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v3/queue":
			if queued.Load() {
				_, _ = fmt.Fprint(w, queue)
			} else {
				_, _ = fmt.Fprint(w, `{"totalRecords":0,"records":[]}`)
			}
		case "GET /api/v3/manualimport":
			lookups.Add(1)
			_, _ = fmt.Fprint(w, `[{"path":"/downloads/Pack/S05E01.mkv","series":{"id":7},"episodes":[{"id":1}],"quality":{"quality":{"id":3}},"rejections":[{"reason":"Episode file already imported","type":"permanent"}]}]`)
		case "POST /api/v3/command":
			posts.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	s := testService(Arr{Host: server.URL, Token: "secret", Type: Sonarr})
	for range 3 {
		if err := s.CleanupQueue(t.Context(), "arr"); err != nil {
			t.Fatal(err)
		}
	}
	if got := lookups.Load(); got != 1 {
		t.Fatalf("manual import lookups = %d, want 1 (backed off after the first)", got)
	}
	if got := posts.Load(); got != 0 {
		t.Fatalf("command submissions = %d, want 0", got)
	}

	// Once the item leaves the queue its backoff is forgotten, so a later
	// re-grab with the same downloadId is tried straight away.
	queued.Store(false)
	if err := s.CleanupQueue(t.Context(), "arr"); err != nil {
		t.Fatal(err)
	}
	queued.Store(true)
	if err := s.CleanupQueue(t.Context(), "arr"); err != nil {
		t.Fatal(err)
	}
	if got := lookups.Load(); got != 2 {
		t.Fatalf("manual import lookups = %d, want 2 after the item left and returned", got)
	}
}
