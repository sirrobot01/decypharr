package backend

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"runtime"

	"github.com/dylanmazurek/decypharr/pkg/mount/dfs/config"
	"github.com/dylanmazurek/decypharr/pkg/mount/dfs/vfs"
)

// Type represents the type of FUSE backend
type Type string

const (
	Hanwen Type = "hanwen"
	Cgo    Type = "cgo"
)

// Backend represents a FUSE backend implementation
type Backend interface {
	// Mount mounts the filesystem at the configured path
	Mount(ctx context.Context) error

	// Unmount unmounts the filesystem
	Unmount(ctx context.Context) error

	// WaitReady waits for the mount to be ready
	WaitReady(ctx context.Context) error

	// IsReady returns true if the mount is ready
	IsReady() bool

	Refresh(dir string)

	// Type returns the backend type
	Type() Type
}

type Func func(vfs *vfs.Manager, config *config.FuseConfig) (Backend, error)

var registry = make(map[Type]Func)

// Register registers a backend constructor function for a given type
func Register(backendType Type, constructor Func) {
	registry[backendType] = constructor
}

func Registry() map[Type]Func {
	return registry
}

// GetDefaultBackendType returns the recommended backend for the current platform
// Linux: hanwen (fastest, pure Go)
// macOS/Windows: cgofuse (cross-platform, works with Fuse-T/WinFsp)
func GetDefaultBackendType() Type {
	if runtime.GOOS == "linux" {
		// Get from environment variable override
		backendEnv := cmp.Or(os.Getenv("DFS_FUSE_BACKEND"), "hanwen")
		switch backendEnv {
		case "hanwen":
			return Hanwen
		case "cgo":
			return Cgo
		default:
			return Hanwen
		}
	}
	return Cgo
}

func New(backendType Type, vfs *vfs.Manager, config *config.FuseConfig) (Backend, error) {
	constructor, ok := registry[backendType]
	if !ok {
		if len(registry) == 0 {
			return nil, fmt.Errorf("no backends registered")
		}
		// Fallback to any available backend
		for _, c := range registry {
			return c(vfs, config)
		}
	}
	return constructor(vfs, config)
}
