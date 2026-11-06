package debridlink

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func testProvider(t *testing.T, handler http.HandlerFunc) *DebridLink {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	config.Get().AllowedExt = []string{"mkv"}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	provider, err := New(config.Debrid{Name: "debridlink", APIKey: "token"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	provider.Host = server.URL
	return provider
}

func TestFloodRefusalStopsTheKeyWithoutRetry(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusBadRequest, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var calls atomic.Int64
			provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				_, _ = fmt.Fprint(w, `{"success":false,"error":"floodDetected"}`)
			})

			_, err := provider.GetTorrents()
			throttled, ok := errors.AsType[*request.ThrottledError](err)
			if !ok {
				t.Fatalf("GetTorrents() error = %v, want ThrottledError", err)
			}
			if throttled.RetryAfter != floodPause {
				t.Fatalf("RetryAfter = %s", throttled.RetryAfter)
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, a flood refusal must not be retried", calls.Load())
			}

			if err := provider.UpdateTorrent(&types.Torrent{Id: "x"}); err == nil {
				t.Fatal("UpdateTorrent() error = nil during a flood pause")
			}
			if calls.Load() != 1 {
				t.Fatalf("calls = %d, the pause must keep requests off the network", calls.Load())
			}
			if remaining := provider.throttle.Remaining(); remaining < 59*time.Minute {
				t.Fatalf("Remaining() = %s", remaining)
			}
		})
	}
}

func TestErrorCodesMapToTypedErrors(t *testing.T) {
	for code, want := range map[string]error{
		"badId":            customerror.TorrentNotFoundError,
		"maxTorrent":       customerror.TooManyActiveDownloadsError,
		"maxTransfer":      customerror.TooManyActiveDownloadsError,
		"torrentTooBig":    customerror.TorrentFailedError,
		"notAddTorrent":    customerror.TorrentFailedError,
		"fileNotAvailable": customerror.TorrentBlockedError,
		"maxData":          customerror.TrafficExceededError,
	} {
		t.Run(code, func(t *testing.T) {
			provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"success":false,"error":%q}`, code)
			})
			_, err := provider.SubmitMagnet(&types.Torrent{Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}})
			if !errors.Is(err, want) {
				t.Fatalf("SubmitMagnet() error = %v, want %v", err, want)
			}
		})
	}
}

func TestSeedingTorrentsAreComplete(t *testing.T) {
	const body = `{"success":true,"value":[{"id":"t","hashString":"hash","status":%d,"downloadPercent":%d,"files":[{"id":"f","name":"a.mkv","size":2000,"downloadUrl":"https://files.example/a"}]}]}`
	for _, test := range []struct {
		status, percent int
		want            types.TorrentStatus
		listed          int
	}{
		{4, 50, types.TorrentStatusDownloading, 0},
		{8, 100, types.TorrentStatusDownloaded, 1},
		{100, 100, types.TorrentStatusDownloaded, 1},
	} {
		t.Run(fmt.Sprint(test.status), func(t *testing.T) {
			provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, body, test.status, test.percent)
			})
			torrent, err := provider.CheckStatus(&types.Torrent{Id: "t", DownloadUncached: true})
			if err != nil {
				t.Fatalf("CheckStatus() error = %v", err)
			}
			if torrent.Status != test.want {
				t.Fatalf("status = %s, want %s", torrent.Status, test.want)
			}
			torrents, _, err := provider.getTorrents(0, 100)
			if err != nil {
				t.Fatalf("getTorrents() error = %v", err)
			}
			if len(torrents) != test.listed {
				t.Fatalf("listed = %d, want %d", len(torrents), test.listed)
			}
		})
	}
}

func TestGetTorrentUsesTheDocumentedEndpoint(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/seedbox/list" || r.URL.Query().Get("ids") != "abc" {
			t.Errorf("request = %s", r.URL)
		}
		_, _ = fmt.Fprint(w, `{"success":true,"value":[{"id":"abc","status":4,"downloadPercent":10}]}`)
	})
	torrent, err := provider.GetTorrent("abc")
	if err != nil {
		t.Fatalf("GetTorrent() error = %v", err)
	}
	if torrent.Status != types.TorrentStatusDownloading {
		t.Fatalf("status = %s, an unfinished torrent must not read as downloaded", torrent.Status)
	}
}

func TestUpdateTorrentAcceptsANilFileMap(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"success":true,"value":[{"id":"t","status":100,"files":[{"id":"f","name":"a.mkv","size":2000,"downloadUrl":"https://files.example/a"}]}]}`)
	})
	torrent := &types.Torrent{Id: "t"}
	if err := provider.UpdateTorrent(torrent); err != nil {
		t.Fatalf("UpdateTorrent() error = %v", err)
	}
	if link := torrent.Files["a.mkv"].DownloadLink; link.Size != 2000 {
		t.Fatalf("link size = %d, want the file size", link.Size)
	}
}

func TestSubmitUploadsTorrentFiles(t *testing.T) {
	var file []byte
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		upload, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile() error = %v", err)
			return
		}
		file, _ = io.ReadAll(upload)
		_, _ = fmt.Fprint(w, `{"success":true,"value":{"id":"t","hashString":"hash","status":4}}`)
	})
	torrent := &types.Torrent{Magnet: &utils.Magnet{File: []byte("d8:announce...")}}
	if _, err := provider.SubmitMagnet(torrent); err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if string(file) != "d8:announce..." {
		t.Fatalf("uploaded %q", file)
	}
}

func TestAvailabilityIsUnsupported(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
	})
	if _, err := provider.IsAvailable([]string{"abc"}); !errors.Is(err, types.ErrAvailabilityUnsupported) {
		t.Fatalf("IsAvailable() error = %v", err)
	}
}

func TestProfileReadsPremiumLeftAsSeconds(t *testing.T) {
	provider := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"success":true,"value":{"username":"u","premiumLeft":86400}}`)
	})
	profile, err := provider.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if left := time.Until(profile.Expiration); left < 23*time.Hour || left > 25*time.Hour {
		t.Fatalf("expiration is %s away, want one day", left)
	}
}
