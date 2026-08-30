package share

import (
	"io"
	"io/fs"
	"os"
	"sync"
	"sync/atomic"
)

// file serves one read-only regular file from an io.ReaderAt. Concurrent
// ReadAt calls go straight to the reader so positioned protocol reads
// (kernel readahead, parallel clients) do not serialize on the seek position.
type file struct {
	info   fs.FileInfo
	reader io.ReaderAt
	close  func() error
	pos    int64
	mu     sync.Mutex
	closed atomic.Bool
}

func newFile(info fs.FileInfo, reader io.ReaderAt, close func() error) *file {
	return &file{info: info, reader: reader, close: close}
}

func (f *file) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *file) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, err := f.ReadAt(p, f.pos)
	f.pos += int64(n)
	return n, err
}

func (f *file) ReadAt(p []byte, off int64) (int, error) {
	if f.closed.Load() {
		return 0, os.ErrClosed
	}
	return f.reader.ReadAt(p, off)
}

func (f *file) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = f.pos + offset
	case io.SeekEnd:
		next = f.info.Size() + offset
	default:
		return 0, fs.ErrInvalid
	}
	if next < 0 {
		return 0, fs.ErrInvalid
	}
	f.pos = next
	return next, nil
}

func (f *file) Close() error {
	if f.closed.Swap(true) || f.close == nil {
		return nil
	}
	return f.close()
}

func (f *file) Write([]byte) (int, error) {
	return 0, &fs.PathError{Op: "write", Path: f.info.Name(), Err: fs.ErrPermission}
}

func (f *file) Readdir(int) ([]fs.FileInfo, error) {
	return nil, &fs.PathError{Op: "readdir", Path: f.info.Name(), Err: fs.ErrInvalid}
}

// dirFile serves one open directory. The listing is captured on first use and
// paged out with os.File.Readdir semantics.
type dirFile struct {
	info fs.FileInfo
	list func() ([]fs.FileInfo, error)

	mu      sync.Mutex
	entries []fs.FileInfo
	loaded  bool
	off     int
}

func (d *dirFile) Stat() (fs.FileInfo, error) { return d.info, nil }

func (d *dirFile) Readdir(count int) ([]fs.FileInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.loaded {
		entries, err := d.list()
		if err != nil {
			return nil, err
		}
		d.entries, d.loaded = entries, true
	}
	remaining := d.entries[d.off:]
	if count <= 0 {
		d.off = len(d.entries)
		return remaining, nil
	}
	if len(remaining) == 0 {
		return nil, io.EOF
	}
	if count > len(remaining) {
		count = len(remaining)
	}
	d.off += count
	return remaining[:count], nil
}

func (d *dirFile) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.Name(), Err: fs.ErrInvalid}
}

func (d *dirFile) Write([]byte) (int, error) {
	return 0, &fs.PathError{Op: "write", Path: d.info.Name(), Err: fs.ErrPermission}
}

func (d *dirFile) Seek(offset int64, whence int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if offset == 0 && whence == io.SeekStart {
		d.off = 0
		return 0, nil
	}
	return 0, fs.ErrInvalid
}

func (d *dirFile) Close() error { return nil }
