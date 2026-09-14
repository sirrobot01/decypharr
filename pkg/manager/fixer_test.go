package manager

import (
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type replacementProvider struct {
	debrid.Client
	submissions int
	deletions   atomic.Int64
}

func (c *replacementProvider) Config() config.Debrid {
	return config.Debrid{Name: "remaining", Provider: "torbox"}
}
func (c *replacementProvider) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	c.submissions++
	torrent.Id = "new"
	torrent.Debrid = "remaining"
	torrent.Status = types.TorrentStatusDownloaded
	torrent.Files = map[string]types.File{"video.mkv": {Name: "video.mkv", Id: "file"}}
	return torrent, nil
}
func (c *replacementProvider) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	return torrent, nil
}
func (c *replacementProvider) DeleteTorrent(string) error { c.deletions.Add(1); return nil }

func TestFixTorrentWithRemovedProvider(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "submit replacement", true: "reuse placement"}[existing], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				config.Reset()
				config.SetConfigPath(t.TempDir())
				t.Cleanup(config.Reset)
				store, err := storage.NewStorage(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })
				client := &replacementProvider{}
				m := &Manager{storage: store, clients: xsync.NewMap[string, debrid.Client](), logger: zerolog.Nop()}
				m.clients.Store("remaining", client)
				fixer := NewFixer(m)
				fixer.providerOrder = []string{"remaining"}
				entry := &storage.Entry{
					InfoHash: "0123456789012345678901234567890123456789", Name: "release",
					Protocol: config.ProtocolTorrent, ActiveProvider: "removed",
					Providers: map[string]*storage.ProviderEntry{
						"removed": {Provider: "removed", ID: "old", Status: types.TorrentStatusDownloaded},
					},
				}
				if existing {
					entry.Providers["remaining"] = &storage.ProviderEntry{Provider: "remaining", ID: "new", Status: types.TorrentStatusDownloaded}
				}
				result, err := fixer.FixTorrent(t.Context(), entry, false)
				if err != nil || !result.Success {
					t.Fatalf("repair = %#v, error = %v", result, err)
				}
				if entry.ActiveProvider != "remaining" {
					t.Fatalf("active provider = %q", entry.ActiveProvider)
				}
				wantSubmissions := 1
				if existing {
					wantSubmissions = 0
				}
				if client.submissions != wantSubmissions {
					t.Fatalf("submissions = %d, want %d", client.submissions, wantSubmissions)
				}
				synctest.Wait()
				if client.deletions.Load() != 0 {
					t.Fatal("replacement provider received a deletion")
				}
				loaded, err := store.Get(entry.InfoHash)
				if err != nil || loaded.ActiveProvider != "remaining" {
					t.Fatalf("saved entry = %#v, error = %v", loaded, err)
				}
				if loaded.Providers["removed"].ID != "old" {
					t.Fatal("source placement changed")
				}
			})
		})
	}
}
