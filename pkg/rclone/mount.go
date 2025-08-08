package rclone

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"net/url"
	"path/filepath"
)

// Mount represents a mount using the rclone RC client
type Mount struct {
	Provider  string
	LocalPath string
	WebDAVURL string
	logger    zerolog.Logger
	rcManager *Manager
}

// NewMount creates a new RC-based mount
func NewMount(provider, webdavURL string, rcManager *Manager) *Mount {
	cfg := config.Get()
	mountPath := filepath.Join(cfg.Rclone.MountPath, provider)

	_url, err := url.JoinPath(webdavURL, provider)
	if err != nil {
		_url = fmt.Sprintf("%s/%s", webdavURL, provider)
	}

	return &Mount{
		Provider:  provider,
		LocalPath: mountPath,
		WebDAVURL: _url,
		rcManager: rcManager,
		logger:    rcManager.GetLogger(),
	}
}

// Mount creates the mount using rclone RC
func (m *Mount) Mount(ctx context.Context) error {
	if m.rcManager == nil {
		return fmt.Errorf("rclone manager is not available")
	}

	// Check if already mounted
	if m.rcManager.IsMounted(m.Provider) {
		m.logger.Info().Msgf("Mount %s is already mounted at %s", m.Provider, m.LocalPath)
		return nil
	}

	m.logger.Info().
		Str("provider", m.Provider).
		Str("webdav_url", m.WebDAVURL).
		Str("mount_path", m.LocalPath).
		Msg("Creating mount via RC")

	if err := m.rcManager.Mount(m.Provider, m.WebDAVURL); err != nil {
		m.logger.Error().Str("provider", m.Provider).Msg("Mount operation failed")
		return fmt.Errorf("mount failed for %s", m.Provider)
	}

	m.logger.Info().Msgf("Successfully mounted %s WebDAV at %s via RC", m.Provider, m.LocalPath)
	return nil
}

// Unmount removes the mount using rclone RC
func (m *Mount) Unmount() error {
	if m.rcManager == nil {
		m.logger.Warn().Msg("Rclone manager is not available, skipping unmount")
		return nil
	}

	if !m.rcManager.IsMounted(m.Provider) {
		m.logger.Info().Msgf("Mount %s is not mounted, skipping unmount", m.Provider)
		return nil
	}

	m.logger.Info().Str("provider", m.Provider).Msg("Unmounting via RC")

	if err := m.rcManager.Unmount(m.Provider); err != nil {
		return fmt.Errorf("failed to unmount %s via RC: %w", m.Provider, err)
	}

	m.logger.Info().Msgf("Successfully unmounted %s", m.Provider)
	return nil
}

// IsMounted checks if the mount is active via RC
func (m *Mount) IsMounted() bool {
	if m.rcManager == nil {
		return false
	}
	return m.rcManager.IsMounted(m.Provider)
}

// RefreshDir refreshes directories in the mount
func (m *Mount) RefreshDir(dirs []string) error {
	if m.rcManager == nil {
		return fmt.Errorf("rclone manager is not available")
	}

	if !m.IsMounted() {
		return fmt.Errorf("provider %s not properly mounted. Skipping refreshes", m.Provider)
	}

	if err := m.rcManager.RefreshDir(m.Provider, dirs); err != nil {
		return fmt.Errorf("failed to refresh directories for %s: %w", m.Provider, err)
	}

	return nil
}

// GetMountInfo returns mount information
func (m *Mount) GetMountInfo() (*MountInfo, bool) {
	if m.rcManager == nil {
		return nil, false
	}
	return m.rcManager.GetMountInfo(m.Provider)
}
