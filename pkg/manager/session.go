package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dylanmazurek/decypharr/internal/config"
	"github.com/dylanmazurek/decypharr/internal/customerror"
	"github.com/dylanmazurek/decypharr/pkg/debrid/types"
	"github.com/dylanmazurek/decypharr/pkg/manager/link"
	"github.com/dylanmazurek/decypharr/pkg/storage"
	"github.com/dylanmazurek/decypharr/pkg/usenet"
)

const (
	// sessionIdleTimeout closes the underlying transport (not the session)
	// after this long without a Read; the next Read reopens at the current
	// offset via the resume path. Keeps long-lived sessions from pinning
	// debrid concurrent-connection slots.
	sessionIdleTimeout = 30 * time.Second
	// sessionStallTimeout bounds a single blocking Read with no bytes
	// flowing; matches the vfs downloader no-progress watchdog.
	sessionStallTimeout = 90 * time.Second
	// sessionSeekDiscardMax is the largest forward seek served by discarding
	// bytes from the open body instead of reconnecting.
	sessionSeekDiscardMax = 256 << 10
	// sessionMaxResumes bounds recovery attempts within a single Read/Prime
	// call; any successful byte resets the budget.
	sessionMaxResumes = 4
	// sessionMaxThrottleWait caps how long a Retry-After is honored.
	sessionMaxThrottleWait = 30 * time.Second
	sessionResumeBaseDelay = 500 * time.Millisecond
)

// StreamReader is a resilient, seekable byte stream over one remote file.
// All recovery (link refresh, backoff, reconnect-at-offset) happens inside
// Read; consumers see either bytes or a typed error once recovery is
// exhausted. Transient failures do not poison the session; confirmed
// permanent content failures remain terminal.
type StreamReader interface {
	io.ReadCloser
	io.Seeker
	// Size returns the total file size in bytes.
	Size() int64
	// Prime opens the transport eagerly so open failures surface before any
	// bytes are consumed (e.g. before writing HTTP response headers).
	Prime() error
}

// CacheRangeAcknowledger is an optional capability implemented by protocol
// sessions whose source maintains a disposable delivery cache. DFS calls it
// only after its own cache has accepted an independent copy of the range.
type CacheRangeAcknowledger interface {
	AcknowledgeCachedRange(off, length int64)
}

type cachedRangeReleaser interface {
	ReleaseCachedRange(off, length int64)
}

// transport supplies protocol-specific connect and recovery for a session.
type transport interface {
	// open returns a body positioned at pos reading toward EOF.
	open(ctx context.Context, pos int64) (io.ReadCloser, error)
	// recover reacts to a failed open/read: refresh the link, back off, etc.
	// A nil return means the session may retry; an error is terminal for the
	// current Read/Prime call.
	recover(ctx context.Context, err error, attempt int) error
}

type session struct {
	ctx    context.Context
	cancel context.CancelFunc
	t      transport
	size   int64

	idleTimeout  time.Duration
	stallTimeout time.Duration

	mu         sync.Mutex
	pos        int64
	body       io.ReadCloser
	bodyCancel context.CancelFunc
	closed     bool
	// cacheReleaser follows the most recently opened body. It deliberately
	// survives a body EOF until the caller has published bytes returned by
	// that final Read into its downstream cache.
	cacheReleaser cachedRangeReleaser

	// idle is a persistent timer, created on first arm and Reset thereafter.
	// idleGen/idleArmGen invalidate stale firings: Read bumps idleGen on
	// entry, arming records it, and a firing whose armed gen is stale is a
	// no-op.
	idle       *time.Timer
	idleGen    uint64
	idleArmGen uint64

	// stall is the per-read watchdog, also persistent. Its callback must not
	// take s.mu (it fires while Read holds the lock across body.Read); it
	// invokes stallCancel, which holds the current body's cancel or a no-op.
	// A firing that slips past Stop cancels a body whose read already
	// returned; the next Read takes the resume path.
	stall       *time.Timer
	stallCancel atomic.Value // context.CancelFunc, never nil once stored

	resumes atomic.Int64
	onRead  func(resumes int64)
	onClose func()
}

