package usenet

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

var notFound = &nntp.Error{Type: nntp.ErrorTypeArticleNotFound}

func newFailureTestClient() (*Usenet, *storage.NZBFile, string) {
	file := &storage.NZBFile{
		NzbID:    "download-1",
		Name:     "movie.mkv",
		Segments: []storage.NZBSegment{{MessageID: "segment-1"}},
	}
	return &Usenet{failedFiles: xsync.NewMap[string, failedFile]()}, file, fsKey(file.NzbID, file.Name)
}

// endWait moves the end of the current wait back by d past now.
func endWait(u *Usenet, key string, d time.Duration) {
	failed, _ := u.failedFiles.Load(key)
	failed.until = time.Now().Add(-d)
	u.failedFiles.Store(key, failed)
}

func TestPreStreamChecksPreservesArticleNotFoundClassification(t *testing.T) {
	client, file, key := newFailureTestClient()
	client.markFailed(key, notFound)

	if err := client.preStreamChecks(file); !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("error = %v, want typed article-not-found", err)
	}
}

func TestFailedFileRetryDoublesUpToMax(t *testing.T) {
	client, file, key := newFailureTestClient()
	want := failedFileRetry
	for range 8 {
		client.markFailed(key, notFound)
		if got, _ := client.failedFiles.Load(key); got.wait != want {
			t.Fatalf("wait = %s, want %s", got.wait, want)
		}
		endWait(client, key, time.Second)
		if err := client.preStreamChecks(file); err != nil {
			t.Fatalf("error after the wait = %v, want a new try", err)
		}
		want = min(want*2, failedFileMaxRetry)
	}
}

func TestFailedFileSecondFailureInSameWaitKeepsWait(t *testing.T) {
	client, _, key := newFailureTestClient()
	client.markFailed(key, notFound)
	client.markFailed(key, notFound)
	if got, _ := client.failedFiles.Load(key); got.wait != failedFileRetry {
		t.Fatalf("wait = %s, want %s", got.wait, failedFileRetry)
	}
}

func TestOldFailedFileStartsAgain(t *testing.T) {
	client, file, key := newFailureTestClient()
	client.markFailed(key, notFound)
	client.markFailed(key, notFound)
	endWait(client, key, time.Second)
	client.markFailed(key, notFound) // wait is now an hour

	endWait(client, key, failedFileMaxRetry)
	if err := client.preStreamChecks(file); err != nil {
		t.Fatalf("error = %v, want a new try", err)
	}
	if _, ok := client.failedFiles.Load(key); ok {
		t.Fatal("old failure was kept")
	}
	client.markFailed(key, notFound)
	if got, _ := client.failedFiles.Load(key); got.wait != failedFileRetry {
		t.Fatalf("wait = %s, want %s", got.wait, failedFileRetry)
	}
}
