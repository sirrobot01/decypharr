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

	if err := setRcloneConfig(m.Provider, m.WebDAVURL); err != nil {
		return fmt.Errorf("failed to set rclone config: %w", err)
	}

	// Get the mount function - try different mount methods
	mountFn, err := getMountFn()
	if err != nil {
		return fmt.Errorf("failed to get mount function for %s: %w", m.Provider, err)
	}

	go func() {
		if err := m.performMount(mountCtx, mountFn); err != nil {
			m.logger.Error().Err(err).Msgf("Failed to mount %s at %s", m.Provider, m.LocalPath)
			return
		}
		m.mounted.Store(true)
		m.logger.Info().Msgf("Successfully mounted %s WebDAV at %s", m.Provider, m.LocalPath)
		<-mountCtx.Done() // Wait for context cancellation
	}()
	m.logger.Info().Msgf("Mount process started for %s at %s", m.Provider, m.LocalPath)
	return nil
}

func setRcloneConfig(configName, webdavURL string) error {
	// Set configuration in rclone's config system using FileSetValue
	config.FileSetValue(configName, "type", "webdav")
	config.FileSetValue(configName, "url", webdavURL)
	config.FileSetValue(configName, "vendor", "other")
	config.FileSetValue(configName, "pacer_min_sleep", "0")
	return nil
}

func (m *Mount) performMount(ctx context.Context, mountfn mountlib.MountFn) error {
	// Create filesystem from config
	fsrc, err := fs.NewFs(ctx, fmt.Sprintf("%s:", m.Provider))
	if err != nil {
		return fmt.Errorf("failed to create filesystem: %w", err)
	}

	// Get global rclone config
	rcloneOpt := configPkg.Get().Rclone

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

	vfsOpt := &vfscommon.Options{}

	vfsOpt.Init() // Initialize VFS options with default values

	vfsOpt.CacheMode = cacheMode

	// Set VFS options based on rclone configuration
	if rcloneOpt.NoChecksum {
		vfsOpt.NoChecksum = rcloneOpt.NoChecksum
	}
	if rcloneOpt.NoModTime {
		vfsOpt.NoModTime = rcloneOpt.NoModTime
	}
	if rcloneOpt.UID != 0 {
		vfsOpt.UID = rcloneOpt.UID
	}
	if rcloneOpt.GID != 0 {
		vfsOpt.GID = rcloneOpt.GID
	}
	if rcloneOpt.Umask != "" {
		var umask vfscommon.FileMode
		if err := umask.Set(rcloneOpt.Umask); err == nil {
			vfsOpt.Umask = umask
		}
	}

	// Parse duration strings
	if rcloneOpt.DirCacheTime != "" {
		if dirCacheTime, err := time.ParseDuration(rcloneOpt.DirCacheTime); err == nil {
			vfsOpt.DirCacheTime = fs.Duration(dirCacheTime)
		}
	}

	if rcloneOpt.VfsCachePollInterval != "" {
		if vfsCachePollInterval, err := time.ParseDuration(rcloneOpt.VfsCachePollInterval); err == nil {
			vfsOpt.CachePollInterval = fs.Duration(vfsCachePollInterval)
		}
	}

	if rcloneOpt.VfsCacheMaxAge != "" {
		if vfsCacheMaxAge, err := time.ParseDuration(rcloneOpt.VfsCacheMaxAge); err == nil {
			vfsOpt.CacheMaxAge = fs.Duration(vfsCacheMaxAge)
		}
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

	fs.GetConfig(ctx).UseMmap = true

	if rcloneOpt.VfsCacheMaxSize != "" {
		var cacheMaxSize fs.SizeSuffix
		if err := cacheMaxSize.Set(rcloneOpt.VfsCacheMaxSize); err == nil {
			vfsOpt.CacheMaxSize = cacheMaxSize
		}
	}

	// Create mount options using global config
	mountOpt := &mountlib.Options{
		DebugFUSE:     false,
		AllowNonEmpty: true,
		AllowOther:    true,
		Daemon:        false,
		AsyncRead:     true,
		DeviceName:    fmt.Sprintf("decypharr-%s", m.Provider),
		VolumeName:    fmt.Sprintf("decypharr-%s", m.Provider),
	}

	if rcloneOpt.AttrTimeout != "" {
		if attrTimeout, err := time.ParseDuration(rcloneOpt.AttrTimeout); err == nil {
			mountOpt.AttrTimeout = fs.Duration(attrTimeout)
		}
	}

	// Set cache dir
	if rcloneOpt.CacheDir != "" {
		cacheDir := filepath.Join(rcloneOpt.CacheDir, m.Provider)
		if err := os.MkdirAll(cacheDir, 0755); err != nil {
			// Log error but continue
			m.logger.Error().Err(err).Msgf("Failed to create cache directory %s, using default cache", cacheDir)
		}
		if err := config.SetCacheDir(cacheDir); err != nil {
			// Log error but continue
			m.logger.Error().Err(err).Msgf("Failed to set cache directory %s, using default cache", cacheDir)
		}
	}
	// Create mount point using rclone's internal mounting
	m.mountPoint = mountlib.NewMountPoint(mountfn, m.LocalPath, fsrc, mountOpt, vfsOpt)

	// Start the mount
	_, err = m.mountPoint.Mount()
	if err != nil {
		// Cleanup mount point if it failed
		if m.mountPoint != nil && m.mountPoint.UnmountFn != nil {
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

	m.logger.Debug().Msgf("Shutting down VFS for provider %s", m.Provider)
	m.mountPoint.VFS.Shutdown()
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
	return m.mounted.Load() && m.mountPoint != nil && m.mountPoint.VFS != nil
}

func (m *Mount) RefreshDir(dirs []string) error {
	if !m.IsMounted() {
		return fmt.Errorf("provider %s not properly mounted. Skipping refreshes", m.Provider)
	}

	// Use atomic forget-and-refresh to avoid race conditions
	return m.forceRefreshVFS(dirs)
}

// forceRefreshVFS atomically forgets and refreshes VFS directories to ensure immediate visibility
func (m *Mount) forceRefreshVFS(dirs []string) error {
	vfsInstance := m.mountPoint.VFS
	root, err := vfsInstance.Root()
	if err != nil {
		return fmt.Errorf("failed to get VFS root for %s: %w", m.Provider, err)
	}

	getDir := func(path string) (*vfs.Dir, error) {
		path = strings.Trim(path, "/")
		if path == "" {
			return root, nil
		}
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

	// If no specific directories provided, work with root
	if len(dirs) == 0 {
		// Atomically forget and refresh root
		root.ForgetAll()
		if _, err := root.ReadDirAll(); err != nil {
			return fmt.Errorf("failed to force-refresh root for %s: %w", m.Provider, err)
		}
		return nil
	}

	var errs []error
	// Process each directory atomically
	for _, dir := range dirs {
		if dir != "" {
			dir = strings.Trim(dir, "/")
			// Get the directory handle
			vfsDir, err := getDir(dir)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed to find directory '%s' for force-refresh in %s: %w", dir, m.Provider, err))
				continue
			}

			// Atomically forget and refresh this specific directory
			vfsDir.ForgetAll()
			if _, err := vfsDir.ReadDirAll(); err != nil {
				errs = append(errs, fmt.Errorf("failed to force-refresh directory '%s' in %s: %w", dir, m.Provider, err))
			}
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