var noopCancel context.CancelFunc = func() {}

func newSession(ctx context.Context, t transport, size, offset int64) *session {
	sctx, cancel := context.WithCancel(ctx)
	return &session{
		ctx:          sctx,
		cancel:       cancel,
		t:            t,
		size:         size,
		pos:          offset,
		idleTimeout:  sessionIdleTimeout,
		stallTimeout: sessionStallTimeout,
	}
}

func (s *session) Size() int64 { return s.size }

// AcknowledgeCachedRange forwards downstream ownership only for transports
// that expose the capability. HTTP/debrid sessions are a no-op.
func (s *session) AcknowledgeCachedRange(off, length int64) {
	if length <= 0 || off < 0 {
		return
	}
	s.mu.Lock()
	releaser := s.cacheReleaser
	s.mu.Unlock()
	if releaser != nil {
		releaser.ReleaseCachedRange(off, length)
	}
}

func (s *session) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if s.pos >= s.size {
		return 0, io.EOF
	}
	s.idleGen++ // invalidate any pending idle firing
	if s.idle != nil {
		s.idle.Stop()
	}

	for attempt := 0; ; attempt++ {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		if s.body == nil {
			if err := s.connectLocked(); err != nil {
				if rerr := s.recoverStep(err, &attempt); rerr != nil {
					return 0, rerr
				}
				continue
			}
		}

		// Bound this read attempt: if no bytes flow for stallTimeout the
		// body context is cancelled, the read errors, and we resume.
		s.stallCancel.Store(s.bodyCancel)
		if s.stall == nil {
			s.stall = time.AfterFunc(s.stallTimeout, s.stallFired)
		} else {
			s.stall.Reset(s.stallTimeout)
		}
		n, err := s.body.Read(p)
		s.stall.Stop()
		s.stallCancel.Store(noopCancel)
		if n > len(p) {
			// Every consumer copies through a buffer sized to len(p); a body
			// that over-reports makes them slice past it. Drop the body and
			// report the violation rather than hand back a bogus count.
			s.closeBodyLocked()
			return 0, fmt.Errorf("stream body returned %d bytes for a %d-byte read", n, len(p))
		}
		s.pos += int64(n)

		if n > 0 {
			if err != nil {
				// Deliver the bytes; the next Read deals with the error.
				s.closeBodyLocked()
			} else {
				s.armIdleLocked()
			}
			if s.onRead != nil {
				s.onRead(s.resumes.Load())
			}
			return n, nil
		}

		s.closeBodyLocked()
		switch {
		case err == nil:
			err = io.ErrNoProgress
		case err == io.EOF:
			if s.pos >= s.size {
				return 0, io.EOF
			}
			err = io.ErrUnexpectedEOF // short body: resume at current offset
		}
		if rerr := s.recoverStep(err, &attempt); rerr != nil {
			return 0, rerr
		}
	}
}

// recoverStep consults the transport about err. It returns nil when the
// session should retry (counting a resume), or the terminal error.
//
// Caller holds s.mu; the lock is released across the transport recovery
// (link refresh and backoff sleeps can take tens of seconds). The body is
// always nil at this point, so a Seek landing meanwhile just moves s.pos,
// which the post-recovery reconnect targets.
func (s *session) recoverStep(err error, attempt *int) error {
	if cerr := s.ctx.Err(); cerr != nil {
		return cerr
	}
	if *attempt >= sessionMaxResumes {
		return err
	}
	s.mu.Unlock()
	rerr := s.t.recover(s.ctx, err, *attempt)
	s.mu.Lock()
	if s.closed {
		return os.ErrClosed
	}
	if rerr != nil {
		return rerr
	}
	s.resumes.Add(1)
	return nil
}

func (s *session) Prime() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.body != nil || s.pos >= s.size {
		return nil
	}
	for attempt := 0; ; attempt++ {
		if err := s.ctx.Err(); err != nil {
			return err
		}
		err := s.connectLocked()
		if err == nil {
			s.armIdleLocked()
			return nil
		}
		if rerr := s.recoverStep(err, &attempt); rerr != nil {
			return rerr
		}
	}
}

