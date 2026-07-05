package rclone

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/retry"
	"github.com/sirrobot01/decypharr/internal/utils"
	"golang.org/x/net/context"
)

// mountWithRetry attempts to mount with retry logic using avast/retry-go
func (m *Manager) mountWithRetry(ctx context.Context, maxRetries int) error {
	return retry.Do(
		func() error {
			return m.performMount(ctx)
		},
		retry.Attempts(uint(maxRetries)+1),
		retry.Delay(config.DefaultRetryDelay),
		retry.DelayType(retry.FixedDelay),
		retry.LastErrorOnly(true),
		retry.RetryIf(func(err error) bool {
			return true // Always retry on error
		}),
	)
}

// performMount performs a single mount attempt
func (m *Manager) performMount(ctx context.Context) error {
	cfg := config.Get().Mount

	// Create mount directory if not on windows
	if runtime.GOOS != "windows" {
		_ = os.MkdirAll(cfg.MountPath, 0755)
	}

	// Check if already mounted
	mountInfo := m.getMountInfo()

	if mountInfo != nil && mountInfo.Mounted {
		m.logger.Info().Msg("Already mounted")
		return nil
	}

	// Clean up any stale mount first
	if mountInfo != nil && !mountInfo.Mounted {
		err := m.forceUnmount(ctx)
		if err != nil {
			return err
		}
	}

	// Create rclone config for this provider
	if err := m.createConfig(); err != nil {
		return fmt.Errorf("failed to create rclone config: %w", err)
	}

	// Prepare mount arguments
	mountArgs := map[string]any{
		"fs":         FSName,
		"mountPoint": cfg.MountPath,
	}
	mountOpt := map[string]any{
		"AllowNonEmpty": true,
		"AllowOther":    true,
		"DebugFUSE":     false,
		"DeviceName":    "decypharr",
		"VolumeName":    "decypharr",
	}

	if cfg.Rclone.AsyncRead != nil {
		mountOpt["AsyncRead"] = *cfg.Rclone.AsyncRead
	}

	if cfg.Rclone.UseMmap {
		mountOpt["UseMmap"] = cfg.Rclone.UseMmap
	}

	if cfg.Rclone.Transfers != 0 {
		mountOpt["Transfers"] = cfg.Rclone.Transfers
	}

	configOpts := make(map[string]any)

	if cfg.Rclone.BufferSize != "" {
		configOpts["BufferSize"] = cfg.Rclone.BufferSize
	}

	if len(configOpts) > 0 {
		// Only add _config if there are options to set
		mountArgs["_config"] = configOpts
	}
	vfsOpt := map[string]any{
		"CacheMode":    cfg.Rclone.VfsCacheMode,
		"DirCacheTime": cfg.Rclone.DirCacheTime,
	}
	vfsOpt["PollInterval"] = 0 // Poll interval not supported for webdav, set to 0

	// AddOrUpdate VFS options if caching is enabled
	if cfg.Rclone.VfsCacheMode != "off" {

		if cfg.Rclone.VfsCacheMaxAge != "" {
			vfsOpt["CacheMaxAge"] = cfg.Rclone.VfsCacheMaxAge
		}
		if cfg.Rclone.VfsDiskSpaceTotal != "" {
			vfsOpt["DiskSpaceTotalSize"] = cfg.Rclone.VfsDiskSpaceTotal
		}
		if cfg.Rclone.VfsReadChunkSizeLimit != "" {
			vfsOpt["ChunkSizeLimit"] = cfg.Rclone.VfsReadChunkSizeLimit
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

		if cfg.Rclone.VfsCacheMinFreeSpace != "" {
			vfsOpt["CacheMinFreeSpace"] = cfg.Rclone.VfsCacheMinFreeSpace
		}

		if cfg.Rclone.VfsFastFingerprint {
			vfsOpt["FastFingerprint"] = cfg.Rclone.VfsFastFingerprint
		}

		if cfg.Rclone.VfsReadChunkStreams != 0 {
			vfsOpt["ChunkStreams"] = cfg.Rclone.VfsReadChunkStreams
		}

		if cfg.Rclone.NoChecksum {
			vfsOpt["NoChecksum"] = cfg.Rclone.NoChecksum
		}
		if cfg.Rclone.NoModTime {
			vfsOpt["NoModTime"] = cfg.Rclone.NoModTime
		}
	}

	// AddOrUpdate mount options based on configuration
	if cfg.Rclone.UID != 0 {
		vfsOpt["UID"] = cfg.Rclone.UID
	}
	if cfg.Rclone.GID != 0 {
		vfsOpt["GID"] = cfg.Rclone.GID
	}

	if cfg.Rclone.Umask != "" {
		umask, err := strconv.ParseInt(cfg.Rclone.Umask, 8, 32)
		if err == nil {
			vfsOpt["Umask"] = uint32(umask)
		}
	}

	if cfg.Rclone.AttrTimeout != "" {
		if attrTimeout, err := utils.ParseDuration(cfg.Rclone.AttrTimeout); err == nil {
			mountOpt["AttrTimeout"] = attrTimeout.String()
		}
	}

	mountArgs["vfsOpt"] = vfsOpt
	mountArgs["mountOpt"] = mountOpt

	if err := m.client.Mount(ctx, mountArgs); err != nil {
		_ = m.forceUnmount(ctx)
		return fmt.Errorf("failed to mount %s via RC: %w", cfg.MountPath, err)
	}

	// Store mount info
	mntInfo := &MountInfo{
		LocalPath:  cfg.MountPath,
		WebDAVURL:  m.webdavURL,
		Mounted:    true,
		MountedAt:  time.Now().Format(time.RFC3339),
		ConfigName: ConfigName,
	}

	m.info.Store(mntInfo)

	return nil
}

// unmount is the internal unmount function
func (m *Manager) unmount(ctx context.Context) {
	mountInfo := m.getMountInfo()

	if mountInfo == nil || !mountInfo.Mounted {
		m.logger.Info().Msg("Mount not found or already unmounted")
		return
	}

	m.logger.Info().Msg("Unmounting")

	// Try RC unmount first

	err := m.client.Unmount(context.Background(), mountInfo.LocalPath)

	// If RC unmount fails or server is not ready, try force unmount
	if err != nil {
		m.logger.Warn().Err(err).Msg("RC unmount failed, trying force unmount")
		if err := m.forceUnmount(ctx); err != nil {
			m.logger.Error().Err(err).Msg("Force unmount failed")
			// Don't return error here, update the state anyway
		}
	}

	// Update mount info
	mountInfo.Mounted = false
	mountInfo.Error = ""
	if err != nil {
		mountInfo.Error = err.Error()
	}
	m.logger.Info().Msg("Unmount completed")
}

// createConfig creates an rclone config entry for the provider
func (m *Manager) createConfig() error {
	args := map[string]any{
		"name": ConfigName,
		"type": "webdav",
		"parameters": map[string]any{
			"url":             m.webdavURL,
			"vendor":          "other",
			"pacer_min_sleep": "0",
		},
	}
	if err := m.client.CreateConfig(context.Background(), args); err != nil {
		return fmt.Errorf("failed to create rclone config: %w", err)
	}
	return nil
}

// forceUnmount attempts to force unmount a path using system commands
func (m *Manager) forceUnmount(ctx context.Context) error {
	mountPath := config.Get().Mount.MountPath
	methods := [][]string{
		{"umount", mountPath},
		{"umount", "-l", mountPath}, // lazy unmount
		{"fusermount", "-uz", mountPath},
		{"fusermount3", "-uz", mountPath},
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for _, method := range methods {
		if err := m.tryUnmountCommand(ctx, method...); err == nil {
			m.logger.Info().
				Strs("command", method).
				Msg("Successfully unmounted using system command")
			return nil
		}
	}

	return fmt.Errorf("all force unmount attempts failed for %s", mountPath)
}

// tryUnmountCommand tries to run an unmount command
func (m *Manager) tryUnmountCommand(ctx context.Context, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("no command provided")
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	return cmd.Run()
}
