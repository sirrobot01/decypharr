package webdav

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/strm"
)

const testInfohash = "aabbccddeeff00112233445566778899aabbccdd"

// newStreamServer builds the stream routes against a real manager and
// storage, without the readiness middleware (the manager is never started).
func newStreamServer(t *testing.T) (*httptest.Server, *storage.Entry) {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	m := manager.New()
	t.Cleanup(func() { _ = m.Storage().Close() })

	entry := &storage.Entry{
		InfoHash: testInfohash,
		Name:     "Movie.2023",
		Files: map[string]*storage.File{
			"Movie.2023.mkv": {Name: "Movie.2023.mkv", Size: 1234, InfoHash: testInfohash},
		},
	}
	if err := m.Storage().AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(m)
	r := chi.NewRouter()
	r.Get("/stream/{infohash}/{fileID}/{name}", h.handleStream)
	r.Head("/stream/{infohash}/{fileID}/{name}", h.handleStream)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, entry
}

func streamURL(srv *httptest.Server, entry *storage.Entry, sig string) string {
	fileID := entry.Files["Movie.2023.mkv"].ID
	u := srv.URL + "/stream/" + entry.InfoHash + "/" + fileID + "/Movie.2023.mkv"
	if sig != "" {
		u += "?s=" + sig
	}
	return u
}

func TestStreamHeadServesFromStorageAlone(t *testing.T) {
	srv, entry := newStreamServer(t)

	resp, err := http.Head(streamURL(srv, entry, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HEAD status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Length"); got != "1234" {
		t.Errorf("Content-Length = %q", got)
	}
	if got := resp.Header.Get("Accept-Ranges"); got != "bytes" {
		t.Errorf("Accept-Ranges = %q", got)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}

	// ffprobe-style repeat probe with the cached validator.
	req, _ := http.NewRequest(http.MethodHead, streamURL(srv, entry, ""), nil)
	req.Header.Set("If-None-Match", etag)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match status = %d, want 304", resp2.StatusCode)
	}
}

func TestStreamAuth(t *testing.T) {
	srv, entry := newStreamServer(t)
	cfg := config.Get()
	cfg.UseAuth = true
	cfg.EnableWebdavAuth = true

	// No signature: 401 with a Basic challenge.
	resp, err := http.Head(streamURL(srv, entry, ""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned status = %d, want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") == "" {
		t.Error("missing WWW-Authenticate challenge")
	}

	// Tampered signature: still 401.
	resp, err = http.Head(streamURL(srv, entry, "deadbeef"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad-sig status = %d, want 401", resp.StatusCode)
	}

	// Valid signature: accepted.
	fileID := entry.Files["Movie.2023.mkv"].ID
	sig := strm.Sign(cfg.Strm.Secret, entry.InfoHash, fileID)
	resp, err = http.Head(streamURL(srv, entry, sig))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("signed status = %d, want 200", resp.StatusCode)
	}

	// Auth off: signatures are not enforced.
	cfg.UseAuth = false
	resp, err = http.Head(streamURL(srv, entry, ""))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("auth-off status = %d, want 200", resp.StatusCode)
	}
}

func TestStreamUnknownIdentity(t *testing.T) {
	srv, entry := newStreamServer(t)

	resp, err := http.Head(srv.URL + "/stream/" + entry.InfoHash + "/ffffffffffffffff/x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown fileID status = %d, want 404", resp.StatusCode)
	}

	resp, err = http.Head(srv.URL + "/stream/0000000000000000000000000000000000000000/ffffffffffffffff/x.mkv")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown infohash status = %d, want 404", resp.StatusCode)
	}
}
