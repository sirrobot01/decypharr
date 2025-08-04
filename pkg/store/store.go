package store

import (
	"cmp"
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
		qbitCfg := cfg.QBitTorrent

		instance = &Store{
			repair:            repair.New(arrs, deb),
			arr:               arrs,
			debrid:            deb,
			torrents:          newTorrentStorage(cfg.TorrentsFile()),
			logger:            logger.Default(), // Use default logger [decypharr]
			refreshInterval:   time.Duration(cmp.Or(qbitCfg.RefreshInterval, 10)) * time.Minute,
			skipPreCache:      qbitCfg.SkipPreCache,
			downloadSemaphore: make(chan struct{}, cmp.Or(qbitCfg.MaxDownloads, 5)),
			importsQueue:      NewImportQueue(context.Background(), 1000),
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
		if instance.downloadSemaphore != nil {
			// Close the semaphore channel to
			close(instance.downloadSemaphore)
		}
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
