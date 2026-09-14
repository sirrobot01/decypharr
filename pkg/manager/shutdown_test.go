package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/notifications"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type shutdownMount struct {
	MountManager
	stop func() error
}

func (m shutdownMount) Stop() error            { return m.stop() }
func (m shutdownMount) Refresh([]string) error { return nil }

type completedTorrentProvider struct {
	debrid.Client
	torrent *types.Torrent
}

func (c completedTorrentProvider) CheckStatus(*types.Torrent) (*types.Torrent, error) {
	return c.torrent, nil
}

func TestShutdownResumesInterruptedSymlinks(t *testing.T) {
	for _, multiSeason := range []bool{false, true} {
		t.Run(map[bool]string{false: "single release", true: "season pack"}[multiSeason], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				config.Reset()
				config.SetConfigPath(t.TempDir())
				t.Cleanup(config.Reset)
				cfg := config.Get()
				cfg.Mount.MountPath = t.TempDir()
				cfg.SkipPreCache = true
				dbPath := t.TempDir()
				store, err := storage.NewStorage(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				m := &Manager{
					storage: store, config: cfg, ctx: ctx, cancelDownloads: cancel,
					queue: newQueue(store, ""), arr: arr.New(), logger: zerolog.Nop(),
					clients: xsync.NewMap[string, debrid.Client](), processingEntries: xsync.NewMap[string, struct{}](),
					Notifications: notifications.New(&cfg.Notifications, zerolog.Nop()),
				}
				m.initEntryCache()
				m.downloader = NewDownloadManager(m)
				entry := &storage.Entry{
					InfoHash: "0123456789012345678901234567890123456789", Name: "Show.S01-S02",
					Protocol: config.ProtocolTorrent, State: storage.EntryStateDownloading,
					ActiveProvider: "provider", Action: config.DownloadActionSymlink, SkipMultiSeason: !multiSeason,
					SavePath: t.TempDir(), Files: map[string]*storage.File{}, Providers: map[string]*storage.ProviderEntry{},
				}
				torrent := &types.Torrent{
					Id: "provider-id", Debrid: "provider", InfoHash: entry.InfoHash, Name: entry.Name,
					Status: types.TorrentStatusDownloaded, Progress: 100,
					Files: map[string]types.File{"Show.S02E01.mkv": {Name: "Show.S02E01.mkv", Id: "2", Size: 5}},
				}
				if multiSeason {
					torrent.Files["Show.S01E01.mkv"] = types.File{Name: "Show.S01E01.mkv", Id: "1", Size: 5}
				}
				applyDebridTorrentToEntry(entry, torrent)
				m.clients.Store("provider", completedTorrentProvider{torrent: torrent})
				if err := m.queue.Add(entry); err != nil {
					t.Fatal(err)
				}
				var completedSeason *storage.Entry
				if multiSeason {
					found, seasons := m.downloader.detectMultiSeason(entry)
					if !found {
						t.Fatal("season pack was not detected")
					}
					for _, season := range convertToMultiSeason(entry, seasons) {
						if season.Files["Show.S01E01.mkv"] != nil {
							completedSeason = season
							season.MarkAsCompleted(season.DownloadPath())
							if err := m.queue.Add(season); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				mountPath := m.GetTorrentMountPath(entry)
				if err := os.MkdirAll(mountPath, 0o755); err != nil {
					t.Fatal(err)
				}
				mountStops := 0
				m.mountManager = shutdownMount{stop: func() error {
					mountStops++
					saved, err := m.queue.GetTorrent(entry.InfoHash)
					if err != nil {
						return err
					}
					if saved.IsDownloading {
						t.Error("mount stopped before interrupted work was saved")
					}
					return nil
				}}
				m.processNewTorrent(entry, torrent)
				synctest.Wait()
				saved, err := m.queue.GetTorrent(entry.InfoHash)
				if err != nil || !saved.IsDownloading || saved.IsComplete {
					t.Fatalf("in-flight entry = %#v, error = %v", saved, err)
				}
				if err := m.Stop(); err != nil {
					t.Fatal(err)
				}
				if mountStops != 1 {
					t.Fatalf("mount stops = %d", mountStops)
				}
				if m.startDownloadTask(func() { t.Error("work started after shutdown") }) {
					t.Fatal("shutdown accepted new work")
				}
				synctest.Wait()

				store, err = storage.NewStorage(dbPath)
				if err != nil {
					t.Fatal(err)
				}
				m.storage, m.queue = store, newQueue(store, "")
				m.ctx, m.cancelDownloads = context.WithCancel(t.Context())
				m.downloadsStopped = false
				t.Cleanup(func() { _ = m.Stop() })
				saved, err = m.queue.GetTorrent(entry.InfoHash)
				if err != nil {
					t.Fatal(err)
				}
				if saved.State != storage.EntryStateDownloading || saved.IsDownloading || saved.IsComplete || saved.LastError != "" {
					t.Fatalf("interrupted entry cannot resume: %#v", saved)
				}
				for name := range torrent.Files {
					if err := os.WriteFile(filepath.Join(mountPath, name), []byte("media"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				m.restoreActiveDownloadJobs()
				m.processQueuedEntries()
				m.downloadTasks.Wait()
				synctest.Wait()
				saved, err = m.queue.GetTorrent(entry.InfoHash)
				if err != nil || !saved.IsComplete || saved.IsDownloading {
					t.Fatalf("resumed entry = %#v, error = %v", saved, err)
				}
				downloadPath := saved.DownloadPath()
				if multiSeason {
					_, seasons := m.downloader.detectMultiSeason(entry)
					for _, season := range convertToMultiSeason(entry, seasons) {
						if season.Files["Show.S02E01.mkv"] != nil {
							downloadPath = season.DownloadPath()
						}
					}
					if _, err := os.Lstat(filepath.Join(completedSeason.DownloadPath(), "Show.S01E01.mkv")); !os.IsNotExist(err) {
						t.Fatalf("completed season was processed again: %v", err)
					}
				}
				linkPath := filepath.Join(downloadPath, "Show.S02E01.mkv")
				target, err := os.Readlink(linkPath)
				if err != nil || target != filepath.Join(mountPath, "Show.S02E01.mkv") {
					t.Fatalf("restored symlink = %q, error = %v", target, err)
				}
				if err := os.Remove(linkPath); err != nil {
					t.Fatal(err)
				}
				m.restoreActiveDownloadJobs()
				m.processQueuedEntries()
				m.downloadTasks.Wait()
				if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
					t.Fatalf("completed import was processed again: %v", err)
				}
			})
		})
	}
}
