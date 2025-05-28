package qbit

import (
	"cmp"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"os"
	"path/filepath"
)

type QBit struct {
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	Port            string   `json:"port"`
	DownloadFolder  string   `json:"download_folder"`
	Categories      []string `json:"categories"`
	Storage         *TorrentStorage
	logger          zerolog.Logger
	Tags            []string
	RefreshInterval int
	SkipPreCache    bool

	downloadSemaphore chan struct{}
}

func New() *QBit {
	_cfg := config.Get()
	cfg := _cfg.QBitTorrent
	port := cmp.Or(_cfg.Port, os.Getenv("QBIT_PORT"), "8282")
	refreshInterval := cmp.Or(cfg.RefreshInterval, 10)
	return &QBit{
		Username:          cfg.Username,
		Password:          cfg.Password,
		Port:              port,
		DownloadFolder:    cfg.DownloadFolder,
		Categories:        cfg.Categories,
		Storage:           NewTorrentStorage(filepath.Join(_cfg.Path, "torrents.json")),
		logger:            logger.New("qbit"),
		RefreshInterval:   refreshInterval,
		SkipPreCache:      cfg.SkipPreCache,
		downloadSemaphore: make(chan struct{}, cmp.Or(cfg.MaxDownloads, 5)),
	}
}

func (q *QBit) Reset() {
	if q.Storage != nil {
		q.Storage.Reset()
	}
	q.Tags = nil
	close(q.downloadSemaphore)
}
