package dfs

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/backend"
	_ "github.com/sirrobot01/decypharr/pkg/mount/dfs/backend/register"
	fuseconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
)

// Manager manages FUSE filesystem instances with proper caching
type Manager struct {
	manager            *manager.Manager
	logger             zerolog.Logger
	ready              atomic.Bool
	backend            backend.Backend
	defaultBackendType backend.Type
	config             *fuseconfig.FuseConfig
	vfs                *vfs.Manager
}

// NewManager creates a new  FUSE filesystem manager
func NewManager(manager *manager.Manager) *Manager {
	fuseConfig := fuseconfig.ParseFuseConfig()

	m := &Manager{
		manager:            manager,
		logger:             logger.New("dfs"),
		defaultBackendType: backend.GetDefaultBackendType(),
		config:             fuseConfig,
	}
	return m
}

// Start starts the FUSE filesystem manager
func (m *Manager) Start(ctx context.Context) error {
	// Create VFS manager

	m.logger.Info().
		Str("mount_path", m.config.MountPath).
		Str("backend", string(m.defaultBackendType)).
		Msg("Starting DFS with backend")

	vfsManager, err := vfs.NewManager(context.Background(), m.manager, m.config)
	if err != nil {
		return fmt.Errorf("failed to create VFS manager: %w", err)
	}
	m.vfs = vfsManager

	// Create backend
	bck, err := backend.New(m.defaultBackendType, vfsManager, m.config)
	if err != nil {
		return fmt.Errorf("failed to create backend: %w", err)
	}
	m.backend = bck

	// Mount using the backend
	if err := m.backend.Mount(ctx); err != nil {
		return fmt.Errorf("backend mount failed: %w", err)
	}

	m.ready.Store(true)
	m.logger.Info().
		Str("mount_path", m.config.MountPath).
		Str("backend", string(m.defaultBackendType)).
		Msg("DFS started successfully")
	return nil
}

// Stop stops the  FUSE filesystem manager
func (m *Manager) Stop() error {
	if m.backend == nil {
		m.logger.Info().Msg("Backend not initialized, nothing to stop")
		return nil
	}
	m.logger.Info().
		Str("backend", string(m.backend.Type())).
		Msg("Stopping FUSE filesystem")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Unmount using backend, this also ensures the VFS manager is properly closed
	if err := m.backend.Unmount(ctx); err != nil {
		m.logger.Warn().Err(err).Msg("Backend unmount error")
	}
	m.ready.Store(false)
	return nil
}

func (m *Manager) IsReady() bool {
	return m.ready.Load()
}

func (m *Manager) Refresh(dirs []string) error {
	if m.backend == nil {
		return fmt.Errorf("backend not initialized")
	}
	for _, dir := range dirs {
		m.backend.Refresh(dir)
	}
	return nil
}

// Stats returns unified statistics across all DFS mounts
func (m *Manager) Stats() map[string]interface{} {
	// Aggregate stats from all mounts
	stats := map[string]interface{}{
		"enabled": true,
		"ready":   m.ready.Load(),
		"type":    m.Type(),
		"backend": string(m.defaultBackendType),
	}
	if m.vfs != nil {
		for key, value := range m.vfs.GetStats() {
			stats[key] = value
		}
	}
	return stats
}

func (m *Manager) CleanupCache() (map[string]interface{}, error) {
	if m.vfs == nil {
		return nil, fmt.Errorf("VFS manager is not initialized")
	}
	return m.vfs.CleanupCache(), nil
}

func (m *Manager) PurgeCache() (map[string]interface{}, error) {
	if m.vfs == nil {
		return nil, fmt.Errorf("VFS manager is not initialized")
	}
	return m.vfs.PurgeCache(), nil
}

func (m *Manager) Type() string {
	return "dfs"
}
