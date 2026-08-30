//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
)

// File implements a FUSE file with RFS streaming
type File struct {
	fs.Inode
	config    *config.FuseConfig
	logger    *logger.RateLimitedEvent
	info      atomic.Pointer[manager.FileInfo]
	createdAt time.Time
	vfs       *vfs.Manager
}

var (
	_ = (fs.NodeOpener)((*File)(nil))
	_ = (fs.NodeGetattrer)((*File)(nil))
)

// NewFile creates a new file
func NewFile(vfsManager *vfs.Manager, config *config.FuseConfig, info *manager.FileInfo, rl *logger.RateLimitedLogger) *File {
	createdAt := info.ModTime()
	if createdAt.IsZero() {
		// Choose the fallback once when the node is created. Recomputing it in
		// Getattr would change ctime and mtime whenever the attr cache expires.
		createdAt = time.Now()
	}

	file := &File{
		config:    config,
		logger:    rl.Rate(fmt.Sprintf("%s/%s", info.Parent(), info.Name())),
		vfs:       vfsManager,
		createdAt: createdAt,
	}
	file.info.Store(info)
	return file
}

func (f *File) updateInfo(info *manager.FileInfo) {
	f.info.Store(info)
}

func (f *File) modTime(info *manager.FileInfo) time.Time {
	if modTime := info.ModTime(); !modTime.IsZero() {
		return modTime
	}
	return f.createdAt
}

func (f *File) infoForHandle(fh fs.FileHandle) *manager.FileInfo {
	if handle, ok := fh.(*Handle); ok && handle.info != nil {
		return handle.info
	}
	return f.info.Load()
}

// Getattr returns file attributes
func (f *File) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info := f.infoForHandle(fh)
	if info == nil {
		return syscall.EIO
	}

	modTime := uint64(f.modTime(info).Unix())
	out.Mode = 0644 | fuse.S_IFREG
	out.Size = uint64(info.Size())
	out.Nlink = 1 // Files always have 1 link (themselves)
	out.Blksize = 4096
	out.Blocks = (uint64(info.Size()) + 511) / 512 // Number of 512-byte blocks
	out.Uid = f.config.UID
	out.Gid = f.config.GID
	out.Atime = modTime
	out.Mtime = modTime
	out.Ctime = modTime
	out.AttrValid = uint64(AttrTimeout.Seconds())
	return 0
}

// Open creates file handle with VFS or DFS based on configuration
// Reader is created eagerly here instead of lazily in Read() to surface errors early
func (f *File) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	info := f.info.Load()
	if info == nil {
		return nil, 0, syscall.EIO
	}
	content := info.Content()

	var reader *vfs.StreamingFile
	if info.IsRemote() && len(content) == 0 {
		var err error
		reader, err = f.vfs.GetFile(info)
		if err != nil {
			f.logger.Error().Err(err).Str("file", info.Name()).Msg("Failed to get reader at open")
			return nil, 0, syscall.EIO
		}
	}

	fh := &Handle{
		file:       f,
		info:       info,
		content:    content,
		streamFile: reader,
		logger:     f.logger,
	}
	return fh, 0, 0
}

func skippableError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) {
		return true
	}
	return false
}
