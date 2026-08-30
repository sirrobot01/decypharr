package share

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/manager"
)

// fakeSession is one sequential body over data, the shape a manager session
// presents: reads move forward, a Seek repositions, and Close ends it.
type fakeSession struct {
	data    []byte
	pos     int64
	closed  bool
	failAt  int64 // offset at which Read fails once; negative disables
	readErr error
}

func (s *fakeSession) Read(p []byte) (int, error) {
	if s.closed {
		return 0, os.ErrClosed
	}
	if s.failAt >= 0 && s.pos >= s.failAt {
		s.failAt = -1
		return 0, s.readErr
	}
	if s.pos >= int64(len(s.data)) {
		return 0, io.EOF
	}
	n := copy(p, s.data[s.pos:])
	s.pos += int64(n)
	return n, nil
}

func (s *fakeSession) Seek(off int64, whence int) (int64, error) {
	if whence != io.SeekStart {
		return 0, errors.New("unsupported whence")
	}
	s.pos = off
	return off, nil
}

func (s *fakeSession) Close() error { s.closed = true; return nil }
func (s *fakeSession) Size() int64  { return int64(len(s.data)) }
func (s *fakeSession) Prime() error { return nil }

// newTestReader returns a reader over data plus a counter of sessions opened.
func newTestReader(data []byte, mutate func(*fakeSession)) (*streamReader, *atomic.Int64) {
	var opens atomic.Int64
	r := &streamReader{
		ctx:  context.Background(),
		size: int64(len(data)),
		dial: func(_ context.Context, off int64) (manager.StreamReader, error) {
			opens.Add(1)
			s := &fakeSession{data: data, pos: off, failAt: -1}
			if mutate != nil {
				mutate(s)
			}
			return s, nil
		},
	}
	return r, &opens
}

func TestStreamReaderSequentialReuseOneSession(t *testing.T) {
	data := bytes.Repeat([]byte("decypharr"), 500)
	r, opens := newTestReader(data, nil)
	defer r.close()

	got := make([]byte, 0, len(data))
	buf := make([]byte, 256)
	for off := int64(0); off < int64(len(data)); {
		n, err := r.ReadAt(buf, off)
		got = append(got, buf[:n]...)
		off += int64(n)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("read %d bytes, want %d", len(got), len(data))
	}
	// The whole point of dropping the lane pool: sequential reads must not
	// cost more than one debrid session.
	if n := opens.Load(); n != 1 {
		t.Fatalf("opened %d sessions for a sequential read, want 1", n)
	}
}

func TestStreamReaderEOFBoundaries(t *testing.T) {
	data := []byte("0123456789")
	r, _ := newTestReader(data, nil)
	defer r.close()

	// A read that ends exactly at EOF reports it along with the bytes.
	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 6)
	if n != 4 || err != io.EOF {
		t.Fatalf("tail read: n=%d err=%v, want 4, io.EOF", n, err)
	}
	if !bytes.Equal(buf, data[6:]) {
		t.Fatalf("tail read returned %q", buf)
	}

	// A read starting at or past EOF returns nothing.
	if n, err := r.ReadAt(buf, int64(len(data))); n != 0 || err != io.EOF {
		t.Fatalf("read at EOF: n=%d err=%v, want 0, io.EOF", n, err)
	}

	// An oversized request is clamped to what the file holds.
	big := make([]byte, 64)
	if n, err := r.ReadAt(big, 8); n != 2 || err != io.EOF {
		t.Fatalf("oversized read: n=%d err=%v, want 2, io.EOF", n, err)
	}
}

func TestStreamReaderBackwardSeek(t *testing.T) {
	data := []byte("0123456789")
	r, opens := newTestReader(data, nil)
	defer r.close()

	buf := make([]byte, 4)
	if _, err := r.ReadAt(buf, 6); err != io.EOF {
		t.Fatalf("forward read: %v", err)
	}
	// Seeking back reuses the session; the manager session handles the
	// reconnect internally, so this must not open a second one.
	if n, err := r.ReadAt(buf, 0); n != 4 || err != nil {
		t.Fatalf("backward read: n=%d err=%v", n, err)
	}
	if !bytes.Equal(buf, data[:4]) {
		t.Fatalf("backward read returned %q", buf)
	}
	if n := opens.Load(); n != 1 {
		t.Fatalf("opened %d sessions, want 1", n)
	}
}

func TestStreamReaderReconnectsAfterFailure(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 100)
	boom := errors.New("link died")
	first := true
	r, opens := newTestReader(data, func(s *fakeSession) {
		if first {
			first = false
			s.failAt, s.readErr = 0, boom
		}
	})
	defer r.close()

	buf := make([]byte, 10)
	if _, err := r.ReadAt(buf, 0); !errors.Is(err, boom) {
		t.Fatalf("first read: err = %v, want %v", err, boom)
	}
	// The spent session is dropped, so the retry opens a fresh one.
	if n, err := r.ReadAt(buf, 0); n != 10 || err != nil {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
	if n := opens.Load(); n != 2 {
		t.Fatalf("opened %d sessions, want 2", n)
	}
}

func TestStreamReaderCloseReleasesSession(t *testing.T) {
	data := []byte("0123456789")
	var opened *fakeSession
	r, _ := newTestReader(data, func(s *fakeSession) { opened = s })

	buf := make([]byte, 4)
	if _, err := r.ReadAt(buf, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.close(); err != nil {
		t.Fatal(err)
	}
	if opened == nil || !opened.closed {
		t.Fatal("close did not release the session")
	}
	if _, err := r.ReadAt(buf, 0); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("read after close: err = %v, want os.ErrClosed", err)
	}
}
