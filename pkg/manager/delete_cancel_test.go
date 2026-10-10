package manager

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// newEndlessDownloadServer streams a large body slowly until the client goes
// away. started is closed when the first GET arrives, stopped when the server
// sees that request's context end.
func newEndlessDownloadServer(t *testing.T) (*httptest.Server, <-chan struct{}, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	stopped := make(chan struct{})
	var startedOnce, stoppedOnce sync.Once
	chunk := make([]byte, 32<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", fmt.Sprint(1<<30))
		if r.Method == http.MethodHead {
			return
		}
		startedOnce.Do(func() { close(started) })
		flusher, _ := w.(http.Flusher)
		for {
			select {
			case <-r.Context().Done():
				stoppedOnce.Do(func() { close(stopped) })
				return
			case <-time.After(5 * time.Millisecond):
			}
			if _, err := w.Write(chunk); err != nil {
				continue
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, started, stopped
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// Deleting an entry while one of its files is downloading must stop the HTTP
// transfer, remove the partial file and folder, and keep the progress
// callback from writing the entry back into the queue (#441).
func TestQueueDeleteCancelsInflightDownload(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	queue, entry, downloadedPath := newQueueDeleteTest(t)
	server, started, stopped := newEndlessDownloadServer(t)
	d := &Downloader{
		manager: &Manager{ctx: t.Context(), streamClient: server.Client(), queue: queue},
		logger:  zerolog.Nop(),
	}

	ctx, release := queue.track(t.Context(), entry.InfoHash)
	destPath := filepath.Join(downloadedPath, "partial.mkv")
	progressed := make(chan struct{}, 1)
	result := make(chan error, 1)
	go func() {
		defer release()
		result <- d.downloadFile(ctx, server.URL, destPath, nil, func(delta, speed int64) {
			entry.SizeDownloaded += delta
			entry.Speed = speed
			_ = queue.Update(entry)
			select {
			case progressed <- struct{}{}:
			default:
			}
		})
	}()
	waitFor(t, started, "download to start")
	waitFor(t, progressed, "first progress update")

	if err := queue.Delete(entry.InfoHash, true, nil); err != nil {
		t.Fatalf("delete queued entry: %v", err)
	}

	// Delete waits for the worker to release, so it has already returned.
	select {
	case err := <-result:
		if !errors.Is(err, errEntryDeleted) {
			t.Fatalf("download error = %v, want %v", err, errEntryDeleted)
		}
	default:
		t.Fatal("Delete returned while the download was still running")
	}
	waitFor(t, stopped, "server to see the request cancelled")

	if _, err := os.Stat(downloadedPath); !os.IsNotExist(err) {
		t.Fatalf("download folder still present after delete: %v", err)
	}
	time.Sleep(600 * time.Millisecond) // longer than one progress tick
	if _, err := queue.GetTorrent(entry.InfoHash); err == nil {
		t.Fatal("deleted entry was written back to the queue")
	}
}

// A worker that is cancelled by a delete must not resurrect the entry with its
// final error or progress writes.
func TestQueueUpdateRefusedForDeletedInflightEntry(t *testing.T) {
	queue, entry, _ := newQueueDeleteTest(t)
	ctx, release := queue.track(t.Context(), entry.InfoHash)

	lateWrite := make(chan error, 1)
	go func() {
		defer release()
		<-ctx.Done()
		entry.MarkAsError(context.Cause(ctx))
		lateWrite <- queue.Update(entry)
	}()

	if err := queue.Delete(entry.InfoHash, false, nil); err != nil {
		t.Fatalf("delete queued entry: %v", err)
	}
	if err := <-lateWrite; !errors.Is(err, errEntryDeleted) {
		t.Fatalf("late update error = %v, want %v", err, errEntryDeleted)
	}
	if _, err := queue.GetTorrent(entry.InfoHash); err == nil {
		t.Fatal("deleted entry was written back to the queue")
	}

	// Once the work has released, the hash is free to be queued again.
	if err := queue.Add(entry); err != nil {
		t.Fatalf("re-add entry: %v", err)
	}
	if err := queue.Update(entry); err != nil {
		t.Fatalf("update re-added entry: %v", err)
	}
}

// Shutdown cancels the same context, but interrupted work must still be able
// to save its state so it resumes on the next start.
func TestQueueUpdateAllowedAfterShutdownCancel(t *testing.T) {
	queue, entry, _ := newQueueDeleteTest(t)
	parent, shutdown := context.WithCancel(t.Context())
	ctx, release := queue.track(parent, entry.InfoHash)
	defer release()

	shutdown()
	<-ctx.Done()
	if isEntryDeleted(ctx) {
		t.Fatal("shutdown cancellation reported as entry deletion")
	}
	entry.IsDownloading = false
	if err := queue.Update(entry); err != nil {
		t.Fatalf("update after shutdown: %v", err)
	}
}

// Deleting a season pack parent cancels the season entry it is working on.
func TestQueueDeleteParentCancelsSeasonWork(t *testing.T) {
	queue, entry, _ := newQueueDeleteTest(t)
	season := &storage.Entry{InfoHash: entry.InfoHash + "-s01", Name: "Example.S01", SavePath: entry.SavePath}
	if err := queue.Add(season); err != nil {
		t.Fatalf("add season: %v", err)
	}
	parentCtx, releaseParent := queue.track(t.Context(), entry.InfoHash)
	seasonCtx, releaseSeason := queue.track(parentCtx, season.InfoHash)
	go func() {
		<-seasonCtx.Done()
		releaseSeason()
		releaseParent()
	}()

	if err := queue.DeleteWhere("", config.ProtocolAll, "", []string{entry.InfoHash}, nil); err != nil {
		t.Fatalf("delete parent: %v", err)
	}
	if !isEntryDeleted(seasonCtx) {
		t.Fatalf("season context cause = %v, want %v", context.Cause(seasonCtx), errEntryDeleted)
	}
}

// The entry's own worker removes it from the queue (action "none"); that must
// not cancel or wait on the worker itself.
func TestQueueRemoveDoesNotCancelOwnWork(t *testing.T) {
	queue, entry, _ := newQueueDeleteTest(t)
	ctx, release := queue.track(t.Context(), entry.InfoHash)
	defer release()

	done := make(chan error, 1)
	go func() { done <- queue.remove(entry.InfoHash, false, nil) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("remove: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("remove waited on the entry's own work")
	}
	if ctx.Err() != nil {
		t.Fatal("remove cancelled the entry's own work")
	}
}
