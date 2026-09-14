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
	service.reconcileImportedJobs(t.Context())
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
	reopened.reconcileImportedJobs(t.Context())
	ready, _ := reopened.Job(job.ID)
	if ready.Status != StatusReady || searches.Load() != 1 {
		t.Fatalf("status=%s searches=%d", ready.Status, searches.Load())
	}
}

func TestReconcileImportedManagedJobs(t *testing.T) {
	for _, tc := range []struct {
		name            string
		kind            arr.Type
		movie           string
		files           string
		episodes        string
		ready           bool
		changedInstance bool
		unauthorized    bool
		sameDownload    bool
	}{
		{name: "movie imported outside managed index", kind: arr.Radarr, movie: `{"id":9,"movieFile":{"id":44,"movieId":9,"path":"/library/local.mkv"}}`, ready: true},
		{name: "movie reimported from same download", kind: arr.Radarr, movie: `{"id":9,"movieFile":{"id":44,"movieId":9,"path":"/library/new.mkv"}}`, ready: true, sameDownload: true},
		{name: "original movie file", kind: arr.Radarr, movie: `{"id":9,"movieFile":{"id":42,"movieId":9,"path":"/library/old.mkv"}}`},
		{name: "different movie", kind: arr.Radarr, movie: `{"id":10,"movieFile":{"id":44,"movieId":10,"path":"/library/new.mkv"}}`},
		{name: "missing movie file", kind: arr.Radarr, movie: `{"id":9}`},
		{name: "changed Arr instance", kind: arr.Radarr, movie: `{"id":9,"movieFile":{"id":44,"movieId":9,"path":"/library/new.mkv"}}`, changedInstance: true},
		{name: "Arr request fails", kind: arr.Radarr, unauthorized: true},
		{name: "all episodes imported", kind: arr.Sonarr,
			files:    `[{"id":44,"seriesId":9,"path":"/library/first.mkv"},{"id":45,"seriesId":9,"path":"/library/second.mkv"}]`,
			episodes: `[{"id":101,"episodeFileId":44},{"id":102,"episodeFileId":45}]`, ready: true},
		{name: "partial season import", kind: arr.Sonarr,
			files:    `[{"id":44,"seriesId":9,"path":"/library/first.mkv"},{"id":43,"seriesId":9,"path":"/library/old-second.mkv"}]`,
			episodes: `[{"id":101,"episodeFileId":44},{"id":102,"episodeFileId":43}]`},
		{name: "different episodes", kind: arr.Sonarr,
			files:    `[{"id":44,"seriesId":9,"path":"/library/other.mkv"}]`,
			episodes: `[{"id":103,"episodeFileId":44}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configureArrHTTPTest(t)
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Method != http.MethodGet {
					t.Error("confirmation sent a mutation")
				}
				if tc.unauthorized {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/api/v3/movie/9":
					fmt.Fprint(w, tc.movie)
				case "/api/v3/episodefile":
					if r.URL.Query().Get("seriesId") != "9" {
						t.Error("wrong series ID")
					}
					fmt.Fprint(w, tc.files)
				case "/api/v3/episode":
					if r.URL.Query().Get("seriesId") != "9" {
						t.Error("wrong series ID")
					}
					fmt.Fprint(w, tc.episodes)
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)
			instance := arr.Arr{Name: "library", Type: tc.kind, Host: server.URL, Token: "token"}
			registry := arr.New()
			registry.AddOrUpdate(instance)
			directory := t.TempDir()
			service, err := NewService(ServiceOptions{Directory: directory, Arrs: registry})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = service.Close() })
			binding := Binding{
				ArrName: instance.Name, ArrType: instance.Type, ArrInstanceFingerprint: instance.Fingerprint(),
				EntryID: "managed-entry", EntryFileID: "first-file", DownloadID: "same-download",
				ArrFileID: 42, LibraryPath: "/library/old.mkv", Confidence: ConfidenceExactPath,
			}
			if tc.changedInstance {
				binding.ArrInstanceFingerprint = "different-instance"
			}
			if tc.kind == arr.Radarr {
				binding.MovieID = 9
			} else {
				binding.SeriesID = 9
				binding.EpisodeIDs = []int{101}
			}
			bindings := []Binding{binding}
			if tc.kind == arr.Sonarr {
				second := binding
				second.EntryFileID, second.ArrFileID, second.EpisodeIDs = "second-file", 43, []int{102}
				bindings = append(bindings, second)
			}
			now := time.Now()
			job := Job{
				ID: "waiting-job", ArrName: instance.Name, ArrType: instance.Type,
				EntryID: binding.EntryID, FileID: binding.EntryFileID, DownloadID: binding.DownloadID, Bindings: bindings,
				Cause: CauseRepair, Strategy: StrategyHistoryFailed, Status: StatusWaitingForImport,
				CreatedAt: now.Add(-waitingTimeout - time.Minute), UpdatedAt: now.Add(-waitingTimeout - time.Minute),
			}
			service.now = func() time.Time { return now }
			if err := service.jobRepository.Save(job); err != nil {
				t.Fatal(err)
			}
			if err := service.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			if tc.sameDownload {
				replacement := binding
				replacement.EntryID, replacement.EntryFileID, replacement.ArrFileID = "new-entry", "new-file", 44
				if err := service.UpsertBinding(replacement); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := service.Job(job.ID)
			if !before.Status.waiting() {
				t.Fatalf("job was already %s", before.Status)
			}
			service.reconcileImportedJobs(t.Context())
			updated, _ := service.Job(job.ID)
			if tc.ready && updated.Status != StatusReady || !tc.ready && !updated.Status.waiting() {
				t.Fatalf("status = %s, imported = %v", updated.Status, tc.ready)
			}
			wantRequests := int64(1)
			if tc.kind == arr.Sonarr {
				wantRequests = 2
			}
			if tc.changedInstance {
				wantRequests = 0
			}
			if requests.Load() != wantRequests {
				t.Fatalf("requests = %d, want %d", requests.Load(), wantRequests)
			}
			service.maintainJobs()
			updated, _ = service.Job(job.ID)
			want := StatusFailed
			if tc.ready {
				want = StatusReady
			}
			if updated.Status != want {
				t.Fatalf("status after timeout = %s, want %s", updated.Status, want)
			}
			if tc.ready && updated.LastError != "" {
				t.Fatalf("completed job error = %q", updated.LastError)
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
			saved, _ := reopened.Job(job.ID)
			if saved.Status != want {
				t.Fatalf("persisted status = %s, want %s", saved.Status, want)
			}
		})
	}
}
