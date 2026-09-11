package reacquire

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"

	"github.com/sirrobot01/decypharr/internal/config"
)

type recordedProgress struct {
	statuses []Status
}

func (progress *recordedProgress) Update(status Status, mutate func(*Job)) error {
	progress.statuses = append(progress.statuses, status)
	return nil
}

func (progress *recordedProgress) UpdateDurable(status Status, mutate func(*Job)) error {
	return progress.Update(status, mutate)
}

func TestReacquireHandlerFailsExactDownloadAndWaitsForArr(t *testing.T) {
	configureArrHTTPTest(t)
	var historyCalls atomic.Int64
	var failed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/moviefile/42":
			_, _ = fmt.Fprint(w, `{"id":42,"movieId":9,"path":"/library/movie.mkv"}`)
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v3/moviefile/42":
			w.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/config/downloadclient":
			_, _ = fmt.Fprint(w, `{"enableCompletedDownloadHandling":true,"autoRedownloadFailed":true}`)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/history":
			call := historyCalls.Add(1)
			if call == 1 {
				_, _ = fmt.Fprint(w, `{"page":1,"totalRecords":0,"records":[]}`)
			} else {
				_, _ = fmt.Fprint(w, `{"page":1,"totalRecords":1,"records":[{"id":7,"downloadId":"download-1","eventType":"grabbed"}]}`)
			}
		case request.Method == http.MethodPost && request.URL.Path == "/api/v3/history/failed/7":
			failed.Store(true)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	registry := newTestArrStorage()
	instance := arr.Arr{Name: "movies", Host: server.URL, Token: "secret", Type: arr.Radarr}
	registry.AddOrUpdate(instance)
	handler := NewHandler(registry)
	progress := &recordedProgress{}
	job := Job{
		ArrName:    "movies",
		ArrType:    arr.Radarr,
		EntryID:    "entry-1",
		FileID:     "file-1",
		DownloadID: "download-1",
		Strategy:   StrategyHistoryFailed,
		Bindings: []Binding{{
			ArrName:                "movies",
			ArrType:                arr.Radarr,
			ArrInstanceFingerprint: instance.Fingerprint(),
			EntryID:                "entry-1",
			EntryFileID:            "file-1",
			DownloadID:             "download-1",
			ArrFileID:              42,
			LibraryPath:            "/library/movie.mkv",
			MovieID:                9,
			Confidence:             ConfidenceExactPath,
		}},
	}
	if err := handler.Reacquire(t.Context(), job, progress); err != nil {
		t.Fatal(err)
	}
	if !failed.Load() {
		t.Fatal("history record was not marked failed")
	}
	if got := progress.statuses[len(progress.statuses)-1]; got != StatusWaitingForGrab {
		t.Fatalf("last status = %q, want %q", got, StatusWaitingForGrab)
	}
}

