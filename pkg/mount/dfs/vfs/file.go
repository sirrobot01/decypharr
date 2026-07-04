package vfs

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
)

// StreamingFile is the FUSE file interface for VFS
type StreamingFile struct {
	item     *CacheItem
	fileSize int64
	closed   atomic.Bool
}

// NewStreamingFile creates a new streaming file handle. It returns nil when
// the item has been claimed for teardown by the cache janitor — the caller
// must fetch a fresh item and try again (see Manager.GetFile).
func NewStreamingFile(item *CacheItem) *StreamingFile {
	if !item.Open() { // take an open reference; fails on a claimed item
		return nil
	}

	return &StreamingFile{
		item:     item,
		fileSize: item.info.Size,
	}
}

// ReadAt implements io.ReaderAt using a background context.
// Prefer ReadAtContext when a caller context is available (e.g. from a FUSE handle).
func (f *StreamingFile) ReadAt(p []byte, off int64) (int, error) {
	return f.ReadAtContext(context.Background(), p, off)
}

// ReadAtContext reads from the file, passing ctx into the download layer so
// the operation can be interrupted by a read timeout or client disconnect.
func (f *StreamingFile) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if f.closed.Load() {
		return 0, errors.New("file closed")
	}

	if off >= f.fileSize {
		return 0, io.EOF
	}

	// Clamp read size
	readSize := int64(len(p))
	if off+readSize > f.fileSize {
		readSize = f.fileSize - off
		p = p[:readSize]
	}

	n, err := f.item.ReadAtContext(ctx, p, off)

	// Handle partial read at EOF
	if n < int(readSize) && err == nil {
		err = io.EOF
	}

	return n, err
}

// Size returns the file size
func (f *StreamingFile) Size() int64 {
	return f.fileSize
}

// Close closes the file handle
func (f *StreamingFile) Close() error {
	if f.closed.Swap(true) {
		return nil
	}
	f.item.Release() // Decrement opens count

	return nil
}
