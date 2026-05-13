package external

import (
	"context"
	"fmt"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/rclone"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

type Manager struct {
	manager       *manager.Manager
	client        *rclone.Client
	logger        zerolog.Logger
	webdavEnabled bool
}

// NewManager creates a new external rclone manager
// This does nothing, just a placeholder to satisfy the interface
func NewManager(manager *manager.Manager) *Manager {
	_logger := logger.New("external")
	cfg := config.Get()
	rcloneClient := rclone.NewClient(
		cfg.Mount.ExternalRclone.RCUrl,
		cfg.Mount.ExternalRclone.RCUsername,
		cfg.Mount.ExternalRclone.RCPassword,
		_logger,
	)
	m := &Manager{
		manager:       manager,
		logger:        _logger,
		client:        rcloneClient,
		webdavEnabled: !cfg.DisableWebDav,
	}
	return m
}

func (m *Manager) Start(ctx context.Context) error {
	if !m.webdavEnabled {
		return fmt.Errorf("webdav is not enabled")
	}
	return nil
}

func (m *Manager) Stop() error {
	return nil
}

func (m *Manager) Refresh(dirs []string) error {
	return m.client.Refresh(context.Background(), dirs, "")
}

func (m *Manager) IsReady() bool {
	return true
}

func (m *Manager) Type() string {
	return "external"
}
