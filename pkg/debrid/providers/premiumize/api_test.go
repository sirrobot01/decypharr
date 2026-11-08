package premiumize

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func testPremiumize(t *testing.T, handler http.Handler) *Premiumize {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	throttle := new(request.Throttle)
	return &Premiumize{
		Host:                server.URL,
		client:              request.New(request.WithMaxRetries(0), request.WithThrottle(throttle)),
		config:              config.Debrid{Name: "premiumize"},
		logger:              zerolog.Nop(),
		throttle:            throttle,
		validateFileAllowed: func(string, int64) error { return nil },
		accountsManager:     account.NewManager(config.Debrid{Name: "premiumize", DownloadAPIKeys: []string{"token"}}, nil, zerolog.Nop()),
	}
}

func errorBody(code string) string {
	return fmt.Sprintf(`{"status":"error","message":"refused","code":%q}`, code)
}

func TestRateLimitEnvelopeIsThrottled(t *testing.T) {
	pm := testPremiumize(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, errorBody("rate_limit_reached"))
	}))

	_, err := pm.GetTorrents()
	if _, ok := errors.AsType[*request.ThrottledError](err); !ok {
		t.Fatalf("GetTorrents() error = %v, want ThrottledError", err)
	}
	if pm.throttle.Remaining() != 0 {
		t.Fatal("Premiumize documents no wait, so the key must not pause")
	}
}

func TestErrorEnvelopeReachesCallersThatWantNoBody(t *testing.T) {
	pm := testPremiumize(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, errorBody("transient_error"))
	}))
	if err := pm.DeleteTorrent("transfer"); !hasCode(err, "transient_error") {
		t.Fatalf("DeleteTorrent() error = %v, a failed delete must not read as success", err)
	}
}

func TestSubmitErrors(t *testing.T) {
	for code, want := range map[string]error{
		"invalid_request":       customerror.TorrentFailedError,
		"service_unsupported":   customerror.TorrentFailedError,
		"account_limit_reached": customerror.TrafficExceededError,
	} {
		t.Run(code, func(t *testing.T) {
			pm := testPremiumize(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, errorBody(code))
			}))
			_, err := pm.SubmitMagnet(&types.Torrent{Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}})
			if !errors.Is(err, want) {
				t.Fatalf("SubmitMagnet() error = %v, want %v", err, want)
			}
		})
	}
	t.Run("transient stays transient", func(t *testing.T) {
		pm := testPremiumize(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, errorBody("transient_error"))
		}))
		_, err := pm.SubmitMagnet(&types.Torrent{Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}})
		if err == nil || customerror.IsTerminalTorrentError(err) {
			t.Fatalf("SubmitMagnet() error = %v", err)
		}
	})
}

func TestMissingItemIsUnavailable(t *testing.T) {
	pm := testPremiumize(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, errorBody("not_found"))
	}))
	if err := pm.CheckFile(t.Context(), "hash", linkScheme+"item"); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("CheckFile() error = %v", err)
	}
	if _, err := pm.GetDownloadLink(t.Context(), "id", &types.File{Id: "item", Link: linkScheme + "item"}); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("GetDownloadLink() error = %v", err)
	}
}

func TestSyncSpendsFolderCallsOnlyOnNewTransfers(t *testing.T) {
	var folders atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/transfer/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","transfers":[{"id":"a","name":"A","status":"finished","progress":1,"folder_id":"folder-a"}]}`)
	})
	mux.HandleFunc("GET /api/folder/list", func(w http.ResponseWriter, r *http.Request) {
		folders.Add(1)
		_, _ = fmt.Fprint(w, `{"status":"success","content":[{"id":"item","name":"a.mkv","type":"file","size":10,"link":"https://cdn.example/expiring"}]}`)
	})
	pm := testPremiumize(t, mux)

	for range 3 {
		torrents, err := pm.GetTorrents()
		if err != nil {
			t.Fatalf("GetTorrents() error = %v", err)
		}
		file, ok := torrents[0].Files["a.mkv"]
		if !ok || file.Link != linkScheme+"item" || torrents[0].Status != types.TorrentStatusDownloaded {
			t.Fatalf("torrent = %+v", torrents[0])
		}
		// A caller that edits its copy must not change the kept list.
		delete(torrents[0].Files, "a.mkv")
	}
	if folders.Load() != 1 {
		t.Fatalf("folder calls = %d, want 1", folders.Load())
	}
}

func TestIncompleteTransfersAreNotKept(t *testing.T) {
	var folders atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/transfer/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","transfers":[{"id":"a","name":"A","status":"finished","progress":1,"folder_id":"folder-a"}]}`)
	})
	mux.HandleFunc("GET /api/folder/list", func(w http.ResponseWriter, r *http.Request) {
		if folders.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `{"status":"success","content":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"status":"success","content":[{"id":"item","name":"a.mkv","type":"file","size":10,"link":"https://cdn.example/a"}]}`)
	})
	pm := testPremiumize(t, mux)

	for range 2 {
		if _, err := pm.GetTorrents(); err != nil {
			t.Fatalf("GetTorrents() error = %v", err)
		}
	}
	torrents, err := pm.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(torrents[0].Files) != 1 || folders.Load() != 2 {
		t.Fatalf("files = %d after %d folder calls", len(torrents[0].Files), folders.Load())
	}
}
