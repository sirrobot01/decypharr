package store

import (
	"cmp"
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
	repair            *repair.Repair
	arr               *arr.Storage
	debrid            *debrid.Storage
	torrents          *TorrentStorage
	logger            zerolog.Logger
	refreshInterval   time.Duration
	skipPreCache      bool
	downloadSemaphore chan struct{}
}

var (
	instance *Store
	once     sync.Once
)

// GetStore returns the singleton instance
func GetStore() *Store {
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
		}
	})
	return instance
}

func Reset() {
	if instance != nil {
		if instance.debrid != nil {
			instance.debrid.Reset()
		}
		close(instance.downloadSemaphore)
	}
	once = sync.Once{}
	instance = nil
}

func (s *Store) GetArr() *arr.Storage {
	return s.arr
}
func (s *Store) GetDebrid() *debrid.Storage {
	return s.debrid
}
func (s *Store) GetRepair() *repair.Repair {
	return s.repair
}
func (s *Store) GetTorrentStorage() *TorrentStorage {
	return s.torrents
}
