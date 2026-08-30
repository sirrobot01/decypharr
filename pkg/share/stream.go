package share

import (
	"context"
	"io"
	"io/fs"
	"os"
	"sync"

	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/facetfs"
)

// streamClient labels sessions in the active-streams view. One cache serves
// both exports, so a session cannot be attributed to NFS or SMB.
const streamClient = "Share"

// streamer opens read-only handles onto remote catalog entries. Each handle
// owns exactly one manager session and nothing is pooled: the cache in front
// drives at most four sequential fetches per file and thirty-two overall, so
// the session count is already bounded where the reads are.
type streamer struct {
	ctx context.Context
	mgr *manager.Manager
}

func (s *streamer) open(info *manager.FileInfo, stat fs.FileInfo) (facetfs.File, error) {
	r := &streamReader{
		ctx:  s.ctx,
		size: stat.Size(),
		dial: func(ctx context.Context, off int64) (manager.StreamReader, error) {
			return s.mgr.OpenStreamForFile(ctx, info, off, streamClient)
		},
	}
	return newFile(stat, r, r.close), nil
}

// streamReader presents one resilient manager session as an io.ReaderAt. The
// session is opened on first read, positioned where that read starts, and
// dropped on failure so the next read reconnects. Callers are overwhelmingly
// sequential, so the session rarely seeks.
type streamReader struct {
	ctx  context.Context
	dial func(ctx context.Context, off int64) (manager.StreamReader, error)
	size int64

	mu      sync.Mutex
	session manager.StreamReader
	pos     int64
	closed  bool
}

func (r *streamReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if off >= r.size {
		return 0, io.EOF
	}
	want := int64(len(p))
	if off+want > r.size {
		want = r.size - off
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if err := r.positionLocked(off); err != nil {
		return 0, err
	}

	n, err := io.ReadFull(r.session, p[:want])
	r.pos += int64(n)
	if err == io.ErrUnexpectedEOF {
		err = nil // short read inside the file; the caller keeps n bytes
	}
	if err != nil {
		// A spent session is never reused; the next read opens a fresh one.
		r.dropLocked()
		if err == io.EOF && n > 0 {
			err = nil
		}
		return n, err
	}
	if off+want == r.size {
		return n, io.EOF
	}
	return n, nil
}

// positionLocked puts the session at off, opening it there on first use.
// Opening at the offset costs one request; seeking an open session backward
// makes it reconnect internally, which is why the cache reads forward.
func (r *streamReader) positionLocked(off int64) error {
	if r.session == nil {
		session, err := r.dial(r.ctx, off)
		if err != nil {
			return err
		}
		r.session, r.pos = session, off
		return nil
	}
	if r.pos == off {
		return nil
	}
	if _, err := r.session.Seek(off, io.SeekStart); err != nil {
		r.dropLocked()
		return err
	}
	r.pos = off
	return nil
}

func (r *streamReader) dropLocked() {
	if r.session != nil {
		_ = r.session.Close()
		r.session = nil
	}
}

func (r *streamReader) close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	r.dropLocked()
	return nil
}
