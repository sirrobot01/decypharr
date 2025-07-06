package qbit

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/store"
)

// StoreInterface defines the store operations that QBit needs
type StoreInterface interface {
	AddTorrent(ctx context.Context, req *store.ImportRequest) error
}

type QBit struct {
	Username            string
	Password            string
	DownloadFolder      string
	Categories          []string
	AlwaysRmTrackerUrls bool
	storage             *store.TorrentStorage
	logger              zerolog.Logger
	Tags                []string
	store               StoreInterface // Add store dependency
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
		storage:             store.Get().Torrents(),
		logger:              logger.New("qbit"),
		Tags:                []string{},
		store:               store.Get(), // Inject the real store
	}
}

// NewWithDependencies creates a QBit instance with injected dependencies (for testing and flexibility)
func NewWithDependencies(storeInterface StoreInterface, storage *store.TorrentStorage, logger zerolog.Logger, username, password, downloadFolder string, categories []string, alwaysRmTrackerUrls bool) *QBit {
	return &QBit{
		Username:            username,
		Password:            password,
		DownloadFolder:      downloadFolder,
		Categories:          categories,
		AlwaysRmTrackerUrls: alwaysRmTrackerUrls,
		storage:             storage,
		logger:              logger,
		Tags:                []string{},
		store:               storeInterface,
	}
}

func (q *QBit) Reset() {
	if q.storage != nil {
		q.storage.Reset()
	}
	q.Tags = nil
}
