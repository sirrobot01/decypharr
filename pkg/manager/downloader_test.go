package manager

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/manager/link"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// newFailingLinkService returns a link.Service that has no debrid clients
// registered, so every GetLink call will fail with "client not found".
// entryRefresher and repairer are nil, so GetLink never retries.
func newFailingLinkService(logger zerolog.Logger) *link.Service {
	return link.New(
		xsync.NewMap[string, debrid.Client](), // no clients → all GetLink calls fail
		nil, // entryRefresher — nil so no retry on missing placement files
		nil, // repairer — nil so no repair attempts
		nil, // httpClient — not needed for this test
		0,   // retries
		logger,
	)
}

// TestProcessTorrentDownloadNoFilesLeavesStaleState verifies the bug
// where processTorrentDownload returns an error (from the "no valid download
// links" path) but never calls entry.MarkAsError(err) and never resets
// entry.IsDownloading, leaving the entry in a stale in-progress state.
//
// This test triggers the bug via an entry with no files: GetActiveFiles()
// returns empty, so the tasks slice is never populated and the "no valid
// download links available" error at line 294-296 is returned.
//
// Contrast with processUsenetDownload (lines 394-396) which correctly calls
// entry.MarkAsError(err) and d.manager.queue.Update(entry) on failure.
func TestProcessTorrentDownloadNoFilesLeavesStaleState(t *testing.T) {
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
		storage: st,
		queue:   queue,
		logger:  zerolog.Nop(),
		config:  cfg,
		ctx:     ctx,
	}
	m.downloader = NewDownloadManager(m)

	// -- create an entry with no files, which triggers the "no valid
	//    download links available" error path at line 294-296. -------------
	now := time.Now()
	entry := &storage.Entry{
		Protocol:  config.ProtocolTorrent,
		InfoHash:  "test-hash-no-files",
		Name:      "test-torrent-no-files",
		SavePath:  tempDir,
		State:     storage.EntryStateDownloading,
		Action:    config.DownloadActionDownload,
		Size:      1000,
		AddedOn:   now,
		CreatedAt: now,
		UpdatedAt: now,
		Files:     nil, // No files → GetActiveFiles() returns empty → tasks empty → error
	}

	// -- act: invoke the code under test directly (same-package access) ----
	dlErr := m.downloader.processTorrentDownload(entry)

	// -- assert: error should be returned ---------------------------------
	if dlErr == nil {
		t.Fatal("expected error from processTorrentDownload with no files, got nil")
	}

	// -- BUG ASSERTIONS ----------------------------------------------------
	// All of these SHOULD pass after the fix, but currently FAIL because
	// processTorrentDownload never updates entry state on the error path:
	//
	//   - No call to entry.MarkAsError(err)
	//   - No call to d.manager.queue.Update(entry)
	//   - entry.IsDownloading is still true (set at line 261)
	//   - entry.State was never changed to EntryStateError
	//   - entry.LastError was never populated
	//
	// Contrast with processUsenetDownload at lines 394-396 which does both.

	if entry.IsDownloading {
		t.Errorf("BUG: expected entry.IsDownloading to be false after download failure, "+
			"got true (entry left in stale downloading state)")
	}

	if entry.State != storage.EntryStateError {
		t.Errorf("BUG: expected entry.State to be EntryStateError after download failure, got %q",
			entry.State)
	}

	if entry.LastError == "" {
		t.Error("BUG: expected entry.LastError to be populated after download failure, got empty string")
	}
}

