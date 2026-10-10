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
