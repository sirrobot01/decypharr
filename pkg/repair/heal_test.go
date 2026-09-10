package repair

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type fakeReacquirer struct {
	libraryRequests []reacquire.LibraryRequest
	library         func(reacquire.LibraryRequest) (*reacquire.Job, error)
	requests        []reacquire.Request
	reacquire       func(reacquire.Request) (*reacquire.Job, error)
}

func (fake *fakeReacquirer) Reacquire(request reacquire.Request) (*reacquire.Job, error) {
	fake.requests = append(fake.requests, request)
	return fake.reacquire(request)
}

func (fake *fakeReacquirer) ReacquireLibraryFile(_ context.Context, request reacquire.LibraryRequest) (*reacquire.Job, error) {
	fake.libraryRequests = append(fake.libraryRequests, request)
	if fake.library == nil {
		return nil, errors.New("library recovery unavailable")
	}
	return fake.library(request)
}

func TestHealBrokenEntryQueuesStableFileIdentity(t *testing.T) {
	store := newRepairTestStorage(t)
	const (
		entryID  = "nzb-entry-1"
		fileID   = "stable-file-1"
		fileName = "Movie.2026.mkv"
	)
	entry := &storage.Entry{
		InfoHash: entryID,
		Name:     "Movie.2026",
		Files: map[string]*storage.File{
			fileName: {ID: fileID, Name: fileName, InfoHash: entryID},
		},
	}
	if err := store.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}

	reacquirer := &fakeReacquirer{reacquire: func(reacquire.Request) (*reacquire.Job, error) {
		return &reacquire.Job{ID: "reacquire-1"}, nil
	}}
	service := New(Dependencies{Storage: store, Reacquirer: reacquirer})
	run := &storage.RepairRun{ID: "repair-run-1"}
	health := &storage.EntryHealth{
		EntryName:   entry.Name,
		Status:      storage.HealthBroken,
		FileCount:   1,
		BrokenCount: 1,
		BrokenFiles: []storage.BrokenFile{{
			EntryName: entry.Name,
			FileName:  fileName,
			InfoHash:  entryID,
			ArrName:   "radarr",
		}},
	}

	var statsMu sync.Mutex
	service.healBrokenEntry(t.Context(), run, &statsMu, health)

	if len(reacquirer.requests) != 1 {
		t.Fatalf("reacquire requests = %d, want 1", len(reacquirer.requests))
	}
	request := reacquirer.requests[0]
	if request.EntryID != entryID || request.FileID != fileID {
		t.Fatalf("reacquire identity = %q/%q, want %q/%q", request.EntryID, request.FileID, entryID, fileID)
	}
	if request.Cause != reacquire.CauseRepair || request.Strategy != reacquire.StrategyHistoryFailed {
		t.Fatalf("reacquire request = %#v", request)
	}
	if run.Stats.Repaired != 1 || run.Stats.RepairFailed != 0 {
		t.Fatalf("repair stats = %#v", run.Stats)
	}
	if health.LastRepairAt.IsZero() {
		t.Fatal("LastRepairAt was not recorded")
	}
	loaded, err := store.Get(entryID)
	if err != nil {
		t.Fatalf("load queued entry: %v", err)
	}
	if file, ok := loaded.Files[fileName]; !ok || file.Deleted {
		t.Fatalf("queued entry file was removed: %#v", file)
	}
}

func TestHealBrokenEntryCountsQueueAndIdentityFailures(t *testing.T) {
	store := newRepairTestStorage(t)
	const (
		entryID  = "nzb-entry-2"
		fileName = "Episode.mkv"
	)
	if err := store.AddOrUpdate(&storage.Entry{
		InfoHash: entryID,
		Name:     "Series.Release",
		Files: map[string]*storage.File{
			fileName: {ID: "stable-file-2", Name: fileName, InfoHash: entryID},
		},
	}); err != nil {
		t.Fatal(err)
	}

	reacquirer := &fakeReacquirer{reacquire: func(reacquire.Request) (*reacquire.Job, error) {
		return nil, errors.New("queue unavailable")
	}}
	service := New(Dependencies{Storage: store, Reacquirer: reacquirer})
	run := &storage.RepairRun{ID: "repair-run-2"}
	health := &storage.EntryHealth{
		EntryName:   "Series.Release",
		Status:      storage.HealthBroken,
		FileCount:   3,
		BrokenCount: 3,
		BrokenFiles: []storage.BrokenFile{
			{FileName: fileName, InfoHash: entryID, ArrName: "sonarr"},
			{FileName: "missing.mkv", InfoHash: entryID, ArrName: "sonarr"},
			{FileName: "unmanaged.mkv", InfoHash: entryID},
		},
	}

	var statsMu sync.Mutex
	service.healBrokenEntry(t.Context(), run, &statsMu, health)

	if len(reacquirer.requests) != 1 {
		t.Fatalf("reacquire requests = %d, want only the safely resolved file", len(reacquirer.requests))
	}
	if run.Stats.Repaired != 0 || run.Stats.RepairFailed != 2 {
		t.Fatalf("repair stats = %#v", run.Stats)
	}
	if health.LastRepairAt.IsZero() {
		t.Fatal("LastRepairAt was not recorded")
	}
}

