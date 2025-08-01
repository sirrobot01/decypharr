package qbit

import (
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/store"
)

type QBit struct {
	Username       string
	Password       string
	DownloadFolder string
	Categories     []string
	storage        *store.TorrentStorage
	logger         zerolog.Logger
	Tags           []string
}

func New() *QBit {
	cfg := config.Get()
	qbitCfg := cfg.QBitTorrent
	if qbitCfg == nil {
		return nil
	}
	return &QBit{
		Username:       qbitCfg.Username,
		Password:       qbitCfg.Password,
		DownloadFolder: qbitCfg.DownloadFolder,
		Categories:     qbitCfg.Categories,
		storage:        store.Get().Torrents(),
		logger:         logger.New("qbit"),
	}
}

func (q *QBit) Reset() {
	if q.storage != nil {
		q.storage.Reset()
	}
	q.Tags = nil
}
