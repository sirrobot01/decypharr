package manager

import (
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// A client that deletes a torrent and adds the same hash again within the
// dedup window must get an error it can act on, not a success for a torrent
// that will never appear in torrents/info.
func TestAddNewTorrentAfterDeleteWithinDedupWindow(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.November, 17, 21, 48, 0, 0, time.UTC)
	gate := newTorrentSubmissionGate(torrentSubmissionDedupWindow)
	gate.clock = func() time.Time { return now }
	m := &Manager{
		queue:              newQueue(store, ""),
		arr:                arr.New(),
		logger:             zerolog.Nop(),
		ctx:                t.Context(),
		clients:            xsync.NewMap[string, debrid.Client](),
		torrentSubmissions: gate,
	}

	hash := strings.Repeat("a", 40)
	req := NewTorrentRequest("torbox", "/downloads", &utils.Magnet{InfoHash: hash, Name: "Book"}, arr.Arr{Name: "books"}, "", nil, "", ImportTypeQBit, false)
	// Submitted, then deleted by the client: the window remembers the hash,
	// the queue no longer has it.
	gate.mark(torrentSubmissionKey(req), now)

	assertRecentlySubmitted := func(step string) {
		t.Helper()
		err := m.AddNewTorrent(t.Context(), req)
		var ce *customerror.Error
		if !errors.As(err, &ce) {
			t.Fatalf("%s: AddNewTorrent() = %v, want a recently_submitted error", step, err)
		}
		if ce.Code != "recently_submitted" || !ce.IsRetryable() || ce.StatusCode() != http.StatusConflict {
			t.Fatalf("%s: error = code %q retryable %v status %d, want recently_submitted, retryable, 409", step, ce.Code, ce.IsRetryable(), ce.StatusCode())
		}
	}

	now = now.Add(time.Minute)
	assertRecentlySubmitted("re-add after delete")

	// Refusals slide the window like suppressed duplicates do (#308): a loop
	// that keeps retrying never gets the hash resubmitted.
	now = now.Add(4 * time.Minute)
	assertRecentlySubmitted("retry inside the slid window")

	// While the hash is still queued a duplicate add stays an idempotent
	// success, as in qBittorrent.
	if err := m.queue.Add(&storage.Entry{
		InfoHash: hash, Name: "Book", Protocol: config.ProtocolTorrent,
		State: storage.EntryStateDownloading, Status: debridTypes.TorrentStatusDownloading, AddedOn: now,
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := m.AddNewTorrent(t.Context(), req); err != nil {
		t.Fatalf("duplicate of a queued hash: AddNewTorrent() = %v, want nil", err)
	}

	// Removed again and quiet for a full window: submitted afresh. With no
	// provider configured the submission itself fails, which proves it ran.
	if err := m.queue.Delete(hash, false, nil); err != nil {
		t.Fatal(err)
	}
	now = now.Add(torrentSubmissionDedupWindow)
	err = m.AddNewTorrent(t.Context(), req)
	if err == nil || !strings.Contains(err.Error(), "no debrid clients available") {
		t.Fatalf("after a quiet window: AddNewTorrent() = %v, want a fresh submission attempt", err)
	}
}
