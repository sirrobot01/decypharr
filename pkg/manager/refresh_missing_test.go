package manager

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func refreshTestManager(t *testing.T, entries ...*storage.Entry) *Manager {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	store, err := storage.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, e := range entries {
		if err := store.AddOrUpdate(e); err != nil {
			t.Fatal(err)
		}
	}
	return &Manager{storage: store, clients: xsync.NewMap[string, debrid.Client](), logger: zerolog.Nop()}
}

func torboxEntry(hash string, added time.Time) *storage.Entry {
	return &storage.Entry{
		InfoHash:       hash,
		Name:           "pack-" + hash,
		ActiveProvider: "torbox",
		CreatedAt:      added,
		AddedOn:        added,
		Files:          map[string]*storage.File{"ep.mkv": {Name: "ep.mkv", Size: 1, InfoHash: hash}},
		Providers: map[string]*storage.ProviderEntry{
			"torbox": {Provider: "torbox", ID: "1", AddedAt: added, Status: types.TorrentStatusDownloaded},
		},
	}
}

// On 2026-10-30 TorBox's torrent list did not yet include a pack added 3.5
// minutes earlier. The refresh treated it as removed from TorBox and deleted
// the entry; the next refresh re-added it five minutes later. Meanwhile its
// files returned 404 and Sonarr's import failed. A recently added entry
// missing from the list is now kept for the next refresh to see.
func TestRefreshKeepsRecentlyAddedEntryMissingFromProviderList(t *testing.T) {
	fresh := torboxEntry("fresh", time.Now().Add(-4*time.Minute))
	m := refreshTestManager(t, fresh)

	_, _, toDelete, err := m.detectTorrentChanges("torbox", map[string]*types.Torrent{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toDelete) != 0 {
		t.Fatalf("deleted %v, want a recently added entry kept while the provider list catches up", toDelete)
	}
}

func TestRefreshStillDeletesOldEntryMissingFromProviderList(t *testing.T) {
	old := torboxEntry("old", time.Now().Add(-2*time.Hour))
	m := refreshTestManager(t, old)

	_, _, toDelete, err := m.detectTorrentChanges("torbox", map[string]*types.Torrent{})
	if err != nil {
		t.Fatal(err)
	}
	if len(toDelete) != 1 || toDelete[0] != "old" {
		t.Fatalf("toDelete = %v, want the old entry deleted", toDelete)
	}
}
