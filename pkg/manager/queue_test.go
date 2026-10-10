package manager

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestQueueDeleteKeepsFilesWhenNotRequested(t *testing.T) {
	queue, entry, downloadedPath := newQueueDeleteTest(t)

	if err := queue.Delete(entry.InfoHash, false, nil); err != nil {
		t.Fatalf("delete queued entry: %v", err)
	}
	if _, err := os.Stat(downloadedPath); err != nil {
		t.Fatalf("expected downloaded files to remain: %v", err)
	}
	if _, err := queue.GetTorrent(entry.InfoHash); err == nil {
		t.Fatal("expected queued entry to be removed")
	}
}

func TestQueueDeleteRemovesFilesWhenRequested(t *testing.T) {
	queue, entry, downloadedPath := newQueueDeleteTest(t)

	if err := queue.Delete(entry.InfoHash, true, nil); err != nil {
		t.Fatalf("delete queued entry: %v", err)
	}
	if _, err := os.Stat(downloadedPath); !os.IsNotExist(err) {
		t.Fatalf("expected downloaded files to be removed, got %v", err)
	}
}

func newQueueDeleteTest(t *testing.T) (*Queue, *storage.Entry, string) {
	t.Helper()

	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	savePath := filepath.Join(t.TempDir(), "downloads")
	entry := &storage.Entry{
		InfoHash: "0123456789abcdef",
		Name:     "Example.mkv",
		SavePath: savePath,
	}
	downloadedPath := entry.DownloadPath()
	if err := os.MkdirAll(downloadedPath, 0o755); err != nil {
		t.Fatalf("create downloaded path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(downloadedPath, "video.mkv"), []byte("test"), 0o644); err != nil {
		t.Fatalf("create downloaded file: %v", err)
	}

	queue := newQueue(store, "")
	if err := queue.Add(entry); err != nil {
		t.Fatalf("add queued entry: %v", err)
	}
	return queue, entry, downloadedPath
}

func TestQueueDeleteStalledRemovesStuckQueuedEntries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending bool
		removed bool
	}{
		{name: "stuck", pending: false, removed: true},
		{name: "waiting for a worker", pending: true, removed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.Reset()
			config.SetConfigPath(t.TempDir())
			t.Cleanup(config.Reset)
			queue, entry, _ := newQueueDeleteTest(t)
			queue.removeStalledAfter = time.Hour
			entry.Status = debridTypes.TorrentStatusQueued
			entry.AddedOn = time.Now().Add(-2 * time.Hour)
			if err := queue.Update(entry); err != nil {
				t.Fatalf("update queued entry: %v", err)
			}

			if err := queue.DeleteStalled(func(string) bool { return tc.pending }); err != nil {
				t.Fatalf("delete stalled: %v", err)
			}
			_, err := queue.GetTorrent(entry.InfoHash)
			if removed := err != nil; removed != tc.removed {
				t.Fatalf("removed = %v, want %v", removed, tc.removed)
			}
		})
	}
}
