//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	info      *manager.FileInfo
	createdAt time.Time
	content   []byte // For files like version.txt
	vfs       *vfs.Manager
}

var (
	_ = (fs.NodeOpener)((*File)(nil))
	_ = (fs.NodeGetattrer)((*File)(nil))
)

// NewFile creates a new file
func NewFile(vfsManager *vfs.Manager, config *config.FuseConfig, info *manager.FileInfo, rl *logger.RateLimitedLogger) *File {
	return &File{
		config:  config,
		logger:  rl.Rate(fmt.Sprintf("%s/%s", info.Parent(), info.Name())),
		info:    info,
		vfs:     vfsManager,
		content: info.Content(),
	}
}

// Getattr returns file attributes
func (f *File) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	var modTime uint64
	if f.createdAt.IsZero() {
		modTime = uint64(time.Now().Unix())
	} else {
		modTime = uint64(f.createdAt.Unix())
	}
	out.Mode = 0644 | fuse.S_IFREG
	out.Size = uint64(f.info.Size())
	out.Nlink = 1 // Files always have 1 link (themselves)
	out.Blksize = 4096
	out.Blocks = (uint64(f.info.Size()) + 511) / 512 // Number of 512-byte blocks
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
	// Sidecar files (subtitles): open the on-disk fd once and keep it for all Reads.
	if f.info.IsSidecar() {
		fd, err := os.Open(f.info.SidecarPath())
		if err != nil {
			return nil, 0, syscall.ENOENT
		}
		fh := &Handle{file: f, sidecarFd: fd, logger: f.logger}
		fh.lastAccess.Store(time.Now().Unix())
		return fh, fuse.FOPEN_DIRECT_IO, 0
	}

	var reader *vfs.StreamingFile
	if f.info.IsRemote() && len(f.content) == 0 {
		var err error
		reader, err = f.vfs.GetFile(f.info)
		if err != nil {
			f.logger.Error().Err(err).Str("file", f.info.Name()).Msg("Failed to get reader at open")
			return nil, 0, syscall.EIO
		}
	}

	fh := &Handle{
		file:       f,
		streamFile: reader,
		logger:     f.logger,
	}
	fh.lastAccess.Store(time.Now().Unix())
	return fh, 0, 0
}

func skippableError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) {
		return true
	}
	return false
}
