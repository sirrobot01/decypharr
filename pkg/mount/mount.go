package mount

import (
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rclone/rclone/cmd/mountlib"
	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/rclone/rclone/vfs"
	"github.com/rclone/rclone/vfs/vfscommon"

	_ "github.com/rclone/rclone/backend/local"  // Local backend (required for VFS cache)
	_ "github.com/rclone/rclone/backend/webdav" // WebDAV backend
	_ "github.com/rclone/rclone/cmd/cmount"     // Custom mount for macOS
	_ "github.com/rclone/rclone/cmd/mount"      // Standard FUSE mount
	// Try to import available mount backends for macOS
	// These are conditional imports that may or may not work depending on system setup
	_ "github.com/rclone/rclone/cmd/mount2" // Alternative FUSE mount

	configPkg "github.com/sirrobot01/decypharr/internal/config"
)

func getMountFn() (mountlib.MountFn, error) {
	// Try mount methods in order of preference
	for _, method := range []string{"", "mount", "cmount", "mount2"} {
		_, mountFn := mountlib.ResolveMountMethod(method)
		if mountFn != nil {
			return mountFn, nil
		}
	}
	return nil, errors.New("no suitable mount function found")
}

type Mount struct {
	Provider   string
	LocalPath  string
	WebDAVURL  string
	mountPoint *mountlib.MountPoint
	vfs        *vfs.VFS
	cancel     context.CancelFunc
	mounted    atomic.Bool
	logger     zerolog.Logger
}

func NewMount(provider, webdavURL string) *Mount {
	cfg := configPkg.Get()
	_logger := logger.New("mount-" + provider)
	mountPath := filepath.Join(cfg.Rclone.MountPath, provider)
	_url, err := url.JoinPath(webdavURL, provider)
	if err != nil {
		_url = fmt.Sprintf("%s/%s", webdavURL, provider)
	}

	// Get mount function to validate if FUSE is available
	mountFn, err := getMountFn()
	if err != nil || mountFn == nil {
		_logger.Warn().Err(err).Msgf("Mount function not available for %s, using WebDAV URL %s", provider, _url)
		return nil
	}
	return &Mount{
		Provider:  provider,
		LocalPath: mountPath,
		WebDAVURL: _url,
		logger:    _logger,
	}
}

func (m *Mount) Mount(ctx context.Context) error {
	if m.mounted.Load() {
		m.logger.Info().Msgf("Mount %s is already mounted at %s", m.Provider, m.LocalPath)
		return nil
	}

	if err := os.MkdirAll(m.LocalPath, 0755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("failed to create mount directory %s: %w", m.LocalPath, err)
	}

	// Check if the mount point is already busy/mounted
	if m.isMountBusy() {
		m.logger.Warn().Msgf("Mount point %s appears to be busy, attempting cleanup", m.LocalPath)
		if err := m.forceUnmount(); err != nil {
			m.logger.Error().Err(err).Msgf("Failed to cleanup busy mount point %s", m.LocalPath)
			return fmt.Errorf("mount point %s is busy and cleanup failed: %w", m.LocalPath, err)
		}
		m.logger.Info().Msgf("Successfully cleaned up busy mount point %s", m.LocalPath)
	}

	// Create mount context
	mountCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel

	configName := fmt.Sprintf("decypharr_%s", m.Provider)
	if err := setRcloneConfig(configName, m.WebDAVURL); err != nil {
		return fmt.Errorf("failed to set rclone config: %w", err)
	}

	// Check if fusermount3 is available
	if _, err := exec.LookPath("fusermount3"); err != nil {
		m.logger.Info().Msgf("FUSE mounting not available (fusermount3 not found). Files accessible via WebDAV at %s", m.WebDAVURL)
		m.mounted.Store(true) // Mark as "mounted" for WebDAV access
		return nil
	}

	// Get the mount function - try different mount methods
	mountFn, err := getMountFn()
	if err != nil {
		return fmt.Errorf("failed to get mount function for %s: %w", m.Provider, err)
	}

	go func() {
		if err := m.performMount(mountCtx, mountFn, configName); err != nil {
			m.logger.Error().Err(err).Msgf("Failed to mount %s at %s", m.Provider, m.LocalPath)
			return
		}
		m.mounted.Store(true)
		m.logger.Info().Msgf("Successfully mounted %s WebDAV at %s", m.Provider, m.LocalPath)

		// Wait for context cancellation
		<-mountCtx.Done()
	}()
	m.logger.Info().Msgf("Mount process started for %s at %s", m.Provider, m.LocalPath)
	return nil
}

func setRcloneConfig(configName, webdavURL string) error {
	// Set configuration in rclone's config system using FileSetValue
	config.FileSetValue(configName, "type", "webdav")
	config.FileSetValue(configName, "url", webdavURL)
	config.FileSetValue(configName, "vendor", "other")
	return nil
}