func (s *session) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, os.ErrClosed
	}
	var next int64
	switch whence {
	case io.SeekStart:
		next = offset
	case io.SeekCurrent:
		next = s.pos + offset
	case io.SeekEnd:
		next = s.size + offset
	default:
		return 0, fmt.Errorf("invalid seek whence %d", whence)
	}
	if next < 0 {
		return 0, fmt.Errorf("invalid seek to %d", next)
	}
	if next == s.pos {
		return next, nil
	}
	if s.body != nil && next > s.pos && next-s.pos <= sessionSeekDiscardMax {
		if _, err := io.CopyN(io.Discard, s.body, next-s.pos); err != nil {
			s.closeBodyLocked()
		}
	} else {
		s.closeBodyLocked()
	}
	s.pos = next
	return next, nil
}

func (s *session) Close() error {
	s.cancel() // unblock any in-flight Read before taking the lock
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.closeBodyLocked()
	s.cacheReleaser = nil
	if s.idle != nil {
		s.idle.Stop()
	}
	if s.stall != nil {
		s.stall.Stop()
	}
	s.mu.Unlock()
	if s.onClose != nil {
		s.onClose()
	}
	return nil
}

func (s *session) connectLocked() error {
	bctx, cancel := context.WithCancel(s.ctx)
	body, err := s.t.open(bctx, s.pos)
	if err != nil {
		cancel()
		return err
	}
	s.body = body
	if releaser, ok := body.(cachedRangeReleaser); ok {
		s.cacheReleaser = releaser
	}
	s.bodyCancel = cancel
	return nil
}

func (s *session) closeBodyLocked() {
	if s.body != nil {
		_ = s.body.Close()
		s.body = nil
	}
	if s.bodyCancel != nil {
		s.bodyCancel()
		s.bodyCancel = nil
	}
}

// stallFired runs on the timer goroutine while a Read may be holding s.mu
// across a blocking body read — so it must not lock; it only cancels the
// current body context (or no-ops).
func (s *session) stallFired() {
	if c, ok := s.stallCancel.Load().(context.CancelFunc); ok && c != nil {
		c()
	}
}

func (s *session) armIdleLocked() {
	if s.idleTimeout <= 0 || s.body == nil {
		return
	}
	s.idleGen++
	s.idleArmGen = s.idleGen
	if s.idle == nil {
		s.idle = time.AfterFunc(s.idleTimeout, s.idleFired)
	} else {
		s.idle.Reset(s.idleTimeout)
	}
}

func (s *session) idleFired() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.idleArmGen != s.idleGen {
		return
	}
	s.closeBodyLocked()
}

// httpTransport serves debrid links over HTTP with link refresh on failure.
type httpTransport struct {
	client  *http.Client
	getLink func(ctx context.Context) (types.DownloadLink, error)
	refresh func(ctx context.Context, bad types.DownloadLink) (types.DownloadLink, error)

	// base/limit map file-relative session offsets onto the link target when
	// the file is a byte-ranged slice of a larger download: the served bytes
	// are [base+pos, base+limit). limit==0 means the whole link target. Only
	// the HTTP Range header sees the base; session pos/size stay
	// file-relative.
	base  int64
	limit int64

	mu   sync.Mutex
	last types.DownloadLink
}

