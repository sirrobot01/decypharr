package usenet

import (
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/dylanmazurek/decypharr/internal/customerror"
	"github.com/dylanmazurek/decypharr/internal/nntp"
	"github.com/dylanmazurek/decypharr/pkg/storage"
)

func TestPreStreamChecksPreservesArticleNotFoundClassification(t *testing.T) {
	client := &Usenet{failedFiles: xsync.NewMap[string, error]()}
	file := &storage.NZBFile{
		NzbID:    "download-1",
		Name:     "movie.mkv",
		Segments: []storage.NZBSegment{{MessageID: "segment-1"}},
	}
	client.failedFiles.Store(fsKey(file.NzbID, file.Name), &nntp.Error{Type: nntp.ErrorTypeArticleNotFound})

	if err := client.preStreamChecks(file); !customerror.IsArticleNotFoundError(err) {
		t.Fatalf("error = %v, want typed article-not-found", err)
	}
}
