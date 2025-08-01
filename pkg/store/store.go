package store

import (
	"context"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/repair"
	"sync"
	"time"
)

type Store struct {
	repair             *repair.Repair
	arr                *arr.Storage
	debrid             *debrid.Storage
	importsQueue       *ImportQueue // Queued import requests(probably from too_many_active_downloads)
	torrents           *TorrentStorage
	logger             zerolog.Logger
	refreshInterval    time.Duration
	skipPreCache       bool
	downloadSemaphore  chan struct{}
	removeStalledAfter time.Duration // Duration after which stalled torrents are removed
}

var (
	instance *Store
	once     sync.Once
)

// Get returns the singleton instance
func Get() *Store {
	once.Do(func() {
		arrs := arr.NewStorage()
		deb := debrid.NewStorage()
		cfg := config.Get()
		instance = &Store{
			repair:            repair.New(arrs, deb),
			arr:               arrs,
			debrid:            deb,
			torrents:          newTorrentStorage(cfg.TorrentsFile()),
			logger:            logger.Default(), // Use default logger [decypharr]
			importsQueue:      NewImportQueue(context.Background(), 1000),
			refreshInterval:   10 * time.Minute,       // Default refresh interval
			skipPreCache:      false,                  // Default skip pre-cache
			downloadSemaphore: make(chan struct{}, 5), // Default max concurrent downloads
		}
		if cfg.QBitTorrent != nil {
			instance.refreshInterval = time.Duration(cfg.QBitTorrent.RefreshInterval) * time.Minute
			instance.skipPreCache = cfg.QBitTorrent.SkipPreCache
			instance.downloadSemaphore = make(chan struct{}, cfg.QBitTorrent.MaxDownloads)
		}
		if cfg.RemoveStalledAfter != "" {
			removeStalledAfter, err := time.ParseDuration(cfg.RemoveStalledAfter)
			if err == nil {
				instance.removeStalledAfter = removeStalledAfter
			}
		}
	})
	return instance
}

func Reset() {
	if instance != nil {
		if instance.debrid != nil {
			instance.debrid.Reset()
		}

		if instance.importsQueue != nil {
			instance.importsQueue.Close()
		}

		close(instance.downloadSemaphore)
	}
	once = sync.Once{}
	instance = nil
}

func (s *Store) Arr() *arr.Storage {
	return s.arr
}
func (s *Store) Debrid() *debrid.Storage {
	return s.debrid
}
func (s *Store) Repair() *repair.Repair {
	return s.repair
}
func (s *Store) Torrents() *TorrentStorage {
	return s.torrents
}
