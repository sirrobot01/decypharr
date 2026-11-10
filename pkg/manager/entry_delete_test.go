package manager

import (
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func newDeleteTestManager(t *testing.T) (*Manager, *storage.Storage) {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	store, err := storage.NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := &Manager{storage: store, clients: xsync.NewMap[string, debrid.Client](), logger: zerolog.Nop()}
	m.initEntryCache()
	return m, store
}

func addDeleteTestEntry(t *testing.T, store *storage.Storage, infohash, folder string, added time.Time, names ...string) *storage.Entry {
	t.Helper()
	entry := &storage.Entry{InfoHash: infohash, Name: folder, Files: map[string]*storage.File{}}
	for i, name := range names {
		entry.Files[name] = &storage.File{Name: name, InfoHash: infohash, Size: int64(100 * (i + 1)), AddedOn: added}
	}
	if err := store.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func listedNames(m *Manager, folder string) map[string]bool {
	_, children := m.GetTorrentChildren(folder)
	names := make(map[string]bool, len(children))
	for _, child := range children {
		names[child.Name()] = true
	}
	return names
}

func removeThroughMount(t *testing.T, m *Manager, folder, name string) {
	t.Helper()
	info, err := m.GetTorrentFile(folder, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RemoveEntry(info); err != nil {
		t.Fatal(err)
	}
}

func TestMountDeleteRemovesEveryEntryOfFolder(t *testing.T) {
	const folder = "Example.Show.S01.1080p"
	for _, mode := range []string{"files", "folder"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m, store := newDeleteTestManager(t)
				older := addDeleteTestEntry(t, store, "older", folder, time.Unix(1000, 0), "e01.mkv", "e02.mkv")
				newer := addDeleteTestEntry(t, store, "newer", folder, time.Unix(2000, 0), "e01.mkv", "e02.mkv")

				if mode == "files" {
					removeThroughMount(t, m, folder, "e01.mkv")
					removeThroughMount(t, m, folder, "e02.mkv")
				} else {
					info, err := m.GetTorrentEntry(folder)
					if err != nil {
						t.Fatal(err)
					}
					if err := m.RemoveEntry(info); err != nil {
						t.Fatal(err)
					}
				}
				synctest.Wait()

				for _, entry := range []*storage.Entry{older, newer} {
					if _, err := store.Get(entry.InfoHash); err == nil {
						t.Errorf("entry %s still exists", entry.InfoHash)
					}
				}
				if _, err := store.GetEntryItem(folder); err == nil {
					t.Error("folder is still in the name index")
				}
				if current, children := m.GetTorrentChildren(folder); current != nil || len(children) != 0 {
					t.Errorf("folder still listed with %d files", len(children))
				}
			})
		})
	}
}

func TestPartialMountDeleteHidesFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, store := newDeleteTestManager(t)
		const folder = "Example.Movie.2020"
		addDeleteTestEntry(t, store, "hash", folder, time.Unix(1000, 0), "movie.mkv", "sample.mkv")
		if got := listedNames(m, folder); !got["movie.mkv"] || !got["sample.mkv"] {
			t.Fatalf("listing before delete = %v", got)
		}

		removeThroughMount(t, m, folder, "sample.mkv")

		if got := listedNames(m, folder); !got["movie.mkv"] || got["sample.mkv"] {
			t.Fatalf("listing after delete = %v, want only movie.mkv", got)
		}
		current, _ := m.GetTorrentChildren(folder)
		info, err := m.GetEntryInfo(folder)
		if err != nil {
			t.Fatal(err)
		}
		if current.Size() != 100 || info.Size() != 100 {
			t.Fatalf("folder size = %d (listing), %d (lookup), want 100", current.Size(), info.Size())
		}
		if _, err := m.GetTorrentFile(folder, "sample.mkv"); err == nil {
			t.Fatal("deleted file still resolves")
		}
	})
}

// Repair clears a broken copy; another entry's copy of the folder stays served.
func TestRepairRemovalKeepsOtherEntryCopy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, store := newDeleteTestManager(t)
		const folder = "Example.Show.S02.720p"
		addDeleteTestEntry(t, store, "older", folder, time.Unix(1000, 0), "e01.mkv")
		addDeleteTestEntry(t, store, "newer", folder, time.Unix(2000, 0), "e01.mkv")

		if err := m.RemoveTorrentFile(folder, "e01.mkv"); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()

		if _, err := store.Get("newer"); err == nil {
			t.Error("broken entry still exists")
		}
		if _, err := store.Get("older"); err != nil {
			t.Errorf("other entry was deleted: %v", err)
		}
		file, err := m.GetTorrentFile(folder, "e01.mkv")
		if err != nil {
			t.Fatal(err)
		}
		if file.InfoHash() != "older" {
			t.Fatalf("file served from %q, want older", file.InfoHash())
		}
	})
}

func TestConcurrentMountDeletesKeepEveryFlag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m, store := newDeleteTestManager(t)
		const folder = "Example.Show.S03.2160p"
		names := make([]string, 16)
		for i := range names {
			names[i] = fmt.Sprintf("e%02d.mkv", i+1)
		}
		addDeleteTestEntry(t, store, "hash", folder, time.Unix(1000, 0), names...)

		infos := make([]*FileInfo, len(names))
		for i, name := range names {
			info, err := m.GetTorrentFile(folder, name)
			if err != nil {
				t.Fatal(err)
			}
			infos[i] = info
		}
		var wg sync.WaitGroup
		for _, info := range infos {
			wg.Go(func() {
				if err := m.RemoveEntry(info); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		synctest.Wait()

		if _, err := store.Get("hash"); err == nil {
			t.Fatal("entry survived deleting every file")
		}
	})
}
