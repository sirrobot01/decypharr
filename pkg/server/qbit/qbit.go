package qbit

import (
	"github.com/rs/zerolog"
	"github.com/dylanmazurek/decypharr/internal/config"
	"github.com/dylanmazurek/decypharr/internal/logger"
	"github.com/dylanmazurek/decypharr/pkg/manager"
)

type QBit struct {
	downloadFolder          string
	categories              []string
	alwaysRemoveTrackerURLS bool
	logger                  zerolog.Logger
	Tags                    []string
	manager                 *manager.Manager
}

func New(manager *manager.Manager) *QBit {
	cfg := config.Get()
	return &QBit{
		downloadFolder:          cfg.DownloadFolder,
		categories:              cfg.Categories,
		alwaysRemoveTrackerURLS: cfg.AlwaysRmTrackerUrls,
		manager:                 manager,
		logger:                  logger.New("qbit"),
	}
}
