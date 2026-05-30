//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
)

var (
	_ = (fs.FileReader)((*Handle)(nil))
	_ = (fs.FileReleaser)((*Handle)(nil))
	_ = (fs.FileFlusher)((*Handle)(nil))
	_ = (fs.FileFsyncer)((*Handle)(nil))
)

// Handle implements file operations using the new DFS implementation
type Handle struct {
	file       *File
	streamFile *vfs.StreamingFile
	closed     atomic.Bool
	logger     *logger.RateLimitedEvent
	lastAccess atomic.Int64
}

// Read implements DFS streaming
func (fh *Handle) Read(ctx context.Context, dest []byte, off int64) (fuse.ReadResult, syscall.Errno) {
	fh.lastAccess.Store(time.Now().Unix())

	if fh.closed.Load() {
		return nil, syscall.EBADF
	}

	// Static content (e.g. version.txt): serve from the in-memory buffer.
	if len(fh.file.content) > 0 {
		data := fh.readFromStaticContent(off, int64(len(dest)))
		return fuse.ReadResultData(data), 0
	}

	// Sidecar file (e.g. subtitle): serve from disk.
	if fh.file.info.IsSidecar() {
		f, err := os.Open(fh.file.info.SidecarPath())
		if err != nil {
			return nil, syscall.ENOENT
		}
		defer f.Close()
		n, _ := f.ReadAt(dest, off)
		return fuse.ReadResultData(dest[:n]), 0
	}

	if fh.streamFile == nil {
		return nil, syscall.EIO
	}

	if off >= fh.streamFile.Size() {
		return fuse.ReadResultData([]byte{}), 0
	}

	// Apply per-read timeout so a stalled download can't block this goroutine
	// (and therefore go-fuse's goroutine pool) indefinitely.
	readCtx, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()

	n, err := fh.streamFile.ReadAtContext(readCtx, dest, off)
	if err != nil && !skippableError(err) {
		switch {
		case errors.Is(err, syscall.EBADF):
			return nil, syscall.EBADF
		case errors.Is(err, io.EOF):
			return fuse.ReadResultData(dest[:n]), 0
		case errors.Is(err, context.DeadlineExceeded):
			return nil, syscall.ETIMEDOUT
		case errors.Is(err, context.Canceled):
			return nil, syscall.EINTR
		default:
			return nil, syscall.EIO
		}
	}
	return fuse.ReadResultData(dest[:n]), 0
}

// readFromStaticContent handles static content
func (fh *Handle) readFromStaticContent(offset, size int64) []byte {
	content := fh.file.content
	end := offset + size
	if end > int64(len(content)) {
		end = int64(len(content))
	}
	if offset >= int64(len(content)) {
		return []byte{}
	}
	return content[offset:end]
}

// Release closes the file handle
func (fh *Handle) Release(ctx context.Context) syscall.Errno {
	if !fh.closed.CompareAndSwap(false, true) {
		return 0
	}

	if fh.streamFile != nil {
		fh.streamFile.Close()
		if fh.file != nil && fh.file.vfs != nil {
			fh.file.vfs.ReleaseFile(fh.file.info)
		}
	}

	return 0
}

func (fh *Handle) Flush(ctx context.Context) syscall.Errno {
	return 0
}

func (fh *Handle) Fsync(ctx context.Context, flags uint32) syscall.Errno {
	return 0
}
