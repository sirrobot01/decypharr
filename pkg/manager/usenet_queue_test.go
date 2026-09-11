package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
)

func TestAddNewNZBQueuesBeforeNetworkParsing(t *testing.T) {
	oldPath := config.GetMainPath()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(func() { config.SetConfigPath(oldPath) })

	cfg := config.Get()
	oldUsenet := cfg.Usenet
	cfg.Usenet = config.Usenet{
		Providers: []config.UsenetProvider{{
			Host:           "127.0.0.1",
			Port:           1,
			MaxConnections: 1,
		}},
		MaxConnections:           1,
		ProcessingMaxConnections: 1,
	}
	t.Cleanup(func() { cfg.Usenet = oldUsenet })

	usenetClient, err := usenet.New()
	if err != nil {
		t.Fatalf("create usenet client: %v", err)
	}
	t.Cleanup(func() {
		if err := usenetClient.Close(); err != nil {
			t.Errorf("close usenet client: %v", err)
		}
	})

	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close storage: %v", err)
		}
	})

	m := &Manager{
		usenet: usenetClient,
		queue:  newQueue(store, ""),
		logger: zerolog.Nop(),
	}
	received := make(chan *Job, 1)
	release := make(chan struct{}, 1)
	processed := make(chan struct{}, 1)
	m.jobQueue = NewJobQueue(t.Context(), 1, func(ctx context.Context, job *Job) {
		received <- job
		<-release
		m.processJob(ctx, job)
		processed <- struct{}{}
	})
	t.Cleanup(m.jobQueue.Close)
	t.Cleanup(func() {
		select {
		case release <- struct{}{}:
		default:
		}
	})

	req := NewNZBRequest(
		"release.nzb",
		t.TempDir(),
		[]byte("network parsing must happen in the worker"),
		arr.Arr{Name: "sonarr"},
		config.DownloadActionSymlink,
		"",
		ImportTypeSABnzbd,
		false,
	)

	id, err := m.AddNewNZB(t.Context(), req)
	if err != nil {
		t.Fatalf("AddNewNZB() error = %v", err)
	}
	if id != req.Id {
		t.Fatalf("AddNewNZB() id = %q, want %q", id, req.Id)
	}
	if len(req.NZBContent) != 0 {
		t.Fatal("queued request retained NZB content in memory")
	}

	entry, err := m.queue.GetTorrent(id)
	if err != nil {
		t.Fatalf("get queued NZB: %v", err)
	}
	if entry.Status != debridTypes.TorrentStatusQueued || entry.Magnet == "" {
		t.Fatalf("queued NZB status/path = %q/%q", entry.Status, entry.Magnet)
	}
	if _, err := os.Stat(entry.Magnet); err != nil {
		t.Fatalf("staged NZB source: %v", err)
	}

	select {
	case job := <-received:
		if job.Entry == nil || job.Entry.InfoHash != entry.InfoHash || job.NZBMeta != nil || job.NZBGroups != nil {
			t.Fatalf("queued job parsed synchronously: %#v", job)
		}
	case <-time.After(time.Second):
		t.Fatal("NZB job was not submitted")
	}

	release <- struct{}{}
	select {
	case <-processed:
	case <-time.After(time.Second):
		t.Fatal("queued NZB was not processed")
	}
	failed, err := m.queue.GetTorrent(id)
	if err != nil {
		t.Fatalf("get failed NZB: %v", err)
	}
	if failed.State != storage.EntryStateError {
		t.Fatalf("invalid NZB state = %q, want error", failed.State)
	}
}
