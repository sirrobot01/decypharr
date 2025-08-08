package rclone

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

// Mount creates a mount using the rclone RC API with retry logic
func (m *Manager) Mount(provider, webdavURL string) error {
	return m.mountWithRetry(provider, webdavURL, 3)
}

// mountWithRetry attempts to mount with retry logic
func (m *Manager) mountWithRetry(provider, webdavURL string, maxRetries int) error {
	if !m.IsReady() {
		if err := m.WaitForReady(30 * time.Second); err != nil {
			return fmt.Errorf("rclone RC server not ready: %w", err)
		}
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Wait before retry
			wait := time.Duration(attempt*2) * time.Second
			m.logger.Debug().
				Int("attempt", attempt).
				Str("provider", provider).
				Msg("Retrying mount operation")
			time.Sleep(wait)
		}

		if err := m.performMount(provider, webdavURL); err != nil {
			m.logger.Error().
				Err(err).
				Str("provider", provider).
				Int("attempt", attempt+1).
				Msg("Mount attempt failed")
			continue
		}

		return nil // Success
	}
	return fmt.Errorf("mount failed for %s", provider)
}

// performMount performs a single mount attempt
func (m *Manager) performMount(provider, webdavURL string) error {
	cfg := config.Get()
	mountPath := filepath.Join(cfg.Rclone.MountPath, provider)
	cacheDir := ""
	if cfg.Rclone.CacheDir != "" {
		cacheDir = filepath.Join(cfg.Rclone.CacheDir, provider)
	}

	// Create mount directory
	if err := os.MkdirAll(mountPath, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory %s: %w", mountPath, err)
	}

	// Check if already mounted
	m.mountsMutex.RLock()
	existingMount, exists := m.mounts[provider]
	m.mountsMutex.RUnlock()

	if exists && existingMount.Mounted {
		m.logger.Info().Str("provider", provider).Str("path", mountPath).Msg("Already mounted")
		return nil
	}

	// Clean up any stale mount first
	if exists && !existingMount.Mounted {
		m.forceUnmountPath(mountPath)
	}

	// Create rclone config for this provider
	configName := fmt.Sprintf("decypharr-%s", provider)
	if err := m.createConfig(configName, webdavURL); err != nil {
		return fmt.Errorf("failed to create rclone config: %w", err)
	}

	// Prepare mount arguments
	mountArgs := map[string]interface{}{
		"fs":         fmt.Sprintf("%s:", configName),
		"mountPoint": mountPath,
		"mountType":  "mount", // Use standard FUSE mount
		"mountOpt": map[string]interface{}{
			"AllowNonEmpty": true,
			"AllowOther":    true,
			"DebugFUSE":     false,
			"DeviceName":    fmt.Sprintf("decypharr-%s", provider),
			"VolumeName":    fmt.Sprintf("decypharr-%s", provider),
		},
	}

	configOpts := map[string]interface{}{
		"BufferSize": cfg.Rclone.BufferSize,
	}

	if cacheDir != "" {
		// Create cache directory if specified
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			m.logger.Warn().Str("cacheDir", cacheDir).Msg("Failed to create cache directory")
		}
		configOpts["CacheDir"] = cacheDir
	}

	if len(configOpts) > 0 {
		// Only add _config if there are options to set
		mountArgs["_config"] = configOpts
	}

	// Add VFS options if caching is enabled
	if cfg.Rclone.VfsCacheMode != "off" {
		vfsOpt := map[string]interface{}{
			"CacheMode": cfg.Rclone.VfsCacheMode,
		}

		if cfg.Rclone.VfsCacheMaxAge != "" {
			vfsOpt["CacheMaxAge"] = cfg.Rclone.VfsCacheMaxAge
		}
		if cfg.Rclone.VfsCacheMaxSize != "" {
			vfsOpt["CacheMaxSize"] = cfg.Rclone.VfsCacheMaxSize
		}
		if cfg.Rclone.VfsCachePollInterval != "" {
			vfsOpt["CachePollInterval"] = cfg.Rclone.VfsCachePollInterval
		}
		if cfg.Rclone.VfsReadChunkSize != "" {
			vfsOpt["ChunkSize"] = cfg.Rclone.VfsReadChunkSize
		}
		if cfg.Rclone.VfsReadAhead != "" {
			vfsOpt["ReadAhead"] = cfg.Rclone.VfsReadAhead
		}
		if cfg.Rclone.NoChecksum {
			vfsOpt["NoChecksum"] = cfg.Rclone.NoChecksum
		}
		if cfg.Rclone.NoModTime {
			vfsOpt["NoModTime"] = cfg.Rclone.NoModTime
		}

		mountArgs["vfsOpt"] = vfsOpt
	}

	// Add mount options based on configuration
	if cfg.Rclone.UID != 0 {
		mountArgs["mountOpt"].(map[string]interface{})["UID"] = cfg.Rclone.UID
	}
	if cfg.Rclone.GID != 0 {
		mountArgs["mountOpt"].(map[string]interface{})["GID"] = cfg.Rclone.GID
	}
	if cfg.Rclone.AttrTimeout != "" {
		if attrTimeout, err := time.ParseDuration(cfg.Rclone.AttrTimeout); err == nil {
			mountArgs["mountOpt"].(map[string]interface{})["AttrTimeout"] = attrTimeout.String()
		}
	}
	// Make the mount request
	req := RCRequest{
		Command: "mount/mount",
		Args:    mountArgs,
	}

	_, err := m.makeRequest(req)
	if err != nil {
		// Clean up mount point on failure
		m.forceUnmountPath(mountPath)
		return fmt.Errorf("failed to create mount for %s: %w", provider, err)
	}

	// Store mount info
	mountInfo := &MountInfo{
		Provider:   provider,
		LocalPath:  mountPath,
		WebDAVURL:  webdavURL,
		Mounted:    true,
		MountedAt:  time.Now().Format(time.RFC3339),
		ConfigName: configName,
	}

	m.mountsMutex.Lock()
	m.mounts[provider] = mountInfo
	m.mountsMutex.Unlock()

	return nil
}