func (m *Mount) performMount(ctx context.Context, mountfn mountlib.MountFn, configName string) error {
	// Create filesystem from config
	fsrc, err := fs.NewFs(ctx, fmt.Sprintf("%s:", configName))
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	// Get global rclone config
	cfg := configPkg.Get()
	rcloneOpt := &cfg.Rclone

	// Parse duration strings
	dirCacheTime, _ := time.ParseDuration(rcloneOpt.DirCacheTime)
	attrTimeout, _ := time.ParseDuration(rcloneOpt.AttrTimeout)

	// Parse cache mode
	var cacheMode vfscommon.CacheMode
	switch rcloneOpt.VfsCacheMode {
	case "off":
		cacheMode = vfscommon.CacheModeOff
	case "minimal":
		cacheMode = vfscommon.CacheModeMinimal
	case "writes":
		cacheMode = vfscommon.CacheModeWrites
	case "full":
		cacheMode = vfscommon.CacheModeFull
	default:
		cacheMode = vfscommon.CacheModeOff
	}

	vfsOpt := &vfscommon.Options{
		NoModTime:    rcloneOpt.NoModTime,
		NoChecksum:   rcloneOpt.NoChecksum,
		DirCacheTime: fs.Duration(dirCacheTime),
		PollInterval: 0, // Polling is disabled for webdav
		CacheMode:    cacheMode,
		UID:          rcloneOpt.UID,
		GID:          rcloneOpt.GID,
	}

	if rcloneOpt.VfsReadChunkSizeLimit != "" {
		var chunkSizeLimit fs.SizeSuffix
		if err := chunkSizeLimit.Set(rcloneOpt.VfsReadChunkSizeLimit); err == nil {
			vfsOpt.ChunkSizeLimit = chunkSizeLimit
		}
	}

	if rcloneOpt.VfsReadAhead != "" {
		var readAhead fs.SizeSuffix
		if err := readAhead.Set(rcloneOpt.VfsReadAhead); err == nil {
			vfsOpt.ReadAhead = readAhead
		}
	}

	if rcloneOpt.VfsReadChunkSize != "" {
		var chunkSize fs.SizeSuffix
		if err := chunkSize.Set(rcloneOpt.VfsReadChunkSize); err == nil {
			vfsOpt.ChunkSize = chunkSize
		}
	}

	// Parse and set buffer size globally for rclone
	if rcloneOpt.BufferSize != "" {
		var bufferSize fs.SizeSuffix
		if err := bufferSize.Set(rcloneOpt.BufferSize); err == nil {
			fs.GetConfig(ctx).BufferSize = bufferSize
		}
	}

	// Create mount options using global config
	mountOpt := &mountlib.Options{
		DebugFUSE:     false,
		AllowNonEmpty: true,
		AllowOther:    true,
		Daemon:        false,
		AttrTimeout:   fs.Duration(attrTimeout),
		DeviceName:    fmt.Sprintf("decypharr-%s", configName),
		VolumeName:    fmt.Sprintf("decypharr-%s", configName),
	}

	// Set cache dir
	if rcloneOpt.CacheDir != "" {
		cacheDir := filepath.Join(rcloneOpt.CacheDir, configName)
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			// Log error but continue
			m.logger.Error().Err(err).Msgf("Failed to create cache directory %s, using default cache", cacheDir)
		}
		if err := config.SetCacheDir(cacheDir); err != nil {
			// Log error but continue
			m.logger.Error().Err(err).Msgf("Failed to set cache directory %s, using default cache", cacheDir)
		}
	}

	// Create VFS instance
	vfsInstance := vfs.New(fsrc, vfsOpt)
	m.vfs = vfsInstance

	// Create mount point using rclone's internal mounting
	mountPoint := mountlib.NewMountPoint(mountfn, m.LocalPath, fsrc, mountOpt, vfsOpt)
	m.mountPoint = mountPoint

	// Start the mount
	_, err = mountPoint.Mount()
	if err != nil {
		// Cleanup mount point if it failed
		if mountPoint != nil && mountPoint.UnmountFn != nil {
			if unmountErr := m.Unmount(); unmountErr != nil {
				m.logger.Error().Err(unmountErr).Msgf("Failed to cleanup mount point %s after mount failure", m.LocalPath)
			} else {
				m.logger.Info().Msgf("Successfully cleaned up mount point %s after mount failure", m.LocalPath)
			}
		}
		return fmt.Errorf("failed to mount %s at %s: %w", m.Provider, m.LocalPath, err)
	}

	return nil
}

func (m *Mount) Unmount() error {
	if !m.mounted.Load() {
		m.logger.Info().Msgf("Mount %s is not mounted, skipping unmount", m.Provider)
		return nil
	}

	m.mounted.Store(false)

	if m.vfs != nil {
		m.logger.Debug().Msgf("Shutting down VFS for provider %s", m.Provider)
		m.vfs.Shutdown()
	} else {
		m.logger.Warn().Msgf("VFS instance for provider %s is nil, skipping shutdown", m.Provider)
	}
	if m.mountPoint == nil || m.mountPoint.UnmountFn == nil {
		m.logger.Warn().Msgf("Mount point for provider %s is nil or unmount function is not set, skipping unmount", m.Provider)
		return nil
	}

	if err := m.mountPoint.Unmount(); err != nil {
		// Try to force unmount if normal unmount fails
		if err := m.forceUnmount(); err != nil {
			m.logger.Error().Err(err).Msgf("Failed to force unmount %s at %s", m.Provider, m.LocalPath)
			return fmt.Errorf("failed to unmount %s at %s: %w", m.Provider, m.LocalPath, err)
		}
	}
	return nil
}