func (t *httpTransport) open(ctx context.Context, pos int64) (io.ReadCloser, error) {
	dl, err := t.getLink(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.last = dl
	t.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, dl.DownloadLink, nil)
	if err != nil {
		return nil, link.NewPermanentError(err, "request_creation_failed")
	}
	// A bounded slice also bounds the request end so the body EOFs at the
	// slice boundary.
	absStart := t.base + pos
	if t.limit > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(absStart, 10)+"-"+strconv.FormatInt(t.base+t.limit-1, 10))
	} else {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(absStart, 10)+"-")
	}
	req.Header.Set("Accept-Encoding", "identity")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, link.ClassifyTransportError(err)
	}
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		return resp.Body, nil
	case resp.StatusCode == http.StatusOK && absStart == 0 && t.limit <= 0:
		return resp.Body, nil
	case resp.StatusCode == http.StatusOK && t.limit <= 0 && absStart > 0 && absStart <= sessionSeekDiscardMax:
		// Server ignored the Range header but the offset is small enough to
		// discard our way to it.
		if _, err := io.CopyN(io.Discard, resp.Body, absStart); err != nil {
			resp.Body.Close()
			return nil, link.ClassifyTransportError(err)
		}
		return resp.Body, nil
	case resp.StatusCode == http.StatusOK:
		// Byte-ranged slices can't fall back to discarding: without range
		// support the body would run past the slice end.
		resp.Body.Close()
		return nil, link.NewPermanentError(
			fmt.Errorf("server ignored range request at offset %d", absStart), "no_range_support")
	default:
		status, header := resp.StatusCode, resp.Header
		_, _ = io.CopyN(io.Discard, resp.Body, 512)
		resp.Body.Close()
		return nil, link.ClassifyStreamStatus(status, header)
	}
}

func (t *httpTransport) recover(ctx context.Context, err error, attempt int) error {
	lerr := link.GetLinkError(err)
	if lerr == nil {
		lerr = link.ClassifyTransportError(err)
	}
	switch {
	case lerr.IsPermanent():
		return err
	case lerr.ShouldBackoff():
		wait := lerr.RetryAfter()
		if wait <= 0 {
			wait = time.Duration(sessionBackoff(attempt))
		}
		return sleepCtx(ctx, min(wait, sessionMaxThrottleWait))
	case lerr.ShouldRefetch(), lerr.ShouldDisableAccount():
		t.mu.Lock()
		bad := t.last
		t.mu.Unlock()
		if bad.Empty() {
			return nil // nothing to invalidate; next open re-fetches
		}
		if _, rerr := t.refresh(ctx, bad); rerr != nil {
			return rerr
		}
		return nil
	default: // retryable: same link, short backoff
		return sleepCtx(ctx, sessionBackoff(attempt))
	}
}

// usenetTransport serves a session body by pulling directly from a usenet
// FileHandle. Per-segment backbone failover lives inside the usenet client;
// retries here only reopen recoverable handles at the current offset.
type usenetTransport struct {
	size              int64
	openFile          func(ctx context.Context) (DirectReader, error)
	onArticleNotFound func()
	irrecoverable     atomic.Bool
	notifyOnce        sync.Once
}

// DirectReader is a protocol-native random-access reader. The opener selects
// whether it owns rewind persistence or sits beneath a downstream cache.
type DirectReader interface {
	ReadAtContext(ctx context.Context, p []byte, off int64) (int, error)
	Prefetch(ctx context.Context, off, length int64)
	Close() error
}

// RewindOwner identifies the layer that retains already-delivered bytes.
type RewindOwner uint8

const (
	RewindOwnerApplication RewindOwner = iota
	RewindOwnerDownstream
)

type usenetBody struct {
	h       DirectReader
	ctx     context.Context
	pos     int64
	size    int64
	onError func(error)
}

func (b *usenetBody) Read(p []byte) (int, error) {
	if b.pos >= b.size {
		return 0, io.EOF
	}
	if int64(len(p)) > b.size-b.pos {
		p = p[:b.size-b.pos]
	}
	n, err := b.h.ReadAtContext(b.ctx, p, b.pos)
	b.pos += int64(n)
	if err != nil && b.onError != nil {
		b.onError(err)
	}
	if err == nil && n == 0 {
		err = io.ErrNoProgress
	}
	return n, err
}

func (b *usenetBody) Close() error { return b.h.Close() }

func (b *usenetBody) ReleaseCachedRange(off, length int64) {
	if releaser, ok := b.h.(cachedRangeReleaser); ok {
		releaser.ReleaseCachedRange(off, length)
	}
}

