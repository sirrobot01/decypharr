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
}

func New() *QBit {
	_cfg := config.Get()
	cfg := _cfg.QBitTorrent
	return &QBit{
		Username:            cfg.Username,
		Password:            cfg.Password,
		DownloadFolder:      cfg.DownloadFolder,
		Categories:          cfg.Categories,
		AlwaysRmTrackerUrls: cfg.AlwaysRmTrackerUrls,
		storage:             wire.Get().Torrents(),
		logger:         	 logger.New("qbit"),
	}
}

func (q *QBit) Reset() {
	if q.storage != nil {
		q.storage.Reset()
	}
	q.Tags = nil
}
