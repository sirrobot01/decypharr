package usenet

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestPreStreamChecksPreservesArticleNotFoundClassification(t *testing.T) {
	client := &Usenet{failedFiles: xsync.NewMap[string, failedFile]()}
	file := &storage.NZBFile{
		NzbID:    "download-1",
		Name:     "movie.mkv",
		Segments: []storage.NZBSegment{{MessageID: "segment-1"}},
	}
	client.failedFiles.Store(fsKey(file.NzbID, file.Name), failedFile{err: &nntp.Error{Type: nntp.ErrorTypeArticleNotFound}, at: time.Now()})

	if err := client.preStreamChecks(file); !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("error = %v, want typed article-not-found", err)
	}
}

func TestPreStreamChecksRetriesAfterFailedFileRetry(t *testing.T) {
	client := &Usenet{failedFiles: xsync.NewMap[string, failedFile]()}
	file := &storage.NZBFile{
		NzbID:    "download-1",
		Name:     "movie.mkv",
		Segments: []storage.NZBSegment{{MessageID: "segment-1"}},
	}
	client.failedFiles.Store(fsKey(file.NzbID, file.Name), failedFile{
		err: &nntp.Error{Type: nntp.ErrorTypeArticleNotFound},
		at:  time.Now().Add(-failedFileRetry),
	})

	if err := client.preStreamChecks(file); err != nil {
		t.Fatalf("error = %v, want a new try after %s", err, failedFileRetry)
	}
	if _, ok := client.failedFiles.Load(fsKey(file.NzbID, file.Name)); ok {
		t.Fatal("expired failure was kept")
	}
}
