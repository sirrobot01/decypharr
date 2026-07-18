package alldebrid

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestMain(m *testing.M) {
	configDir, err := os.MkdirTemp("", "decypharr-alldebrid-test-")
	if err != nil {
		panic(err)
	}
	config.SetConfigPath(configDir)

	code := m.Run()
	_ = os.RemoveAll(configDir)
	os.Exit(code)
}

func newTestAllDebrid(t *testing.T, handler http.HandlerFunc) *AllDebrid {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	debridConfig := config.Debrid{
		Name:            "test-alldebrid",
		APIKey:          "main-token",
		DownloadAPIKeys: []string{"download-token"},
	}
	testLogger := zerolog.Nop()
	clientOptions := []request.ClientOption{
		request.WithMaxRetries(0),
		request.WithTimeout(2 * time.Second),
	}

	return &AllDebrid{
		Host:                  server.URL,
		APIKey:                debridConfig.APIKey,
		accountsManager:       account.NewManager(debridConfig, nil, testLogger),
		autoExpiresLinksAfter: time.Hour,
		client:                request.New(clientOptions...),
		repairClient:          request.New(clientOptions...),
		logger:                testLogger,
		config:                debridConfig,
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestAllDebridEndpointVersions(t *testing.T) {
	if allDebridAPIHost != "https://api.alldebrid.com" {
		t.Fatalf("API host = %q, want unversioned HTTPS host", allDebridAPIHost)
	}

	tests := []struct {
		name     string
		endpoint string
		want     string
	}{
		{name: "magnet upload", endpoint: allDebridMagnetUploadEndpoint, want: "/v4/magnet/upload"},
		{name: "torrent upload", endpoint: allDebridMagnetUploadFileEndpoint, want: "/v4/magnet/upload/file"},
		{name: "magnet status", endpoint: allDebridMagnetStatusEndpoint, want: "/v4.1/magnet/status"},
		{name: "magnet delete", endpoint: allDebridMagnetDeleteEndpoint, want: "/v4/magnet/delete"},
		{name: "link unlock", endpoint: allDebridLinkUnlockEndpoint, want: "/v4/link/unlock"},
		{name: "link infos", endpoint: allDebridLinkInfosEndpoint, want: "/v4/link/infos"},
		{name: "user", endpoint: allDebridUserEndpoint, want: "/v4/user"},
		{name: "user link delete", endpoint: allDebridUserLinksDeleteEndpoint, want: "/v4/user/links/delete"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.endpoint != test.want {
				t.Fatalf("endpoint = %q, want %q", test.endpoint, test.want)
			}
		})
	}
}

func TestGetTorrentsUsesV41StatusAndDecodesArray(t *testing.T) {
	ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != allDebridMagnetStatusEndpoint {
			t.Errorf("path = %q, want %q", r.URL.Path, allDebridMagnetStatusEndpoint)
		}
		if got := r.URL.Query().Get("status"); got != "ready" {
			t.Errorf("status query = %q, want ready", got)
		}
		writeTestJSON(t, w, `{
			"status": "success",
			"data": {
				"magnets": [
					{
						"id": 41,
						"filename": "ready.mkv",
						"hash": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
						"size": 1000,
						"status": "Ready",
						"statusCode": 4,
						"completionDate": 1700000000
					},
					{
						"id": 42,
						"filename": "active.mkv",
						"hash": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
						"size": 2000,
						"status": "Downloading",
						"statusCode": 1,
						"downloaded": 500,
						"downloadSpeed": 123
					}
				]
			}
		}`)
	})

	torrents, err := ad.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(torrents) != 2 {
		t.Fatalf("len(torrents) = %d, want 2", len(torrents))
	}
	if torrents[0].Id != "41" || torrents[0].Status != types.TorrentStatusDownloaded {
		t.Errorf("first torrent = %#v, want ID 41 downloaded", torrents[0])
	}
	if torrents[1].Id != "42" || torrents[1].Status != types.TorrentStatusDownloading {
		t.Errorf("second torrent = %#v, want ID 42 downloading", torrents[1])
	}
}