// Unmount unmounts a specific provider
func (m *Manager) Unmount(provider string) error {
	return m.unmount(provider)
}

// unmount is the internal unmount function
func (m *Manager) unmount(provider string) error {
	m.mountsMutex.RLock()
	mountInfo, exists := m.mounts[provider]
	m.mountsMutex.RUnlock()

	if !exists || !mountInfo.Mounted {
		m.logger.Info().Str("provider", provider).Msg("Mount not found or already unmounted")
		return nil
	}

	m.logger.Info().Str("provider", provider).Str("path", mountInfo.LocalPath).Msg("Unmounting")

	// Try RC unmount first
	req := RCRequest{
		Command: "mount/unmount",
		Args: map[string]interface{}{
			"mountPoint": mountInfo.LocalPath,
		},
	}

	var rcErr error
	if m.IsReady() {
		_, rcErr = m.makeRequest(req)
	}

	// If RC unmount fails or server is not ready, try force unmount
	if rcErr != nil {
		m.logger.Warn().Err(rcErr).Str("provider", provider).Msg("RC unmount failed, trying force unmount")
		if err := m.forceUnmountPath(mountInfo.LocalPath); err != nil {
			m.logger.Error().Err(err).Str("provider", provider).Msg("Force unmount failed")
			// Don't return error here, update the state anyway
		}
	}

	// Update mount info
	m.mountsMutex.Lock()
	if info, exists := m.mounts[provider]; exists {
		info.Mounted = false
		info.Error = ""
		if rcErr != nil {
			info.Error = rcErr.Error()
		}
	}
	m.mountsMutex.Unlock()

	m.logger.Info().Str("provider", provider).Msg("Unmount completed")
	return nil
}

// UnmountAll unmounts all mounts
func (m *Manager) UnmountAll() error {
	m.mountsMutex.RLock()
	providers := make([]string, 0, len(m.mounts))
	for provider, mount := range m.mounts {
		if mount.Mounted {
			providers = append(providers, provider)
		}
	}
	m.mountsMutex.RUnlock()

	var lastError error
	for _, provider := range providers {
		if err := m.unmount(provider); err != nil {
			lastError = err
			m.logger.Error().Err(err).Str("provider", provider).Msg("Failed to unmount")
		}
	}

	return lastError
}