func newRepairTestStorage(t *testing.T) *storage.Storage {
	t.Helper()
	store, err := storage.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestHealBrokenEntrySkipsArrsWithoutReacquisition(t *testing.T) {
	store := newRepairTestStorage(t)
	registry := arr.New()
	registry.AddOrUpdate(arr.Arr{Name: "lidarr", Host: "http://lidarr.test", Token: "token"})

	reacquirer := &fakeReacquirer{reacquire: func(reacquire.Request) (*reacquire.Job, error) {
		return nil, errors.New("must not be called")
	}}
	service := New(Dependencies{Storage: store, Arrs: registry, Reacquirer: reacquirer})
	run := &storage.RepairRun{ID: "repair-run-3"}
	health := &storage.EntryHealth{
		EntryName:   "Album",
		Status:      storage.HealthBroken,
		FileCount:   1,
		BrokenCount: 1,
		BrokenFiles: []storage.BrokenFile{{
			FileName:  "track.flac",
			InfoHash:  "nzb-entry-3",
			ArrName:   "lidarr",
			ArrFileID: 99,
		}},
	}

	var statsMu sync.Mutex
	service.healBrokenEntry(t.Context(), run, &statsMu, health)

	if len(reacquirer.requests) != 0 {
		t.Fatalf("reacquire requests = %d, want none for a non-Sonarr/Radarr arr", len(reacquirer.requests))
	}
	if run.Stats.Repaired != 0 || run.Stats.RepairFailed != 0 {
		t.Fatalf("repair stats = %#v, want an untouched run", run.Stats)
	}
	if !health.LastRepairAt.IsZero() {
		t.Fatal("a skipped arr recorded a repair attempt")
	}
}

func TestHealBrokenEntryQueuesUnindexedLibraryFile(t *testing.T) {
	var mutations int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations++
		}
		http.Error(w, "unexpected direct Arr request", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)
	store := newRepairTestStorage(t)
	registry := arr.New()
	registry.AddOrUpdate(arr.Arr{Name: "sonarr", Host: server.URL, Token: "token"})
	reacquirer := &fakeReacquirer{library: func(reacquire.LibraryRequest) (*reacquire.Job, error) {
		return &reacquire.Job{ID: "library-job"}, nil
	}}
	service := New(Dependencies{Storage: store, Arrs: registry, Reacquirer: reacquirer})
	run := &storage.RepairRun{ID: "repair-run-4"}
	health := &storage.EntryHealth{EntryName: "Series.Release", Status: storage.HealthBroken, FileCount: 2, BrokenCount: 1,
		BrokenFiles: []storage.BrokenFile{{FileName: "Episode.mkv", InfoHash: "missing-entry", ArrName: "sonarr",
			ArrFileID: 4242, EpisodeID: 7, SourcePath: "/library/Episode.mkv"}},
	}
	var statsMu sync.Mutex
	service.healBrokenEntry(t.Context(), run, &statsMu, health)
	if mutations != 0 {
		t.Fatal("repair sent direct Arr mutations")
	}
	if len(reacquirer.libraryRequests) != 1 || reacquirer.libraryRequests[0].LibraryPath != "/library/Episode.mkv" || reacquirer.libraryRequests[0].ArrFileID != 4242 {
		t.Fatalf("library requests = %#v", reacquirer.libraryRequests)
	}
	if run.Stats.Repaired != 1 || run.Stats.RepairFailed != 0 {
		t.Fatalf("repair stats = %#v", run.Stats)
	}
}

func TestHealBrokenEntryDoesNotBypassUnsafeOrUnavailableReacquisition(t *testing.T) {
	for _, cause := range []error{reacquire.ErrBindingUnsafe, reacquire.ErrServiceNotStarted, reacquire.ErrServiceClosed} {
		t.Run(cause.Error(), func(t *testing.T) {
			store := newRepairTestStorage(t)
			if err := store.AddOrUpdate(&storage.Entry{InfoHash: "entry", Name: "release", Files: map[string]*storage.File{
				"movie.mkv": {ID: "file", Name: "movie.mkv", InfoHash: "entry"},
			}}); err != nil {
				t.Fatal(err)
			}
			reacquirer := &fakeReacquirer{reacquire: func(reacquire.Request) (*reacquire.Job, error) { return nil, cause }}
			service := New(Dependencies{Storage: store, Reacquirer: reacquirer})
			_, err := service.reacquireBrokenFile(t.Context(), storage.BrokenFile{InfoHash: "entry", FileName: "movie.mkv", ArrFileID: 7, ArrName: "radarr", SourcePath: "/library/movie.mkv"})
			if !errors.Is(err, cause) || len(reacquirer.libraryRequests) != 0 {
				t.Fatalf("error = %v, fallback calls = %d", err, len(reacquirer.libraryRequests))
			}
		})
	}
}
