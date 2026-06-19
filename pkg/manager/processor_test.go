package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// TestProcessActionDownloadFailureDoesNotRevertStatus confirms the bug where
// processAction marks an entry as "downloaded" and persists it to main storage
// BEFORE running the download. When the download fails, it neither reverts the
// status nor calls markAsError, leaving the entry in a permanently wrong state.
func TestProcessActionDownloadFailureDoesNotRevertStatus(t *testing.T) {
	// -- setup: temporary directory for config + storage -------------------
	tempDir := t.TempDir()

	// Reset config singleton so our temp dir is used.
	config.Reset()
	config.SetConfigPath(tempDir)
	cfg := config.Get()

	// -- setup: real storage backed by a temp DB ---------------------------
	dbPath := filepath.Join(tempDir, "db")
	st, err := storage.NewStorage(dbPath)
	if err != nil {
		t.Fatalf("failed to create storage: %v", err)
	}
	defer st.Close()

	// -- setup: queue (same storage instance) ------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := newQueue(ctx, st, 100, "")

	// -- setup: minimal but functional Manager -----------------------------
	m := &Manager{
		storage:       st,
		queue:         queue,
		logger:        zerolog.Nop(),
		config:        cfg,
		ctx:           ctx,
		Notifications: nil,
	}
	m.entry = NewEntryCache(m)
	m.downloader = NewDownloadManager(m)

	// -- create an entry that will cause download() to fail ----------------
	// Protocol=NZB + Action=Download => processUsenetDownload()
	// m.usenet is nil => returns "usenet client not configured"
	now := time.Now()
	entry := &storage.Entry{
		Protocol:      config.ProtocolNZB,
		InfoHash:      "test-hash-001",
		Name:          "test-torrent",
		SavePath:      os.TempDir(),
		State:         storage.EntryStateDownloading,
		Action:        config.DownloadActionDownload,
		Size:          1000,
		IsDownloading: true,
		Progress:      0.5,
		AddedOn:       now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	// -- act: invoke the code under test -----------------------------------
	m.processAction(entry)

	// -- assert: the entry was NOT persisted to main storage (fix verification) ----
	// After the fix, the entry should NOT be in main storage on download failure.
	_, getErr := st.Get("test-hash-001")
	if getErr == nil {
		t.Error("BUG: entry should not be in main storage after download failure")
	}

	// The entry should be updated in the queue with error state
	queued, err := st.GetQueued("test-hash-001")
	if err != nil {
		t.Fatalf("entry not found in queue after processAction: %v", err)
	}

	// BUG ASSERTIONS — all of these SHOULD pass after the fix, but currently
	// fail because processAction never reverts the entry on download failure.

	if queued.State != storage.EntryStateError {
		t.Errorf("BUG: expected queued entry.State to be EntryStateError, got %q", queued.State)
	}

	if queued.LastError == "" {
		t.Errorf("BUG: expected queued entry.LastError to be populated, got empty string")
	}

	if queued.Status == debridTypes.TorrentStatusDownloaded {
		t.Errorf("BUG: expected queued entry.Status to NOT be TorrentStatusDownloaded after failure, got %q", queued.Status)
	}

	// Also verify the in-memory entry was not mutated to a correct state.
	if entry.State != storage.EntryStateError {
		t.Errorf("BUG: expected in-memory entry.State to be EntryStateError, got %q", entry.State)
	}

	if entry.LastError == "" {
		t.Errorf("BUG: expected in-memory entry.LastError to be populated, got empty string")
	}

	if entry.Status == debridTypes.TorrentStatusDownloaded {
		t.Errorf("BUG: expected in-memory entry.Status to NOT be TorrentStatusDownloaded, got %q", entry.Status)
	}
}