func (m *Mount) forceUnmount() error {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd":
		if err := m.tryUnmount("umount", m.LocalPath); err == nil {
			m.logger.Info().Msgf("Successfully unmounted %s", m.LocalPath)
			return nil
		}

		// Try lazy unmount
		if err := m.tryUnmount("umount", "-l", m.LocalPath); err == nil {
			m.logger.Info().Msgf("Successfully lazy unmounted %s", m.LocalPath)
			return nil
		}

		if err := m.tryUnmount("fusermount", "-uz", m.LocalPath); err == nil {
			m.logger.Info().Msgf("Successfully unmounted %s using fusermount3", m.LocalPath)
			return nil
		}

		if err := m.tryUnmount("fusermount3", "-uz", m.LocalPath); err == nil {
			m.logger.Info().Msgf("Successfully unmounted %s using fusermount3", m.LocalPath)
			return nil
		}

		return fmt.Errorf("all unmount attempts failed for %s", m.LocalPath)
	default:
		return fmt.Errorf("force unmount not supported on %s", runtime.GOOS)
	}
}

func (m *Mount) tryUnmount(command string, args ...string) error {
	cmd := exec.Command(command, args...)
	return cmd.Run()
}

func (m *Mount) isMountBusy() bool {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd":
		// Check if the mount point is listed in /proc/mounts or mount output
		cmd := exec.Command("mount")
		output, err := cmd.Output()
		if err != nil {
			return false
		}
		return strings.Contains(string(output), m.LocalPath)
	default:
		return false
	}
}

func (m *Mount) IsMounted() bool {
	return m.mounted.Load() && m.mountPoint != nil
}

func (m *Mount) Refresh(dirs []string) error {

	if !m.mounted.Load() || m.vfs == nil {
		return fmt.Errorf("provider %s not properly mounted", m.Provider)
	}
	// Forget the directories first
	if err := m.ForgetVFS(dirs); err != nil {
		return fmt.Errorf("failed to forget VFS directories for %s: %w", m.Provider, err)
	}
	//Then refresh the directories
	if err := m.RefreshVFS(dirs); err != nil {
		return fmt.Errorf("failed to refresh VFS directories for %s: %w", m.Provider, err)
	}
	return nil
}

func (m *Mount) RefreshVFS(dirs []string) error {

	root, err := m.vfs.Root()
	if err != nil {
		return fmt.Errorf("failed to get VFS root for %s: %w", m.Provider, err)
	}

	getDir := func(path string) (*vfs.Dir, error) {
		path = strings.Trim(path, "/")
		segments := strings.Split(path, "/")
		var node vfs.Node = root
		for _, s := range segments {
			if dir, ok := node.(*vfs.Dir); ok {
				node, err = dir.Stat(s)
				if err != nil {
					return nil, err
				}
			}
		}
		if dir, ok := node.(*vfs.Dir); ok {
			return dir, nil
		}
		return nil, vfs.EINVAL
	}

	// If no specific directories provided, refresh root
	if len(dirs) == 0 {

		if _, err := root.ReadDirAll(); err != nil {
			return err
		}
		return nil
	}

	if len(dirs) == 1 {
		vfsDir, err := getDir(dirs[0])
		if err != nil {
			return fmt.Errorf("failed to find directory '%s' for refresh in %s: %w", dirs[0], m.Provider, err)
		}
		if _, err := vfsDir.ReadDirAll(); err != nil {
			return fmt.Errorf("failed to refresh directory '%s' in %s: %w", dirs[0], m.Provider, err)
		}
		return nil
	}

	var errs []error
	// Refresh specific directories
	for _, dir := range dirs {
		if dir != "" {
			// Clean the directory path
			vfsDir, err := getDir(dir)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to find directory '%s' for refresh in %s: %w", dir, m.Provider, err))
			}
			if _, err := vfsDir.ReadDirAll(); err != nil {
				errs = append(errs, fmt.Errorf("failed to refresh directory '%s' in %s: %w", dir, m.Provider, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func (m *Mount) ForgetVFS(dirs []string) error {
	// Get root directory
	root, err := m.vfs.Root()
	if err != nil {
		return fmt.Errorf("failed to get VFS root for %s: %w", m.Provider, err)
	}

	// Forget specific directories
	for _, dir := range dirs {
		if dir != "" {
			// Clean the directory path
			dir = strings.Trim(dir, "/")
			// Forget the directory from cache
			root.ForgetPath(dir, fs.EntryDirectory)
		}
	}

	return nil
}