// GetMountInfo returns information about a specific mount
func (m *Manager) GetMountInfo(provider string) (*MountInfo, bool) {
	m.mountsMutex.RLock()
	defer m.mountsMutex.RUnlock()

	info, exists := m.mounts[provider]
	if !exists {
		return nil, false
	}

	// Create a copy to avoid race conditions
	mountInfo := *info
	return &mountInfo, true
}

// GetAllMounts returns information about all mounts
func (m *Manager) GetAllMounts() map[string]*MountInfo {
	m.mountsMutex.RLock()
	defer m.mountsMutex.RUnlock()

	result := make(map[string]*MountInfo, len(m.mounts))
	for provider, info := range m.mounts {
		// Create a copy to avoid race conditions
		mountInfo := *info
		result[provider] = &mountInfo
	}

	return result
}

// IsMounted checks if a provider is mounted
func (m *Manager) IsMounted(provider string) bool {
	info, exists := m.GetMountInfo(provider)
	return exists && info.Mounted
}

// RefreshDir refreshes directories in the VFS cache
func (m *Manager) RefreshDir(provider string, dirs []string) error {
	if !m.IsReady() {
		return fmt.Errorf("rclone RC server not ready")
	}

	mountInfo, exists := m.GetMountInfo(provider)
	if !exists || !mountInfo.Mounted {
		return fmt.Errorf("provider %s not mounted", provider)
	}

	// If no specific directories provided, refresh root
	if len(dirs) == 0 {
		dirs = []string{"/"}
	}
	args := map[string]interface{}{
		"fs": fmt.Sprintf("decypharr-%s:", provider),
	}
	for i, dir := range dirs {
		if dir != "" {
			if i == 0 {
				args["dir"] = dir
			} else {
				args[fmt.Sprintf("dir%d", i+1)] = dir
			}
		}
	}
	req := RCRequest{
		Command: "vfs/forget",
		Args:    args,
	}

	_, err := m.makeRequest(req)
	if err != nil {
		m.logger.Error().Err(err).
			Str("provider", provider).
			Msg("Failed to refresh directory")
		return fmt.Errorf("failed to refresh directory %s for provider %s: %w", dirs, provider, err)
	}

	req = RCRequest{
		Command: "vfs/refresh",
		Args:    args,
	}

	_, err = m.makeRequest(req)
	if err != nil {
		m.logger.Error().Err(err).
			Str("provider", provider).
			Msg("Failed to refresh directory")
		return fmt.Errorf("failed to refresh directory %s for provider %s: %w", dirs, provider, err)
	}
	return nil
}

// createConfig creates an rclone config entry for the provider
func (m *Manager) createConfig(configName, webdavURL string) error {
	req := RCRequest{
		Command: "config/create",
		Args: map[string]interface{}{
			"name": configName,
			"type": "webdav",
			"parameters": map[string]interface{}{
				"url":             webdavURL,
				"vendor":          "other",
				"pacer_min_sleep": "0",
			},
		},
	}

	_, err := m.makeRequest(req)
	if err != nil {
		return fmt.Errorf("failed to create config %s: %w", configName, err)
	}

	m.logger.Trace().
		Str("config_name", configName).
		Str("webdav_url", webdavURL).
		Msg("Rclone config created")

	return nil
}

// forceUnmountPath attempts to force unmount a path using system commands
func (m *Manager) forceUnmountPath(mountPath string) error {
	methods := [][]string{
		{"umount", mountPath},
		{"umount", "-l", mountPath}, // lazy unmount
		{"fusermount", "-uz", mountPath},
		{"fusermount3", "-uz", mountPath},
	}

	for _, method := range methods {
		if err := m.tryUnmountCommand(method...); err == nil {
			m.logger.Info().
				Strs("command", method).
				Str("path", mountPath).
				Msg("Successfully unmounted using system command")
			return nil
		}
	}

	return fmt.Errorf("all force unmount attempts failed for %s", mountPath)
}

// tryUnmountCommand tries to run an unmount command
func (m *Manager) tryUnmountCommand(args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command provided")
	}

	cmd := exec.CommandContext(m.ctx, args[0], args[1:]...)
	return cmd.Run()
}
