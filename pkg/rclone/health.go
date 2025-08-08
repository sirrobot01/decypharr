package rclone

import (
	"context"
	"fmt"
	"time"
)

// HealthCheck performs comprehensive health checks on the rclone system
func (m *Manager) HealthCheck() error {
	if !m.serverStarted {
		return fmt.Errorf("rclone RC server is not started")
	}

	if !m.IsReady() {
		return fmt.Errorf("rclone RC server is not ready")
	}

	// Check if we can communicate with the server
	if !m.pingServer() {
		return fmt.Errorf("rclone RC server is not responding")
	}

	// Check mounts health
	m.mountsMutex.RLock()
	unhealthyMounts := make([]string, 0)
	for provider, mount := range m.mounts {
		if mount.Mounted && !m.checkMountHealth(provider) {
			unhealthyMounts = append(unhealthyMounts, provider)
		}
	}
	m.mountsMutex.RUnlock()

	if len(unhealthyMounts) > 0 {
		return fmt.Errorf("unhealthy mounts detected: %v", unhealthyMounts)
	}

	return nil
}

// checkMountHealth checks if a specific mount is healthy
func (m *Manager) checkMountHealth(provider string) bool {
	// Try to list the root directory of the mount
	req := RCRequest{
		Command: "operations/list",
		Args: map[string]interface{}{
			"fs":     fmt.Sprintf("decypharr-%s:", provider),
			"remote": "/",
		},
	}

	_, err := m.makeRequest(req)
	return err == nil
}

// RecoverMount attempts to recover a failed mount
func (m *Manager) RecoverMount(provider string) error {
	m.mountsMutex.RLock()
	mountInfo, exists := m.mounts[provider]
	m.mountsMutex.RUnlock()

	if !exists {
		return fmt.Errorf("mount for provider %s does not exist", provider)
	}

	m.logger.Warn().Str("provider", provider).Msg("Attempting to recover mount")

	// First try to unmount cleanly
	if err := m.unmount(provider); err != nil {
		m.logger.Error().Err(err).Str("provider", provider).Msg("Failed to unmount during recovery")
	}

	// Wait a moment
	time.Sleep(2 * time.Second)

	// Try to remount
	if err := m.Mount(provider, mountInfo.WebDAVURL); err != nil {
		return fmt.Errorf("failed to recover mount for %s: %w", provider, err)
	}

	m.logger.Info().Str("provider", provider).Msg("Successfully recovered mount")
	return nil
}

// MonitorMounts continuously monitors mount health and attempts recovery
func (m *Manager) MonitorMounts(ctx context.Context) {
	if !m.serverStarted {
		return
	}

	ticker := time.NewTicker(30 * time.Second) // Check every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			m.logger.Debug().Msg("Mount monitoring stopped")
			return
		case <-ticker.C:
			m.performMountHealthCheck()
		}
	}
}

// performMountHealthCheck checks and attempts to recover unhealthy mounts
func (m *Manager) performMountHealthCheck() {
	if !m.IsReady() {
		return
	}

	m.mountsMutex.RLock()
	providers := make([]string, 0, len(m.mounts))
	for provider, mount := range m.mounts {
		if mount.Mounted {
			providers = append(providers, provider)
		}
	}
	m.mountsMutex.RUnlock()

	for _, provider := range providers {
		if !m.checkMountHealth(provider) {
			m.logger.Warn().Str("provider", provider).Msg("Mount health check failed, attempting recovery")

			// Mark mount as unhealthy
			m.mountsMutex.Lock()
			if mount, exists := m.mounts[provider]; exists {
				mount.Error = "Health check failed"
				mount.Mounted = false
			}
			m.mountsMutex.Unlock()

			// Attempt recovery
			go func(provider string) {
				if err := m.RecoverMount(provider); err != nil {
					m.logger.Error().Err(err).Str("provider", provider).Msg("Failed to recover mount")
				}
			}(provider)
		}
	}
}