func TestGetTorrentUsesV41ArrayAndFlattensFiles(t *testing.T) {
	ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != allDebridMagnetStatusEndpoint {
			t.Errorf("path = %q, want %q", r.URL.Path, allDebridMagnetStatusEndpoint)
		}
		if got := r.URL.Query().Get("id"); got != "123456" {
			t.Errorf("id query = %q, want 123456", got)
		}
		writeTestJSON(t, w, `{
			"status": "success",
			"data": {
				"magnets": [
					{
						"id": 123456,
						"filename": "example-release",
						"hash": "1234567890abcdef1234567890abcdef12345678",
						"size": 1000,
						"status": "Ready",
						"statusCode": 4,
						"seeders": 9,
						"completionDate": 1700000000,
						"files": [
							{
								"n": "movie.mkv",
								"s": 900,
								"l": "https://alldebrid.com/f/movie"
							},
							{
								"n": "extras",
								"e": [
									{
										"n": "feature.mp4",
										"s": 100,
										"l": "https://alldebrid.com/f/feature"
									}
								]
							}
						]
					}
				]
			}
		}`)
	})

	torrent, err := ad.GetTorrent("123456")
	if err != nil {
		t.Fatalf("GetTorrent() error = %v", err)
	}
	if torrent.Id != "123456" {
		t.Errorf("ID = %q, want 123456", torrent.Id)
	}
	if torrent.Status != types.TorrentStatusDownloaded || torrent.Progress != 100 {
		t.Errorf("status/progress = %q/%v, want downloaded/100", torrent.Status, torrent.Progress)
	}
	if torrent.InfoHash != "1234567890abcdef1234567890abcdef12345678" {
		t.Errorf("hash = %q", torrent.InfoHash)
	}
	if len(torrent.Files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(torrent.Files))
	}
	nested, ok := torrent.Files["feature.mp4"]
	if !ok {
		t.Fatalf("nested file feature.mp4 missing: %#v", torrent.Files)
	}
	if nested.Path != filepath.Join("extras", "feature.mp4") {
		t.Errorf("nested path = %q, want %q", nested.Path, filepath.Join("extras", "feature.mp4"))
	}
	if nested.Link != "https://alldebrid.com/f/feature" {
		t.Errorf("nested link = %q", nested.Link)
	}
}

func TestUpdateTorrentUsesV41Array(t *testing.T) {
	ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != allDebridMagnetStatusEndpoint {
			t.Errorf("path = %q, want %q", r.URL.Path, allDebridMagnetStatusEndpoint)
		}
		if got := r.URL.Query().Get("id"); got != "77" {
			t.Errorf("id query = %q, want 77", got)
		}
		writeTestJSON(t, w, `{
			"status": "success",
			"data": {
				"magnets": [
					{
						"id": 77,
						"filename": "active-release",
						"hash": "7777777777777777777777777777777777777777",
						"size": 1000,
						"status": "Downloading",
						"statusCode": 1,
						"downloaded": 250,
						"downloadSpeed": 12345,
						"seeders": 7
					}
				]
			}
		}`)
	})

	torrent := &types.Torrent{Id: "77"}
	if err := ad.UpdateTorrent(torrent); err != nil {
		t.Fatalf("UpdateTorrent() error = %v", err)
	}
	if torrent.Status != types.TorrentStatusDownloading {
		t.Errorf("status = %q, want downloading", torrent.Status)
	}
	if torrent.Progress != 25 {
		t.Errorf("progress = %v, want 25", torrent.Progress)
	}
	if torrent.Speed != 12345 || torrent.Seeders != 7 {
		t.Errorf("speed/seeders = %d/%d, want 12345/7", torrent.Speed, torrent.Seeders)
	}
}

