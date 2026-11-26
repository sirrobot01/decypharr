package manager

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func newReaddTest(t *testing.T) (*Manager, *ImportRequest) {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	m := &Manager{
		queue:              newQueue(store, ""),
		arr:                arr.New(),
		logger:             zerolog.Nop(),
		ctx:                t.Context(),
		clients:            xsync.NewMap[string, debrid.Client](), // none: a real submit fails fast
		torrentSubmissions: newTorrentSubmissionGate(5 * time.Minute),
	}
	req := &ImportRequest{
		Magnet: &utils.Magnet{InfoHash: strings.Repeat("ab", 20), Name: "Some.Movie.2018.1080p.BluRay.x264-GRP"},
		Arr:    arr.Arr{Name: "radarr"},
	}
	return m, req
}

func addQueued(t *testing.T, m *Manager, req *ImportRequest, failed bool) {
	t.Helper()
	entry := newTorrentQueueEntry(req, debridTypes.TorrentStatusDownloading)
	if failed {
		entry.MarkAsError(errors.New("realdebrid API error: Status: 404"))
	}
	if err := m.queue.Add(entry); err != nil {
		t.Fatal(err)
	}
}

// The torrent was deleted on the provider, the status check failed and the
// entry went to error. The arr's re-grab of the same hash must be submitted
// again, not swallowed as a duplicate (by the queue lookup or the gate).
func TestReaddOfFailedTorrentIsSubmittedAgain(t *testing.T) {
	m, req := newReaddTest(t)
	m.torrentSubmissions.mark(torrentSubmissionKey(req), time.Now()) // the first add, a minute ago
	addQueued(t, m, req, true)

	err := m.AddNewTorrent(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "no debrid clients available") {
		t.Fatalf("AddNewTorrent() = %v, want a fresh submit to the provider", err)
	}
	if _, err := m.queue.GetTorrent(req.Magnet.InfoHash); err == nil {
		t.Fatal("the failed queue entry is still there")
	}
}

// Re-adding a hash that is still downloading stays an idempotent no-op.
func TestReaddOfActiveTorrentIsStillIgnored(t *testing.T) {
	m, req := newReaddTest(t)
	addQueued(t, m, req, false)

	if err := m.AddNewTorrent(t.Context(), req); err != nil {
		t.Fatalf("AddNewTorrent() = %v, want nil for an entry already downloading", err)
	}
	entry, err := m.queue.GetTorrent(req.Magnet.InfoHash)
	if err != nil || entry.State != storage.EntryStateDownloading {
		t.Fatalf("active entry = %#v, %v; want it kept", entry, err)
	}
}