// TestProcessTorrentDownloadLinkFailureLeavesStaleState verifies the same
// bug but through the path where files exist yet the link service fails to
// provide any valid download links (e.g. all GetLink calls return errors).
// The tasks slice is still empty and the same "no valid download links"
// error path at line 294-296 is hit.
func TestProcessTorrentDownloadLinkFailureLeavesStaleState(t *testing.T) {
	// -- setup: temporary directory for config + storage -------------------
	tempDir := t.TempDir()

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

	// -- setup: queue ------------------------------------------------------
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queue := newQueue(ctx, st, 100, "")

	// -- setup: Manager with mock linkService that has no debrid clients,
	//    causing every GetLink call to fail with "client not found". --------
	m := &Manager{
		storage:     st,
		queue:       queue,
		logger:      zerolog.Nop(),
		config:      cfg,
		ctx:         ctx,
		linkService: newFailingLinkService(zerolog.Nop()),
	}
	m.downloader = NewDownloadManager(m)

	// -- create an entry with files that will all fail link resolution ----
	now := time.Now()
	entry := &storage.Entry{
		Protocol:       config.ProtocolTorrent,
		InfoHash:       "test-hash-link-fail",
		Name:           "test-torrent-link-fail",
		SavePath:       tempDir,
		State:          storage.EntryStateDownloading,
		Action:         config.DownloadActionDownload,
		Size:           1000,
		AddedOn:        now,
		CreatedAt:      now,
		UpdatedAt:      now,
		ActiveProvider: "mock-debrid",
		Providers: map[string]*storage.ProviderEntry{
			"mock-debrid": {
				Provider: "mock-debrid",
				ID:       "mock-id",
				Files: map[string]*storage.ProviderFile{
					// Present but with empty Link and Id → getPlacementFile
					// tries to refresh via nil entryRefresher → returns error.
					"file1.mkv": {Link: "", Id: ""},
				},
			},
		},
		Files: map[string]*storage.File{
			"file1.mkv": {
				Name:     "file1.mkv",
				Size:     500,
				AddedOn:  now,
				InfoHash: "test-hash-link-fail",
			},
		},
	}

	// -- act: invoke the code under test -----------------------------------
	dlErr := m.downloader.processTorrentDownload(entry)

	// -- assert: error should be returned ---------------------------------
	if dlErr == nil {
		t.Fatal("expected error from processTorrentDownload when no valid links, got nil")
	}

	// -- BUG ASSERTIONS: same as the no-files variant ---------------------
	if entry.IsDownloading {
		t.Errorf("BUG: expected entry.IsDownloading to be false after download failure, "+
			"got true (entry left in stale downloading state)")
	}

	if entry.State != storage.EntryStateError {
		t.Errorf("BUG: expected entry.State to be EntryStateError after download failure, got %q",
			entry.State)
	}

	if entry.LastError == "" {
		t.Error("BUG: expected entry.LastError to be populated after download failure, got empty string")
	}
}

// TestLogDownloadCompletion verifies the bug where logDownloadCompletion
// always emits "download transfer completed" at Info level, regardless of
// HTTP status code.
//
// The expected behavior after fix:
//   200/206 → Info("download transfer completed")
//   429     → Warn("download transfer rate limited")
//   404/4xx → Warn("download transfer client error")
//   503/500/5xx → Error("download transfer server error")
//
// Currently the production code always calls d.logger.Info() with
// Msg("download transfer completed"), so non-200/206 cases FAIL.
func TestLogDownloadCompletion(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		wantLevel  string // zerolog JSON level field value
		wantMsg    string
	}{
		{
			name:       "200 OK",
			statusCode: 200,
			wantLevel:  "info",
			wantMsg:    "download transfer completed",
		},
		{
			name:       "206 Partial Content",
			statusCode: 206,
			wantLevel:  "info",
			wantMsg:    "download transfer completed",
		},
		{
			name:       "429 Rate Limited",
			statusCode: 429,
			wantLevel:  "warn",
			wantMsg:    "download transfer rate limited",
		},
		{
			name:       "503 Service Unavailable",
			statusCode: 503,
			wantLevel:  "error",
			wantMsg:    "download transfer server error",
		},
		{
			name:       "404 Not Found (client error)",
			statusCode: 404,
			wantLevel:  "warn",
			wantMsg:    "download transfer client error",
		},
		{
			name:       "500 Internal Server Error",
			statusCode: 500,
			wantLevel:  "error",
			wantMsg:    "download transfer server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := zerolog.New(&buf)
			d := &Downloader{logger: log}

			var downloaded atomic.Int64
			downloaded.Store(1024 * 1024) // 1 MB

			meta := downloadLogMeta{
				statusCode:    tt.statusCode,
				requestHost:   "example.com",
				finalHost:     "cdn.example.com",
				requestRange:  "bytes=0-1048575",
				contentRange:  "bytes 0-1048575/1048576",
				responseProto: "HTTP/2.0",
				transferMode:  "single",
				parts:         1,
			}

			d.logDownloadCompletion("test-file.mkv", time.Now(), &downloaded, meta)

			output := buf.String()

			// Check level field
			levelField := fmt.Sprintf(`"level":"%s"`, tt.wantLevel)
			if !strings.Contains(output, levelField) {
				t.Errorf("expected level %q in log output, got: %s", tt.wantLevel, output)
			}

			// Check message field
			msgField := fmt.Sprintf(`"message":"%s"`, tt.wantMsg)
			if !strings.Contains(output, msgField) {
				t.Errorf("expected message %q in log output, got: %s", tt.wantMsg, output)
			}
		})
	}
}