func TestGetTorrentValidatesExactlyOneMatchingID(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantErrSub string
	}{
		{
			name:       "empty",
			response:   `{"status":"success","data":{"magnets":[]}}`,
			wantErrSub: "expected exactly one magnet, got 0",
		},
		{
			name:       "multiple",
			response:   `{"status":"success","data":{"magnets":[{"id":42},{"id":43}]}}`,
			wantErrSub: "expected exactly one magnet, got 2",
		},
		{
			name:       "mismatched ID",
			response:   `{"status":"success","data":{"magnets":[{"id":43}]}}`,
			wantErrSub: "returned magnet ID 43",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
				writeTestJSON(t, w, test.response)
			})

			_, err := ad.GetTorrent("42")
			if err == nil {
				t.Fatal("GetTorrent() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), test.wantErrSub) {
				t.Fatalf("error = %q, want substring %q", err, test.wantErrSub)
			}
		})
	}
}

func TestHTTP200TopLevelAPIErrors(t *testing.T) {
	t.Run("status discontinued", func(t *testing.T) {
		ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
			writeTestJSON(t, w, `{
				"status": "error",
				"error": {
					"code": "DISCONTINUED",
					"message": "This API endpoint has been discontinued"
				}
			}`)
		})

		_, err := ad.GetTorrents()
		if err == nil {
			t.Fatal("GetTorrents() error = nil")
		}
		if !strings.Contains(err.Error(), "DISCONTINUED") ||
			!strings.Contains(err.Error(), "This API endpoint has been discontinued") {
			t.Fatalf("error = %q, want provider code and message", err)
		}
	})

	t.Run("upload rejection", func(t *testing.T) {
		ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != allDebridMagnetUploadEndpoint {
				t.Errorf("path = %q, want %q", r.URL.Path, allDebridMagnetUploadEndpoint)
			}
			writeTestJSON(t, w, `{
				"status": "error",
				"error": {
					"code": "MAGNET_TOO_MANY_ACTIVE",
					"message": "Already have maximum allowed active magnets"
				}
			}`)
		})

		torrent := &types.Torrent{
			Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		}
		_, err := ad.SubmitMagnet(torrent)
		if err == nil {
			t.Fatal("SubmitMagnet() error = nil")
		}
		if !strings.Contains(err.Error(), "MAGNET_TOO_MANY_ACTIVE") ||
			!strings.Contains(err.Error(), "Already have maximum allowed active magnets") {
			t.Fatalf("error = %q, want provider code and message", err)
		}
	})
}

