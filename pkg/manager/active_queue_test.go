package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestRestoreLeavesActiveDownloadsOutsideSubmissionWorkers(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := &Manager{queue: newQueue(store, ""), arr: arr.New(), logger: zerolog.Nop(), ctx: t.Context()}
	for i := range 4 {
		status := debridTypes.TorrentStatusDownloading
		if i == 3 {
			status = debridTypes.TorrentStatusQueued
		}
		if err := m.queue.Add(&storage.Entry{
			InfoHash: fmt.Sprintf("%040d", i), Name: fmt.Sprintf("entry-%d", i),
			Protocol: config.ProtocolTorrent, State: storage.EntryStateDownloading,
			Status: status, IsDownloading: i != 3, AddedOn: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	received := make(chan *Job, 4)
	m.jobQueue = NewJobQueue(t.Context(), 1, func(ctx context.Context, job *Job) {
		if job.Request == nil {
			m.processJob(ctx, job)
		}
		received <- job
	})
	t.Cleanup(m.jobQueue.Close)
	m.restoreActiveDownloadJobs()
	select {
	case job := <-received:
		if job.ID != fmt.Sprintf("%040d", 3) || job.Request == nil {
			t.Fatalf("restored a wait-only job: %#v", job)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("active downloads blocked the queued import")
	}
	for i := range 3 {
		entry, err := m.queue.GetTorrent(fmt.Sprintf("%040d", i))
		if err != nil {
			t.Fatal(err)
		}
		if entry.Status != debridTypes.TorrentStatusDownloading || entry.IsDownloading {
			t.Fatalf("active entry = %#v", entry)
		}
	}
}
