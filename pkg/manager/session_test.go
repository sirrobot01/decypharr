package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/manager/link"
)

func testPattern(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i*7 + 13)
	}
	return data
}

// fakeCDN serves a byte pattern with Range support and scriptable failures.
type fakeCDN struct {
	data     []byte
	requests atomic.Int64
	// mode is swapped by tests: "ok", "forbidden", "error"
	mode atomic.Value
	// cutAfter > 0 aborts the response after writing that many bytes, once.
	cutAfter atomic.Int64
	// throttleOnce serves a single 429 with Retry-After: 1.
	throttleOnce atomic.Bool
	// stallOnce writes 10 bytes then blocks until the client disconnects.
	stallOnce atomic.Bool
	// validToken, when set, 403s any request whose ?tok= differs.
	validToken atomic.Value
}

func newFakeCDN(data []byte) *fakeCDN {
	c := &fakeCDN{data: data}
	c.mode.Store("ok")
	c.validToken.Store("")
	return c
}

func (c *fakeCDN) url(server *httptest.Server, token string) string {
	if token == "" {
		return server.URL + "/file"
	}
	return server.URL + "/file?tok=" + token
}

func (c *fakeCDN) handler(w http.ResponseWriter, r *http.Request) {
	c.requests.Add(1)

	if mode := c.mode.Load().(string); mode == "forbidden" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	} else if mode == "error" {
		http.Error(w, "boom", http.StatusInternalServerError)
		return
	}
	if want := c.validToken.Load().(string); want != "" && r.URL.Query().Get("tok") != want {
		http.Error(w, "expired token", http.StatusForbidden)
		return
	}
	if c.throttleOnce.CompareAndSwap(true, false) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "slow down", http.StatusTooManyRequests)
		return
	}

	start := int64(0)
	if rng := r.Header.Get("Range"); rng != "" {
		value := strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-")
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			start = parsed
		}
	}
	if start >= int64(len(c.data)) {
		http.Error(w, "range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	body := c.data[start:]

	if c.stallOnce.CompareAndSwap(true, false) {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(c.data)-1, len(c.data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[:10])
		w.(http.Flusher).Flush()
		<-r.Context().Done() // stall until the session's watchdog kills us
		return
	}

	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if start > 0 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(c.data)-1, len(c.data)))
		w.WriteHeader(http.StatusPartialContent)
	}
	if cut := c.cutAfter.Swap(0); cut > 0 && cut < int64(len(body)) {
		_, _ = w.Write(body[:cut])
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // mid-body connection cut
	}
	_, _ = w.Write(body)
}

// testTransport builds an httpTransport against the fake CDN with counting
// getLink/refresh hooks. linkURL is read atomically so refresh can rotate it.
func testTransport(client *http.Client, linkURL *atomic.Value, refreshes *atomic.Int64, onRefresh func()) *httpTransport {
	return &httpTransport{
		client: client,
		getLink: func(context.Context) (types.DownloadLink, error) {
			return types.DownloadLink{Filename: "file", DownloadLink: linkURL.Load().(string)}, nil
		},
		refresh: func(_ context.Context, bad types.DownloadLink) (types.DownloadLink, error) {
			if refreshes != nil {
				refreshes.Add(1)
			}
			if onRefresh != nil {
				onRefresh()
			}
			return types.DownloadLink{Filename: "file", DownloadLink: linkURL.Load().(string)}, nil
		},
	}
}

func startCDN(t *testing.T, cdn *fakeCDN) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(cdn.handler))
	t.Cleanup(server.Close)
	return server
}

func newTestSession(t *testing.T, tr transport, size int64) *session {
	t.Helper()
	s := newSession(context.Background(), tr, size, 0)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionReadsFullFile(t *testing.T) {
	data := testPattern(1 << 20)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	s := newTestSession(t, testTransport(server.Client(), &url, nil, nil), int64(len(data)))

	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("byte mismatch: got %d bytes", len(got))
	}
}

func TestSessionResumesAfterMidBodyCutAndExpiredToken(t *testing.T) {
	data := testPattern(64 << 10)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)
	cdn.validToken.Store("tok1")
	cdn.cutAfter.Store(1000) // first response dies after 1000 bytes

	var url atomic.Value
	url.Store(cdn.url(server, "tok1"))
	var refreshes atomic.Int64
	tr := testTransport(server.Client(), &url, &refreshes, func() {
		// A refresh hands out a link with the now-valid tok2.
		url.Store(cdn.url(server, "tok2"))
	})

	s := newTestSession(t, tr, int64(len(data)))
	first, err := io.ReadAll(io.LimitReader(s, 1000))
	if err != nil || len(first) != 1000 {
		t.Fatalf("first read: n=%d err=%v", len(first), err)
	}
	// Expire tok1 before the session reconnects: the retry with the old link
	// must 403, forcing a refresh.
	cdn.validToken.Store("tok2")

	rest, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := append(first, rest...); !bytes.Equal(got, data) {
		t.Fatalf("byte mismatch after resume: got %d bytes", len(got))
	}
	if refreshes.Load() == 0 {
		t.Fatal("expected at least one link refresh")
	}
}