func TestNonStatusOperationsUseV4Routes(t *testing.T) {
	var mu sync.Mutex
	seen := make(map[string]int)

	ad := newTestAllDebrid(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path]++
		mu.Unlock()

		switch r.URL.Path {
		case allDebridMagnetUploadEndpoint:
			if r.Method != http.MethodGet {
				t.Errorf("magnet upload method = %s, want GET", r.Method)
			}
			if got := r.URL.Query().Get("magnets[]"); got == "" {
				t.Error("magnet upload query is empty")
			}
			writeTestJSON(t, w, `{"status":"success","data":{"magnets":[{"id":41}]}}`)
		case allDebridMagnetUploadFileEndpoint:
			if r.Method != http.MethodPost {
				t.Errorf("torrent upload method = %s, want POST", r.Method)
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data;") {
				t.Errorf("torrent upload content type = %q", r.Header.Get("Content-Type"))
			}
			writeTestJSON(t, w, `{"status":"success","data":{"files":[{"id":42}]}}`)
		case allDebridMagnetDeleteEndpoint:
			if got := r.URL.Query().Get("id"); got != "41" {
				t.Errorf("delete id = %q, want 41", got)
			}
			writeTestJSON(t, w, `{"status":"success","data":{"message":"deleted"}}`)
		case allDebridUserEndpoint:
			writeTestJSON(t, w, `{
				"status":"success",
				"data":{"user":{"username":"demo","email":"demo@example.com","isPremium":true,"premiumUntil":2000000000}}
			}`)
		case allDebridLinkInfosEndpoint:
			if r.Method != http.MethodPost {
				t.Errorf("link infos method = %s, want POST", r.Method)
			}
			writeTestJSON(t, w, `{"status":"success","data":{"infos":[{}]}}`)
		case allDebridLinkUnlockEndpoint:
			if got := r.URL.Query().Get("link"); got == "" {
				t.Error("link unlock query is empty")
			}
			writeTestJSON(t, w, `{
				"status":"success",
				"data":{"link":"https://download.example/movie.mkv","id":"download-id"}
			}`)
		case allDebridUserLinksDeleteEndpoint:
			if got := r.URL.Query().Get("links"); got == "" {
				t.Error("user link delete query is empty")
			}
			writeTestJSON(t, w, `{"status":"success","data":{"message":"deleted"}}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
			http.Error(w, "unexpected path", http.StatusNotFound)
		}
	})

	magnetTorrent := &types.Torrent{
		Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}
	if _, err := ad.SubmitMagnet(magnetTorrent); err != nil {
		t.Fatalf("SubmitMagnet(link) error = %v", err)
	}
	fileTorrent := &types.Torrent{Magnet: &utils.Magnet{File: []byte("torrent-data")}}
	if _, err := ad.SubmitMagnet(fileTorrent); err != nil {
		t.Fatalf("SubmitMagnet(file) error = %v", err)
	}
	if err := ad.DeleteTorrent("41"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
	if _, err := ad.GetProfile(); err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if err := ad.CheckFile(context.Background(), "", "https://alldebrid.com/f/movie"); err != nil {
		t.Fatalf("CheckFile() error = %v", err)
	}

	currentAccount := ad.accountsManager.Current()
	if currentAccount == nil {
		t.Fatal("test download account is nil")
	}
	file := &types.File{
		Name: "movie.mkv",
		Size: 100,
		Link: "https://alldebrid.com/f/movie",
	}
	if _, err := ad.fetchDownloadLink(currentAccount, "", file); err != nil {
		t.Fatalf("fetchDownloadLink() error = %v", err)
	}
	if err := ad.deleteLink(currentAccount, types.DownloadLink{Link: file.Link}); err != nil {
		t.Fatalf("deleteLink() error = %v", err)
	}

	expected := []string{
		allDebridMagnetUploadEndpoint,
		allDebridMagnetUploadFileEndpoint,
		allDebridMagnetDeleteEndpoint,
		allDebridUserEndpoint,
		allDebridLinkInfosEndpoint,
		allDebridLinkUnlockEndpoint,
		allDebridUserLinksDeleteEndpoint,
	}
	mu.Lock()
	defer mu.Unlock()
	for _, endpoint := range expected {
		if seen[endpoint] != 1 {
			t.Errorf("requests to %s = %d, want 1", endpoint, seen[endpoint])
		}
		if strings.HasPrefix(endpoint, "/v4.1/") {
			t.Errorf("non-status endpoint unexpectedly uses v4.1: %s", endpoint)
		}
	}
}

func TestGetAlldebridStatus(t *testing.T) {
	tests := []struct {
		code int
		want types.TorrentStatus
	}{
		{code: -1, want: types.TorrentStatusError},
		{code: 0, want: types.TorrentStatusDownloading},
		{code: 1, want: types.TorrentStatusDownloading},
		{code: 2, want: types.TorrentStatusDownloading},
		{code: 3, want: types.TorrentStatusDownloading},
		{code: 4, want: types.TorrentStatusDownloaded},
		{code: 5, want: types.TorrentStatusError},
		{code: 6, want: types.TorrentStatusError},
		{code: 7, want: types.TorrentStatusError},
		{code: 8, want: types.TorrentStatusError},
		{code: 9, want: types.TorrentStatusError},
		{code: 10, want: types.TorrentStatusError},
		{code: 11, want: types.TorrentStatusError},
		{code: 12, want: types.TorrentStatusError},
		{code: 13, want: types.TorrentStatusError},
		{code: 14, want: types.TorrentStatusError},
		{code: 15, want: types.TorrentStatusError},
		{code: 16, want: types.TorrentStatusError},
	}

	for _, test := range tests {
		if got := getAlldebridStatus(test.code); got != test.want {
			t.Errorf("getAlldebridStatus(%d) = %q, want %q", test.code, got, test.want)
		}
	}
}
