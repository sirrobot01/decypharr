package manager

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type fakeArrRecovery struct {
	binding reacquire.Binding

	calls     atomic.Int64
	startOnce sync.Once
	started   chan struct{}
	release   chan struct{}
	finished  chan struct{}
	requestMu sync.Mutex
	request   reacquire.Request
}

func (f *fakeArrRecovery) Lookup(entryID, fileID string) (reacquire.Binding, bool) {
	if f.binding.EntryID != entryID || f.binding.EntryFileID != fileID {
		return reacquire.Binding{}, false
	}
	return f.binding, true
}

func (f *fakeArrRecovery) Reacquire(request reacquire.Request) (*reacquire.Job, error) {
	f.calls.Add(1)
	f.requestMu.Lock()
	f.request = request
	f.requestMu.Unlock()
	f.startOnce.Do(func() { close(f.started) })
	if f.release != nil {
		<-f.release
	}
	if f.finished != nil {
		close(f.finished)
	}
	return &reacquire.Job{ID: "reacquire-1"}, nil
}

func TestActiveStreamIncludesArrBinding(t *testing.T) {
	recovery := &fakeArrRecovery{
		binding: reacquire.Binding{
			ArrName:      "sonarr-main",
			ArrType:      arr.Sonarr,
			EntryID:      "nzb-1",
			EntryFileID:  "file-1",
			SeriesID:     42,
			SeasonNumber: 3,
			EpisodeIDs:   []int{101, 102},
		},
	}
	m := &Manager{activeStreams: xsync.NewMap[string, *ActiveStream]()}
	m.SetArrRecovery(recovery)
	entry := &storage.Entry{
		Protocol: config.ProtocolNZB,
		InfoHash: "nzb-1",
		Name:     "Show.S03",
		Files: map[string]*storage.File{
			"Show.S03E01-E02.mkv": {
				ID:   "file-1",
				Name: "Show.S03E01-E02.mkv",
				Size: 2 << 30,
			},
		},
	}

	streamID := m.TrackStream(entry, "Show.S03E01-E02.mkv", "DFS")
	t.Cleanup(func() { m.UntrackStream(streamID) })
	recovery.binding.EpisodeIDs[0] = 999

	streams := m.GetActiveStreams()
	if len(streams) != 1 {
		t.Fatalf("active streams = %d, want 1", len(streams))
	}
	stream := streams[0]
	if stream.EntryID != "nzb-1" || stream.FileID != "file-1" {
		t.Fatalf("stream identity = %q/%q, want nzb-1/file-1", stream.EntryID, stream.FileID)
	}
	if stream.ArrName != "sonarr-main" || stream.ArrType != arr.Sonarr {
		t.Fatalf("Arr metadata = %q/%q", stream.ArrName, stream.ArrType)
	}
	if stream.SeriesID != 42 || stream.SeasonNumber != 3 {
		t.Fatalf("series metadata = %d season %d", stream.SeriesID, stream.SeasonNumber)
	}
	if len(stream.EpisodeIDs) != 2 || stream.EpisodeIDs[0] != 101 || stream.EpisodeIDs[1] != 102 {
		t.Fatalf("episode IDs = %v, want [101 102]", stream.EpisodeIDs)
	}

	m.setStreamReacquireJob("nzb-1", "file-1", "reacquire-1")
	if got := m.GetActiveStreams()[0].ReacquireJobID; got != "reacquire-1" {
		t.Fatalf("reacquire job ID = %q, want reacquire-1", got)
	}
}

type missingArticleReader struct{}

func (missingArticleReader) ReadAtContext(context.Context, []byte, int64) (int, error) {
	return 0, customerror.NewArticleNotFoundError(nil)
}

func (missingArticleReader) Prefetch(context.Context, int64, int64) {}
func (missingArticleReader) Close() error                           { return nil }

func TestUsenetPrimeSurfacesMissingArticle(t *testing.T) {
	var opens atomic.Int64
	var notifications atomic.Int64
	transport := &usenetTransport{
		size: 4096,
		openFile: func(context.Context) (DirectReader, error) {
			opens.Add(1)
			return missingArticleReader{}, nil
		},
		onArticleNotFound: func() {
			notifications.Add(1)
		},
	}
	stream := newSession(t.Context(), transport, 4096, 2048)
	t.Cleanup(func() { _ = stream.Close() })

	err := stream.Prime()
	if !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("Prime error = %v, want article-not-found", err)
	}
	if got := opens.Load(); got != 1 {
		t.Fatalf("Prime opened %d handles, want 1", got)
	}
	if got := notifications.Load(); got != 1 {
		t.Fatalf("article-not-found notifications = %d, want 1", got)
	}
	if stream.body != nil {
		t.Fatal("failed Prime retained a response body")
	}
}

func TestUsenetArticleNotFoundQueuesOneNonBlockingReacquire(t *testing.T) {
	recovery := &fakeArrRecovery{
		binding: reacquire.Binding{
			EntryID:     "nzb-1",
			EntryFileID: "file-1",
		},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		finished: make(chan struct{}),
	}
	m := &Manager{activeStreams: xsync.NewMap[string, *ActiveStream]()}
	m.SetArrRecovery(recovery)

	newStream := func(opens *atomic.Int64) *session {
		transport := &usenetTransport{
			size: 4096,
			openFile: func(context.Context) (DirectReader, error) {
				opens.Add(1)
				return missingArticleReader{}, nil
			},
			onArticleNotFound: func() {
				m.submitStreamReacquire("nzb-1", "file-1")
			},
		}
		stream := newSession(t.Context(), transport, 4096, 0)
		t.Cleanup(func() { _ = stream.Close() })
		return stream
	}

	var firstOpens atomic.Int64
	first := newStream(&firstOpens)
	readDone := make(chan error, 1)
	go func() {
		_, err := first.Read(make([]byte, 128))
		readDone <- err
	}()

	select {
	case err := <-readDone:
		if !customerror.IsArticleNotFoundError(err) {
			t.Fatalf("read error = %v, want permanent article-not-found", err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream read blocked on reacquisition submission")
	}
	select {
	case <-recovery.started:
	case <-time.After(time.Second):
		t.Fatal("reacquisition was not submitted")
	}

	if _, err := first.Read(make([]byte, 128)); !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("second read error = %v, want article-not-found", err)
	}
	if got := firstOpens.Load(); got != 1 {
		t.Fatalf("irrecoverable NZB opened %d times, want 1", got)
	}

	var secondOpens atomic.Int64
	second := newStream(&secondOpens)
	if _, err := second.Read(make([]byte, 128)); !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("parallel read error = %v, want article-not-found", err)
	}
	if got := recovery.calls.Load(); got != 1 {
		t.Fatalf("reacquire calls = %d, want 1", got)
	}

	recovery.requestMu.Lock()
	request := recovery.request
	recovery.requestMu.Unlock()
	if request.EntryID != "nzb-1" || request.FileID != "file-1" || request.Cause != reacquire.CauseStream {
		t.Fatalf("reacquire request = %+v", request)
	}

	close(recovery.release)
	select {
	case <-recovery.finished:
	case <-time.After(time.Second):
		t.Fatal("reacquisition submission did not finish")
	}
	if got := recovery.calls.Load(); got != 1 {
		t.Fatalf("reacquire calls after completion = %d, want 1", got)
	}
}

var _ DirectReader = missingArticleReader{}
