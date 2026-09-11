package repair

import (
	"encoding/json"
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
	requests  []reacquire.Request
	reacquire func(reacquire.Request) (*reacquire.Job, error)
}

func (fake *fakeReacquirer) Reacquire(request reacquire.Request) (*reacquire.Job, error) {
	fake.requests = append(fake.requests, request)
	return fake.reacquire(request)
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

func TestHealBrokenEntryFallsBackToLegacyRepairWhenUnmapped(t *testing.T) {
	var (
		mu      sync.Mutex
		deleted []int
		searchd bool
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/api/v3/episodefile/bulk":
			var payload struct {
				EpisodeFileIds []int `json:"episodeFileIds"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			deleted = append(deleted, payload.EpisodeFileIds...)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v3/command":
			searchd = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"records":[]}`))
	}))
	t.Cleanup(server.Close)

	store := newRepairTestStorage(t)
	registry := arr.New()
	registry.AddOrUpdate(arr.Arr{Name: "sonarr", Host: server.URL, Token: "token"})

	reacquirer := &fakeReacquirer{reacquire: func(reacquire.Request) (*reacquire.Job, error) {
		return nil, reacquire.ErrBindingNotFound
	}}
	service := New(Dependencies{Storage: store, Arrs: registry, Reacquirer: reacquirer})
	run := &storage.RepairRun{ID: "repair-run-4"}
	health := &storage.EntryHealth{
		EntryName:   "Series.Release",
		Status:      storage.HealthBroken,
		FileCount:   2,
		BrokenCount: 1,
		BrokenFiles: []storage.BrokenFile{{
			FileName:  "Episode.mkv",
			InfoHash:  "nzb-entry-4",
			ArrName:   "sonarr",
			ArrFileID: 4242,
			EpisodeID: 7,
		}},
	}

	var statsMu sync.Mutex
	service.healBrokenEntry(t.Context(), run, &statsMu, health)

	mu.Lock()
	defer mu.Unlock()
	if len(deleted) != 1 || deleted[0] != 4242 {
		t.Fatalf("deleted episode files = %v, want [4242]", deleted)
	}
	if !searchd {
		t.Fatal("legacy fallback did not re-search the missing episode")
	}
	if run.Stats.Repaired != 1 || run.Stats.RepairFailed != 0 {
		t.Fatalf("repair stats = %#v", run.Stats)
	}
}