func (t *usenetTransport) open(ctx context.Context, pos int64) (io.ReadCloser, error) {
	if t.irrecoverable.Load() {
		return nil, customerror.NewArticleNotFoundError(nil)
	}
	h, err := t.openFile(ctx)
	if err != nil {
		t.markArticleNotFound(err)
		return nil, err
	}
	// Unlike an HTTP transport, opening a Usenet handle performs no upstream
	// I/O. Demand-read one byte so Session.Prime can reject a missing article
	// before an HTTP/WebDAV handler commits successful response headers. The
	// reader caches the containing segment, so the subsequent body read does
	// not download it again and still starts at pos.
	var probe [1]byte
	n, err := h.ReadAtContext(ctx, probe[:], pos)
	if err != nil && (!errors.Is(err, io.EOF) || n != len(probe)) {
		_ = h.Close()
		t.markArticleNotFound(err)
		return nil, err
	}
	if n != len(probe) {
		_ = h.Close()
		return nil, fmt.Errorf("usenet stream prime at offset %d read %d bytes, want %d: %w", pos, n, len(probe), io.ErrUnexpectedEOF)
	}
	// Warm the read-ahead window from the starting offset (bounded inside).
	h.Prefetch(ctx, pos, t.size-pos)
	return &usenetBody{
		h:    h,
		ctx:  ctx,
		pos:  pos,
		size: t.size,
		onError: func(err error) {
			t.markArticleNotFound(err)
		},
	}, nil
}

func (t *usenetTransport) recover(ctx context.Context, err error, attempt int) error {
	if t.markArticleNotFound(err) {
		return err
	}
	if customErr, ok := errors.AsType[*customerror.Error](err); ok && customErr.IsPermanent() {
		return err
	}
	if lerr := link.GetLinkError(err); lerr != nil && lerr.IsPermanent() {
		return err
	}
	return sleepCtx(ctx, sessionBackoff(attempt))
}

func (t *usenetTransport) markArticleNotFound(err error) bool {
	if !customerror.IsArticleNotFoundError(err) {
		return false
	}
	t.irrecoverable.Store(true)
	t.notifyOnce.Do(func() {
		if t.onArticleNotFound != nil {
			t.onArticleNotFound()
		}
	})
	return true
}

// OpenStream opens a resilient, seekable session over one file of an entry and
// registers it in the active-streams view. client identifies the consumer
// (e.g. a User-Agent, "NFS"). Connecting is lazy — use Prime to fast-fail.
func (m *Manager) OpenStream(ctx context.Context, entry *storage.Entry, filename string, offset int64, client string) (StreamReader, error) {
	return m.OpenStreamWithRewindOwner(ctx, entry, filename, offset, client, RewindOwnerApplication)
}

// OpenStreamWithRewindOwner opens a stream with an explicit retention owner.
func (m *Manager) OpenStreamWithRewindOwner(ctx context.Context, entry *storage.Entry, filename string, offset int64, client string, owner RewindOwner) (StreamReader, error) {
	s, source, debrid, err := m.openSession(ctx, entry, filename, offset, owner)
	if err != nil {
		return nil, err
	}
	streamID := m.registerStream(entry, filename, entry.Files[filename], source, debrid, client)
	s.onRead = func(resumes int64) { m.touchStream(streamID, resumes) }
	s.onClose = func() { m.unregisterStream(streamID) }
	return s, nil
}

// SupportsDirectRead reports whether an entry has a protocol-native
// random-access reader. It does not imply that a downstream persistent cache
// should be bypassed; cache owners should use OpenStreamUntrackedForCache.
func SupportsDirectRead(entry *storage.Entry) bool {
	return entry != nil && entry.Protocol == config.ProtocolNZB
}

// OpenDirect opens an application-owned protocol reader without active-stream
// tracking. It is retained for non-cache consumers that explicitly need native
// random access. Persistent cache layers should use
// OpenStreamUntrackedForCache so cache ownership and observability stay intact.
func (m *Manager) OpenDirect(ctx context.Context, entry *storage.Entry, filename string) (DirectReader, error) {
	if !SupportsDirectRead(entry) {
		return nil, nil
	}
	if m.usenet == nil {
		return nil, fmt.Errorf("usenet client not configured")
	}
	return m.usenet.OpenFileWithRetention(ctx, entry.InfoHash, filename, usenet.RetentionRewind)
}