func TestSessionHonorsRetryAfter(t *testing.T) {
	data := testPattern(4 << 10)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)
	cdn.throttleOnce.Store(true)

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	var refreshes atomic.Int64
	s := newTestSession(t, testTransport(server.Client(), &url, &refreshes, nil), int64(len(data)))

	begin := time.Now()
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("byte mismatch")
	}
	if elapsed := time.Since(begin); elapsed < 900*time.Millisecond {
		t.Fatalf("Retry-After not honored: finished in %s", elapsed)
	}
	if refreshes.Load() != 0 {
		t.Fatal("429 must not refresh the link")
	}
}

func TestSessionSeek(t *testing.T) {
	data := testPattern(1 << 20)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	s := newTestSession(t, testTransport(server.Client(), &url, nil, nil), int64(len(data)))

	buf := make([]byte, 10)
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	after := cdn.requests.Load()

	// Small forward seek: served by discarding, no new request.
	if _, err := s.Seek(100, io.SeekCurrent); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, data[110:120]) {
		t.Fatal("wrong bytes after discard seek")
	}
	if cdn.requests.Load() != after {
		t.Fatal("discard seek should not reconnect")
	}

	// Large forward seek: reconnects at the offset.
	if _, err := s.Seek(int64(sessionSeekDiscardMax)+120+1024, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	wantOff := sessionSeekDiscardMax + 120 + 1024
	if !bytes.Equal(buf, data[wantOff:wantOff+10]) {
		t.Fatal("wrong bytes after reopen seek")
	}
	if cdn.requests.Load() != after+1 {
		t.Fatalf("expected one reconnect, saw %d", cdn.requests.Load()-after)
	}

	// Backward seek: reconnects.
	if _, err := s.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, data[:10]) {
		t.Fatal("wrong bytes after backward seek")
	}
	if cdn.requests.Load() != after+2 {
		t.Fatalf("expected two reconnects, saw %d", cdn.requests.Load()-after)
	}
}

func TestSessionIdleClosesBodyAndResumes(t *testing.T) {
	data := testPattern(64 << 10)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	s := newTestSession(t, testTransport(server.Client(), &url, nil, nil), int64(len(data)))
	s.idleTimeout = 50 * time.Millisecond

	buf := make([]byte, 1024)
	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	if cdn.requests.Load() != 1 {
		t.Fatalf("expected 1 request, got %d", cdn.requests.Load())
	}

	time.Sleep(200 * time.Millisecond) // idle fires, body closes

	if _, err := io.ReadFull(s, buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, data[1024:2048]) {
		t.Fatal("wrong bytes after idle resume")
	}
	if cdn.requests.Load() != 2 {
		t.Fatalf("expected reconnect after idle, saw %d requests", cdn.requests.Load())
	}
}

func TestSessionStallWatchdogRecovers(t *testing.T) {
	data := testPattern(32 << 10)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)
	cdn.stallOnce.Store(true)

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	s := newTestSession(t, testTransport(server.Client(), &url, nil, nil), int64(len(data)))
	s.stallTimeout = 100 * time.Millisecond

	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("byte mismatch through stall recovery")
	}
}

func TestSessionNotPoisonedAfterExhaustion(t *testing.T) {
	data := testPattern(8 << 10)
	cdn := newFakeCDN(data)
	server := startCDN(t, cdn)
	cdn.mode.Store("forbidden") // 403 → refresh → 403 → ... until budget spent

	var url atomic.Value
	url.Store(cdn.url(server, ""))
	var refreshes atomic.Int64
	s := newTestSession(t, testTransport(server.Client(), &url, &refreshes, nil), int64(len(data)))

	if _, err := io.ReadAll(s); err == nil {
		t.Fatal("expected error while CDN is down")
	}
	if refreshes.Load() == 0 {
		t.Fatal("403s should have forced refreshes")
	}

	cdn.mode.Store("ok") // provider recovers; same session must work
	if _, err := s.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("session poisoned after recovery: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("byte mismatch after recovery")
	}
}

func TestSessionPrimeFailsFast(t *testing.T) {
	var url atomic.Value
	url.Store("http://127.0.0.1:0/unreachable")
	tr := &httpTransport{
		client: http.DefaultClient,
		getLink: func(context.Context) (types.DownloadLink, error) {
			return types.DownloadLink{}, link.NewPermanentError(link.ErrNoActiveAccount, "no_active_account")
		},
		refresh: func(_ context.Context, _ types.DownloadLink) (types.DownloadLink, error) {
			return types.DownloadLink{}, link.NewPermanentError(link.ErrNoActiveAccount, "no_active_account")
		},
	}
	s := newTestSession(t, tr, 1024)
	begin := time.Now()
	if err := s.Prime(); err == nil {
		t.Fatal("expected Prime to surface the open failure")
	}
	if elapsed := time.Since(begin); elapsed > time.Second {
		t.Fatalf("permanent error should fail fast, took %s", elapsed)
	}
}

