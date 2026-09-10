package reacquire

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func TestLibraryRecoveryUsesDurableJobsAndWaitsForReplacement(t *testing.T) {
	configureArrHTTPTest(t)
	var deleted, imported atomic.Bool
	var searches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/moviefile/42":
			if deleted.Load() {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"id":42,"movieId":9,"path":"/library/movie.mkv"}`)
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/moviefile/42":
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/api/v3/config/downloadclient":
			fmt.Fprint(w, `{"enableCompletedDownloadHandling":true}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			searches.Add(1)
			fmt.Fprint(w, `{"id":123,"name":"MoviesSearch"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v3/movie/9":
			if imported.Load() {
				fmt.Fprint(w, `{"id":9,"movieFile":{"id":43,"movieId":9,"path":"/library/new.mkv"}}`)
			} else {
				fmt.Fprint(w, `{"id":9}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	registry := arr.New()
	registry.AddOrUpdate(arr.Arr{Name: "movies", Type: arr.Radarr, Host: server.URL, Token: "token"})
	directory := t.TempDir()
	service, err := NewService(ServiceOptions{Directory: directory, Arrs: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if err := service.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	request := LibraryRequest{ArrName: "movies", ArrFileID: 42, LibraryPath: "/library/wrong.mkv", Cause: CauseRepair}
	if _, err := service.ReacquireLibraryFile(t.Context(), request); !errors.Is(err, ErrBindingUnsafe) {
		t.Fatalf("wrong path: %v", err)
	}
	if len(service.Jobs()) != 0 || deleted.Load() {
		t.Fatal("unverified request caused work")
	}
	request.LibraryPath = "/library/movie.mkv"
	job, err := service.ReacquireLibraryFile(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !service.runJob(t.Context(), NewHandler(registry), *job) {
		t.Fatal("job failed to run")
	}
	waiting, _ := service.Job(job.ID)
	if !waiting.Status.waiting() || len(waiting.Mutations) != 1 || waiting.Mutations[0].State != MutationConfirmed {
		t.Fatalf("job = %#v", waiting)
	}
	if !deleted.Load() || searches.Load() != 1 {
		t.Fatalf("deleted=%v searches=%d", deleted.Load(), searches.Load())
	}
	service.reconcileLibraryJobs(t.Context())
	waiting, _ = service.Job(job.ID)
	if !waiting.Status.waiting() {
		t.Fatal("missing replacement completed job")
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(ServiceOptions{Directory: directory, Arrs: registry})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if err := reopened.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	duplicate, err := reopened.ReacquireLibraryFile(t.Context(), request)
	if err != nil || duplicate.ID != job.ID {
		t.Fatalf("duplicate after delete/restart = %v, %v", duplicate, err)
	}
	binding := job.Bindings[0]
	binding.EntryID, binding.EntryFileID, binding.DownloadID = "managed-entry", "managed-file", "download"
	binding.Confidence = ConfidenceExactPath
	// Indexing the original file cannot create a second mutation owner.
	if err := reopened.UpsertBinding(binding); err != nil {
		t.Fatal(err)
	}
	duplicate, err = reopened.Reacquire(Request{EntryID: binding.EntryID, FileID: binding.EntryFileID, Cause: CauseStream})
	if err != nil || duplicate.ID != job.ID {
		t.Fatalf("indexed duplicate = %v, %v", duplicate, err)
	}
	imported.Store(true)
	reopened.reconcileLibraryJobs(t.Context())
	ready, _ := reopened.Job(job.ID)
	if ready.Status != StatusReady || searches.Load() != 1 {
		t.Fatalf("status=%s searches=%d", ready.Status, searches.Load())
	}
}