// OpenStreamUntracked opens a session without registering it in the
// active-streams view. It is for consumers that do their own stream tracking —
// for example a background sidecar download.
func (m *Manager) OpenStreamUntracked(ctx context.Context, entry *storage.Entry, filename string, offset int64) (StreamReader, error) {
	s, _, _, err := m.openSession(ctx, entry, filename, offset, RewindOwnerApplication)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// OpenStreamUntrackedForCache opens an untracked session for a downstream
// cache. The downstream owns rewind persistence, so protocol transports keep
// only their active delivery window. DFS uses this to avoid writing every NZB
// byte into both the Usenet session cache and the persistent DFS cache.
//
// The caller is responsible for aggregate stream tracking. DFS tracks once at
// the shared Downloaders level rather than once per read-ahead worker.
func (m *Manager) OpenStreamUntrackedForCache(ctx context.Context, entry *storage.Entry, filename string, offset int64) (StreamReader, error) {
	s, _, _, err := m.openSession(ctx, entry, filename, offset, RewindOwnerDownstream)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// OpenStreamForFile opens a tracked session for a catalog FileInfo, resolving
// the backing storage entry by name. It lets the NFS layer stream without
// depending on storage.Entry directly. client identifies the consumer.
func (m *Manager) OpenStreamForFile(ctx context.Context, info *FileInfo, offset int64, client string) (StreamReader, error) {
	entry, err := m.GetEntryByName(info.Parent(), info.Name())
	if err != nil {
		return nil, fmt.Errorf("resolve entry for %s/%s: %w", info.Parent(), info.Name(), err)
	}
	return m.OpenStream(ctx, entry, info.Name(), offset, client)
}

func (m *Manager) openSession(ctx context.Context, entry *storage.Entry, filename string, offset int64, owner RewindOwner) (*session, string, string, error) {
	file, ok := entry.Files[filename]
	if !ok {
		return nil, "", "", fmt.Errorf("file %s not found in entry %s", filename, entry.Name)
	}
	if file.Size <= 0 {
		return nil, "", "", fmt.Errorf("file %s has invalid size %d", filename, file.Size)
	}
	if offset < 0 || offset > file.Size {
		return nil, "", "", fmt.Errorf("offset %d out of range for %s (size %d)", offset, filename, file.Size)
	}

	var t transport
	source, debrid := "torrent", entry.ActiveProvider
	if entry.Protocol == config.ProtocolNZB {
		if m.usenet == nil {
			return nil, "", "", fmt.Errorf("usenet client not configured")
		}
		source, debrid = "nzb", ""
		nzoID := entry.InfoHash
		retention := retentionForOwner(owner)
		t = &usenetTransport{
			size: file.Size,
			openFile: func(ctx context.Context) (DirectReader, error) {
				return m.usenet.OpenFileWithRetention(ctx, nzoID, filename, retention)
			},
			onArticleNotFound: func() {
				m.submitStreamReacquire(entry.InfoHash, file.ID)
			},
		}
	} else {
		ht := &httpTransport{
			client: m.streamClient,
			getLink: func(ctx context.Context) (types.DownloadLink, error) {
				return m.linkService.GetLink(ctx, entry, filename)
			},
			refresh: func(ctx context.Context, bad types.DownloadLink) (types.DownloadLink, error) {
				return m.linkService.Refresh(ctx, entry, bad)
			},
		}
		if br := file.ByteRange; br != nil {
			ht.base = br[0]
			ht.limit = file.Size
		}
		t = ht
	}

	return newSession(ctx, t, file.Size, offset), source, debrid, nil
}

func retentionForOwner(owner RewindOwner) usenet.Retention {
	if owner == RewindOwnerDownstream {
		return usenet.RetentionDelivery
	}
	return usenet.RetentionRewind
}

func sessionBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0 // first retry is immediate: most blips are one-shot
	}
	return sessionResumeBaseDelay << min(attempt-1, 3) // 0.5s, 1s, 2s, 4s
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
