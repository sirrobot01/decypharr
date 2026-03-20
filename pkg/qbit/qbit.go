package qbit

import (
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/wire"
)

type QBit struct {
	Username            string
	Password            string
	DownloadFolder      string
	Categories          []string
	AlwaysRmTrackerUrls bool
	storage             *wire.TorrentStorage
	logger              zerolog.Logger
	Tags                []string
	jobQueue            chan func()
}

func New() *QBit {
	_cfg := config.Get()
	cfg := _cfg.QBitTorrent
	q := &QBit{
		Username:            cfg.Username,
		Password:            cfg.Password,
		DownloadFolder:      cfg.DownloadFolder,
		Categories:          cfg.Categories,
		AlwaysRmTrackerUrls: cfg.AlwaysRmTrackerUrls,
		storage:             wire.Get().Torrents(),
		logger:              logger.New("qbit"),
		jobQueue:            make(chan func(), 1000), // Bounded buffer
	}

	workers := cfg.Workers
	if workers <= 0 {
		workers = 10
	}

	for i := 0; i < workers; i++ {
		go func() {
			for job := range q.jobQueue {
				job() // Execute the queued job
			}
		}()
	}

	return q
}

func (q *QBit) Reset() {
	if q.storage != nil {
		q.storage.Reset()
	}
	q.Tags = nil
}
