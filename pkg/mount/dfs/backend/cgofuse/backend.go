package cgofuse

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync/atomic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/backend"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
	"github.com/winfsp/cgofuse/fuse"
)

func init() {
	backend.Register(backend.Cgo, func(vfs *vfs.Manager, config *config.FuseConfig) (backend.Backend, error) {
		return NewBackend(vfs, config)
	})
}

// Backend implements the cgofuse backend for cross-platform FUSE support
type Backend struct {
	config *config.FuseConfig
	logger zerolog.Logger
	host   *fuse.FileSystemHost
	ready  atomic.Bool
	root   *FS
	vfs    *vfs.Manager
}

// NewBackend creates a new cgofuse backend
func NewBackend(vfs *vfs.Manager, config *config.FuseConfig) (backend.Backend, error) {
	log := logger.New("cgofuse")
	return &Backend{
		config: config,
		logger: log,
		root:   NewFS(vfs, config, log),
		vfs:    vfs,
	}, nil
}

// Mount mounts the filesystem using cgofuse
func (b *Backend) Mount(ctx context.Context) error {
	// Create mount point if it doesn't exist (skip on Windows)
	if runtime.GOOS != "windows" {
		_ = os.MkdirAll(b.config.MountPath, 0755)
	}
	if b.root == nil {
		return fmt.Errorf("root node is not initialized")
	}

	if b.vfs == nil {
		return fmt.Errorf("VFS manager is not initialized")
	}

	// Create filesystem host
	b.host = fuse.NewFileSystemHost(b.root)
	b.host.SetCapReaddirPlus(true)     // Enable ReaddirPlus (used by WinFsp on Windows)
	b.host.SetCapCaseInsensitive(true) // Windows is case-insensitive

	// Build mount options
	var options []string

	// Common options
	options = append(options, "-o", "fsname=dfs")

	// Platform-specific options
	switch runtime.GOOS {
	case "windows":
		// WinFsp options
		options = append(options, "-o", "uid=-1,gid=-1")
		options = append(options, "-o", "volname=DFS")
		options = append(options, "--FileSystemName=DFS")
		// Allow other users to access
		options = append(options, "-o", "uid=-1,gid=-1")
	case "darwin":
		// macFUSE options
		options = append(options, "-o", "volname=dfs")
		options = append(options, "-o", "noapplexattr")
		options = append(options, "-o", "noappledouble")
		options = append(options, "-o", "allow_other")
	default:
		// Linux FUSE options
		options = append(options, "-o", "allow_other")
		options = append(options, "-o", "default_permissions")
	}

	// Start mount in background
	errChan := make(chan error, 1)

	go func() {
		// Mount returns when unmounted
		ok := b.host.Mount(b.config.MountPath, options)
		if !ok {
			errChan <- fmt.Errorf("mount failed")
		} else {
			errChan <- nil
		}
	}()

	// Wait briefly for mount to initialize
	// cgofuse doesn't have a ready signal, so we check if mount point is accessible
	select {
	case err := <-errChan:
		if err != nil {
			return err
		}
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Mount is running in background
	}

	b.ready.Store(true)
	return nil
}

// Unmount unmounts the filesystem
func (b *Backend) Unmount(ctx context.Context) error {
	b.logger.Info().Msg("Unmounting cgofuse backend")

	if b.host != nil {
		_ = b.host.Unmount()
	}

	// Close VFS manager
	if b.vfs != nil {
		if err := b.vfs.Close(); err != nil {
			b.logger.Warn().Err(err).Msg("Failed to close VFS")
		}
	}

	b.ready.Store(false)
	return nil
}

// WaitReady waits for the mount to be ready
func (b *Backend) WaitReady(ctx context.Context) error {
	// cgofuse doesn't have a direct ready signal
	// We rely on the ready flag being set after successful mount
	if b.ready.Load() {
		return nil
	}
	return fmt.Errorf("mount not ready")
}

// IsReady returns true if the mount is ready
func (b *Backend) IsReady() bool {
	return b.ready.Load()
}

// Type returns the backend type
func (b *Backend) Type() backend.Type {
	return backend.Cgo
}

func (b *Backend) Refresh(name string) {
}
