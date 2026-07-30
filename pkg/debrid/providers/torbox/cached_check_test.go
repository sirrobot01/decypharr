package torbox

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

// config.Get() is reached through SubmitMagnet and calls os.Exit(1) when it
// cannot write its file, so point it at a scratch directory before any test runs.
// os.Exit skips deferred calls, hence the explicit cleanup around m.Run().
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "decypharr-torbox-test")
	if err != nil {
		panic(err)
	}
	config.SetConfigPath(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

const testHash = "0123456789abcdef0123456789abcdef01234567"

func newTestTorbox(t *testing.T, h http.HandlerFunc) *Torbox {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Torbox{
		Host:   srv.URL,
		client: request.New(request.WithMaxRetries(0)),
	}
}

// A cache probe has three outcomes, and the third must stay distinguishable from
// the second: "the probe failed" is not "not cached".
func TestIsCachedSeparatesUnknownFromNegative(t *testing.T) {
	cases := []struct {
		name          string
		handler       http.HandlerFunc
		hash          string
		cached, known bool
	}{
		{
			name: "present in cache",
			hash: testHash,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"` + testHash +
					`":{"name":"x","size":123,"hash":"` + testHash + `"}}}`))
			},
			cached: true, known: true,
		},
		{
			name: "answered, hash absent",
			hash: testHash,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
			},
			cached: false, known: true,
		},
		{
			name: "probe failed: must be unknown, not negative",
			hash: testHash,
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			cached: false, known: false,
		},
		{
			name:    "empty hash is unknown, not negative",
			hash:    "",
			handler: func(w http.ResponseWriter, r *http.Request) { t.Fatal("must not be called") },
			cached:  false, known: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tb := newTestTorbox(t, tc.handler)
			cached, known := tb.isCached(tc.hash)
			if cached != tc.cached || known != tc.known {
				t.Fatalf("isCached() = (%v, %v), want (%v, %v)", cached, known, tc.cached, tc.known)
			}
		})
	}
}

// The point of the check is to refuse cheaply *before* createtorrent, and to stay
// out of the way whenever the answer is not conclusive.
func TestSubmitMagnetSkipsCreateTorrentOnlyWhenKnownUncached(t *testing.T) {
	cases := []struct {
		name             string
		checkcached      func(w http.ResponseWriter)
		downloadUncached bool
		wantCreateCalled bool
		wantErr          bool
	}{
		{
			name: "known uncached: refuse without calling createtorrent",
			checkcached: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
			},
			wantCreateCalled: false, wantErr: true,
		},
		{
			name: "probe failed: fall through to createtorrent as before",
			checkcached: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantCreateCalled: true, wantErr: false,
		},
		{
			name: "cached: proceed to createtorrent",
			checkcached: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"success":true,"data":{"` + testHash +
					`":{"name":"x","size":123,"hash":"` + testHash + `"}}}`))
			},
			wantCreateCalled: true, wantErr: false,
		},
		{
			name: "download_uncached: never probe, never refuse",
			checkcached: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusTeapot) // must not be reached
			},
			downloadUncached: true,
			wantCreateCalled: true, wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var createCalled bool
			tb := newTestTorbox(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/torrents/checkcached":
					tc.checkcached(w)
				case "/api/torrents/createtorrent":
					createCalled = true
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"success":true,"data":{"torrent_id":1,"hash":"` +
						testHash + `"}}`))
				default:
					w.WriteHeader(http.StatusInternalServerError)
				}
			})

			_, err := tb.SubmitMagnet(&types.Torrent{
				InfoHash:         testHash,
				Name:             "some.release",
				DownloadUncached: tc.downloadUncached,
				Magnet:           &utils.Magnet{Link: "magnet:?xt=urn:btih:" + testHash},
			})

			if createCalled != tc.wantCreateCalled {
				t.Errorf("createtorrent called = %v, want %v", createCalled, tc.wantCreateCalled)
			}
			if (err != nil) != tc.wantErr {
				t.Errorf("SubmitMagnet() error = %v, want error = %v", err, tc.wantErr)
			}
		})
	}
}
