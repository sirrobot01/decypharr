package manager

import (
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// statusProvider answers every status check with a fixed error.
type statusProvider struct {
	debrid.Client
	checkErr  error
	deletions atomic.Int64
}

func (c *statusProvider) Config() config.Debrid {
	return config.Debrid{Name: "provider", Provider: "torbox"}
}
func (c *statusProvider) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	return torrent, c.checkErr
}
func (c *statusProvider) DeleteTorrent(string) error { c.deletions.Add(1); return nil }

func TestProcessQueuedTorrentDeletesOnlyOnTerminalErrors(t *testing.T) {
	for _, test := range []struct {
		name       string
		err        error
		wantState  storage.TorrentState
		wantDelete int64
	}{
		{"rate limit keeps torrent", errors.New("GET mylist giving up after 4 attempt(s)"), storage.EntryStateDownloading, 0},
		{"provider failure deletes", fmt.Errorf("torrent x: %w", customerror.TorrentFailedError), storage.EntryStateError, 1},
		{"not cached deletes", fmt.Errorf("torrent x: %w", customerror.TorrentNotCachedError), storage.EntryStateError, 1},
		{"missing torrent errors without delete", customerror.TorrentNotFoundError, storage.EntryStateError, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				config.Reset()
				config.SetConfigPath(t.TempDir())
				t.Cleanup(config.Reset)
				store, err := storage.NewStorage(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = store.Close() })

				client := &statusProvider{checkErr: test.err}
				m := &Manager{
					storage:           store,
					clients:           xsync.NewMap[string, debrid.Client](),
					queue:             &Queue{storage: store},
					logger:            zerolog.Nop(),
					processingEntries: xsync.NewMap[string, struct{}](),
				}
				m.clients.Store("provider", client)

				entry := &storage.Entry{
					InfoHash: "0123456789012345678901234567890123456789", Name: "release",
					Protocol: config.ProtocolTorrent, ActiveProvider: "provider",
					State:     storage.EntryStateDownloading,
					Status:    types.TorrentStatusDownloading,
					Providers: map[string]*storage.ProviderEntry{"provider": {Provider: "provider", ID: "17"}},
				}
				m.processQueuedTorrent(entry)
				synctest.Wait()

				if entry.State != test.wantState {
					t.Fatalf("state = %v, want %v", entry.State, test.wantState)
				}
				if got := client.deletions.Load(); got != test.wantDelete {
					t.Fatalf("deletions = %d, want %d", got, test.wantDelete)
				}
			})
		})
	}
}

func TestFailedChecksStayNonTerminal(t *testing.T) {
	for _, err := range []error{errors.New("connection reset"), customerror.TorrentNotFoundError, customerror.TooManyActiveDownloadsError} {
		if customerror.IsTerminalTorrentError(err) {
			t.Errorf("IsTerminalTorrentError(%v) = true", err)
		}
	}
}