// fakeUsenetHandle is a usenetFileHandle over an in-memory byte slice.
// If failAt >= 0, reads at or beyond that offset fail (the handle "loses"
// the tail) — a reopened handle without failAt serves it.
type fakeUsenetHandle struct {
	data   []byte
	failAt int64
	closed atomic.Bool
}

func (h *fakeUsenetHandle) ReadAtContext(_ context.Context, p []byte, off int64) (int, error) {
	if off >= int64(len(h.data)) {
		return 0, io.EOF
	}
	end := int64(len(h.data))
	if h.failAt >= 0 {
		if off >= h.failAt {
			return 0, errors.New("article fetch failed")
		}
		if end > h.failAt {
			end = h.failAt
		}
	}
	return copy(p, h.data[off:end]), nil
}

func (h *fakeUsenetHandle) Prefetch(context.Context, int64, int64) {}

func (h *fakeUsenetHandle) Close() error {
	h.closed.Store(true)
	return nil
}

func TestUsenetTransportResumesMidStream(t *testing.T) {
	data := testPattern(32 << 10)
	var opens atomic.Int64
	var handles []*fakeUsenetHandle
	tr := &usenetTransport{
		size: int64(len(data)),
		openFile: func(context.Context) (usenetFileHandle, error) {
			failAt := int64(-1)
			if opens.Add(1) == 1 {
				failAt = 500 // first handle dies after 500 bytes
			}
			h := &fakeUsenetHandle{data: data, failAt: failAt}
			handles = append(handles, h)
			return h, nil
		},
	}
	s := newTestSession(t, tr, int64(len(data)))

	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("byte mismatch: got %d bytes across %d opens", len(got), opens.Load())
	}
	if opens.Load() != 2 {
		t.Fatalf("expected exactly one resume, saw %d opens", opens.Load())
	}
	if !handles[0].closed.Load() {
		t.Fatal("failed handle was not closed on resume")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if !handles[1].closed.Load() {
		t.Fatal("live handle was not closed with the session")
	}
}

// TestSessionSeekDuringRecovery: while a Read is backing off (lock released),
// a Seek moves the position; the post-recovery reconnect must target the new
// offset instead of the stale one.
func TestSessionSeekDuringRecovery(t *testing.T) {
	data := testPattern(64 << 10)
	inRecover := make(chan struct{})
	releaseRecover := make(chan struct{})
	var recoverOnce sync.Once

	var opened []int64
	var mu sync.Mutex
	tr := &scriptedTransport{
		recoverFn: func(ctx context.Context, err error, attempt int) error {
			recoverOnce.Do(func() {
				close(inRecover)
				<-releaseRecover
			})
			return nil
		},
		openFn: func(ctx context.Context, pos int64) (io.ReadCloser, error) {
			mu.Lock()
			opened = append(opened, pos)
			n := len(opened)
			mu.Unlock()
			if n == 1 {
				return nil, errors.New("first open fails")
			}
			return io.NopCloser(bytes.NewReader(data[pos:])), nil
		},
	}
	s := newTestSession(t, tr, int64(len(data)))

	readDone := make(chan error, 1)
	buf := make([]byte, 4<<10)
	go func() {
		_, err := s.Read(buf)
		readDone <- err
	}()

	<-inRecover // Read is parked in transport recovery, lock released

	seekTo := int64(32 << 10)
	if _, err := s.Seek(seekTo, io.SeekStart); err != nil {
		t.Fatalf("Seek blocked or failed during recovery: %v", err)
	}
	close(releaseRecover)

	if err := <-readDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	last := opened[len(opened)-1]
	mu.Unlock()
	if last != seekTo {
		t.Fatalf("reconnect targeted %d, want the seeked offset %d", last, seekTo)
	}
	if !bytes.Equal(buf[:4<<10], data[seekTo:seekTo+4<<10]) {
		t.Fatal("read delivered bytes from the pre-seek offset")
	}
}

// scriptedTransport lets tests script open/recover behavior.
type scriptedTransport struct {
	openFn    func(ctx context.Context, pos int64) (io.ReadCloser, error)
	recoverFn func(ctx context.Context, err error, attempt int) error
}

func (t *scriptedTransport) open(ctx context.Context, pos int64) (io.ReadCloser, error) {
	return t.openFn(ctx, pos)
}

func (t *scriptedTransport) recover(ctx context.Context, err error, attempt int) error {
	return t.recoverFn(ctx, err, attempt)
}