func TestReacquireHandlerRefusesStaleArrFileIdentity(t *testing.T) {
	configureArrHTTPTest(t)
	var deleted atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/moviefile/42":
			_, _ = fmt.Fprint(w, `{"id":42,"movieId":9,"path":"/library/other.mkv"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/api/v3/config/downloadclient":
			_, _ = fmt.Fprint(w, `{"enableCompletedDownloadHandling":true}`)
		case request.Method == http.MethodDelete && request.URL.Path == "/api/v3/moviefile/42":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	registry := newTestArrStorage()
	instance := arr.Arr{Name: "movies", Host: server.URL, Token: "secret", Type: arr.Radarr}
	registry.AddOrUpdate(instance)
	job := Job{
		ArrName:  "movies",
		ArrType:  arr.Radarr,
		EntryID:  "entry-1",
		FileID:   "file-1",
		Strategy: StrategyCommandSearch,
		Bindings: []Binding{{
			ArrName:                "movies",
			ArrType:                arr.Radarr,
			ArrInstanceFingerprint: instance.Fingerprint(),
			EntryID:                "entry-1",
			EntryFileID:            "file-1",
			ArrFileID:              42,
			LibraryPath:            "/library/movie.mkv",
			MovieID:                9,
			Confidence:             ConfidenceExactPath,
		}},
	}
	err := NewHandler(registry).Reacquire(t.Context(), job, &recordedProgress{})
	if err == nil {
		t.Fatal("expected stale Arr identity to be rejected")
	}
	if deleted.Load() {
		t.Fatal("stale Arr file was deleted")
	}
}

func TestReacquireHandlerRefusesChangedArrInstance(t *testing.T) {
	configureArrHTTPTest(t)
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		http.NotFound(w, request)
	}))
	defer server.Close()

	registry := newTestArrStorage()
	registry.AddOrUpdate(arr.Arr{Name: "movies", Host: server.URL, Token: "secret", Type: arr.Radarr})
	job := Job{
		ArrName:  "movies",
		ArrType:  arr.Radarr,
		EntryID:  "entry-1",
		FileID:   "file-1",
		Strategy: StrategyCommandSearch,
		Bindings: []Binding{{
			ArrName:                "movies",
			ArrType:                arr.Radarr,
			ArrInstanceFingerprint: "v1:old-instance",
			EntryID:                "entry-1",
			EntryFileID:            "file-1",
			ArrFileID:              42,
			LibraryPath:            "/library/movie.mkv",
			MovieID:                9,
			Confidence:             ConfidenceExactPath,
		}},
	}
	err := NewHandler(registry).Reacquire(t.Context(), job, &recordedProgress{})
	if err == nil {
		t.Fatal("expected changed Arr instance to be rejected")
	}
	if requests.Load() != 0 {
		t.Fatalf("changed Arr instance received %d requests", requests.Load())
	}
}

func TestAutoRedownloadsFailureHonorsInteractiveSourceConfig(t *testing.T) {
	for _, test := range []struct {
		name   string
		config arr.DownloadClientConfig
		source string
		want   bool
	}{
		{name: "master disabled", config: arr.DownloadClientConfig{AutoRedownloadFailedFromInteractiveSearch: true}, source: "InteractiveSearch"},
		{name: "automatic source", config: arr.DownloadClientConfig{AutoRedownloadFailed: true}, source: "Search", want: true},
		{name: "missing source", config: arr.DownloadClientConfig{AutoRedownloadFailed: true}, want: true},
		{name: "interactive disabled", config: arr.DownloadClientConfig{AutoRedownloadFailed: true}, source: "InteractiveSearch"},
		{name: "interactive enabled", config: arr.DownloadClientConfig{AutoRedownloadFailed: true, AutoRedownloadFailedFromInteractiveSearch: true}, source: "InteractiveSearch", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			failure := exactDownloadFailure{grabRecord: arr.HistoryRecord{Data: map[string]string{"releaseSource": test.source}}}
			if got := autoRedownloadsFailure(test.config, failure); got != test.want {
				t.Fatalf("autoRedownloadsFailure() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSearchBindingsReconcilesPersistedCommandWithoutRedispatch(t *testing.T) {
	dispatched := atomic.Int64{}
	queued := time.Now().UTC().Add(-time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			_, _ = fmt.Fprintf(w, `[{"id":71,"name":"MoviesSearch","queued":%q,"body":{"name":"MoviesSearch","movieIds":[9]}}]`, queued.Format(time.RFC3339Nano))
		case http.MethodPost:
			dispatched.Add(1)
			w.WriteHeader(http.StatusCreated)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	mutation := Mutation{
		Key:              mutationKey(MutationMovieSearch, "9"),
		Kind:             MutationMovieSearch,
		State:            MutationIntent,
		CommandName:      "MoviesSearch",
		MovieIDs:         []int{9},
		IntentAt:         queued.Add(-time.Second),
		LastDispatchedAt: queued,
		Attempts:         1,
	}
	job := Job{Mutations: []Mutation{mutation}}
	status, err := newTestHandler(server.URL).searchBindings(
		t.Context(),
		testInstance(server.URL),
		&job,
		[]Binding{{ArrType: arr.Radarr, MovieID: 9}},
		&recordedProgress{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusWaitingForGrab {
		t.Fatalf("status = %q", status)
	}
	if dispatched.Load() != 0 {
		t.Fatalf("search was redispatched %d times", dispatched.Load())
	}
	confirmed, ok := mutationForKey(job, mutation.Key)
	if !ok || confirmed.State != MutationConfirmed || confirmed.ReceiptID != 71 {
		t.Fatalf("confirmed mutation = %#v, found = %v", confirmed, ok)
	}
}

func TestSearchBindingsWaitsForCommandVisibilityBeforeRedispatch(t *testing.T) {
	dispatched := atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			dispatched.Add(1)
		}
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer server.Close()

	now := time.Now().UTC()
	mutation := Mutation{
		Key:              mutationKey(MutationMovieSearch, "9"),
		Kind:             MutationMovieSearch,
		State:            MutationIntent,
		CommandName:      "MoviesSearch",
		MovieIDs:         []int{9},
		IntentAt:         now.Add(-time.Second),
		LastDispatchedAt: now,
		Attempts:         1,
	}
	job := Job{Mutations: []Mutation{mutation}}
	_, err := newTestHandler(server.URL).searchBindings(
		t.Context(),
		testInstance(server.URL),
		&job,
		[]Binding{{ArrType: arr.Radarr, MovieID: 9}},
		&recordedProgress{},
	)
	if !errors.Is(err, arr.ErrMutationOutcomeUnknown) {
		t.Fatalf("error = %v, want unknown mutation outcome", err)
	}
	if dispatched.Load() != 0 {
		t.Fatalf("search was redispatched %d times", dispatched.Load())
	}
}

func TestPreexistingFailedHistoryUsesExplicitSearch(t *testing.T) {
	searches := atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v3/command" {
			http.NotFound(w, request)
			return
		}
		searches.Add(1)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprint(w, `{"id":72,"name":"MoviesSearch","status":"queued"}`)
	}))
	defer server.Close()

	job := Job{}
	status, err := newTestHandler(server.URL).failHistory(
		t.Context(),
		testInstance(server.URL),
		&job,
		[]Binding{{ArrType: arr.Radarr, MovieID: 9}},
		exactDownloadFailure{found: true, alreadyFailed: true, downloadID: "download-1", failedID: 18},
		arr.DownloadClientConfig{AutoRedownloadFailed: true},
		&recordedProgress{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if status != StatusWaitingForGrab || searches.Load() != 1 {
		t.Fatalf("status = %q, searches = %d", status, searches.Load())
	}
}

func testInstance(host string) arr.Arr {
	return arr.Arr{Name: "movies", Host: host, Token: "secret", Type: arr.Radarr}
}

func newTestHandler(host string) *arrHandler {
	arrs := arr.New()
	arrs.AddOrUpdate(testInstance(host))
	return &arrHandler{arrs: arrs}
}

func newTestArrStorage() *arr.Service {
	return arr.New()
}

func configureArrHTTPTest(t *testing.T) {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
}
