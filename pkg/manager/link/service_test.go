package link

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

// A bare 400 on the first HEAD of a fresh TorBox link is transient (#434).
// It must not come back as a permanent error, or the file stays unreadable.
func TestValidateLinkBare400IsNotPermanent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	s := &Service{validated: xsync.NewMap[string, validation](), httpClient: srv.Client()}
	err := s.validateLink(context.Background(), &types.DownloadLink{DownloadLink: srv.URL, Filename: "a.mkv"})

	linkErr := GetLinkError(err)
	if linkErr == nil {
		t.Fatalf("validateLink() error = %v, want a link error", err)
	}
	if linkErr.IsPermanent() {
		t.Fatalf("400 classified as permanent, want refetchable")
	}
	if !linkErr.ShouldRefetch() {
		t.Fatalf("400 category = %s, want refetchable", linkErr.Category)
	}
}

func TestErrorCodeToLinkErrorUnknownIsNotPermanent(t *testing.T) {
	if ErrorCodeToLinkError("418").IsPermanent() {
		t.Fatal("unknown code classified as permanent")
	}
	for _, code := range []string{"404", "401", "link_not_found", "file_not_available"} {
		if !ErrorCodeToLinkError(code).IsPermanent() {
			t.Fatalf("%s no longer permanent", code)
		}
	}
}

// Deterministic download URLs never change the memo key, so a remembered
// failure has to expire on its own.
func TestFailedValidationExpires(t *testing.T) {
	s := &Service{validated: xsync.NewMap[string, validation]()}
	failed := NewPermanentError(Err404, "404")

	s.validated.Store("fresh", validation{err: failed, at: time.Now()})
	s.validated.Store("stale", validation{err: failed, at: time.Now().Add(-2 * failedValidationTTL)})
	s.validated.Store("ok", validation{at: time.Now().Add(-2 * failedValidationTTL)})

	if err, ok := s.loadValidation("fresh"); !ok || err == nil {
		t.Fatalf("fresh failure: got (%v, %v), want cached error", err, ok)
	}
	if _, ok := s.loadValidation("stale"); ok {
		t.Fatal("stale failure still cached")
	}
	if _, exists := s.validated.Load("stale"); exists {
		t.Fatal("stale failure not removed from the map")
	}
	if err, ok := s.loadValidation("ok"); !ok || err != nil {
		t.Fatalf("successful validation: got (%v, %v), want cached success", err, ok)
	}
}
