package realdebrid

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestCheckStatusSelectsAllowedFilesAndMapsLinks(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	if _, err := config.Update(func(c *config.Config) error { c.AllowedExt = []string{"mkv"}; return nil }); err != nil {
		t.Fatal(err)
	}
	var gets, selections atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /torrents/info/torrent-id":
			status := "waiting_files_selection"
			selected := 0
			if gets.Add(1) == 2 {
				status = "downloaded"
				selected = 1
			}
			if gets.Load() > 2 {
				http.Error(w, "unexpected poll", 500)
				return
			}
			fmt.Fprintf(w, `{"id":"torrent-id","filename":"Release","original_filename":"Original","hash":"hash","bytes":3000,"progress":100,"status":%q,"files":[{"id":7,"path":"/Release/first.mkv","bytes":1000,"selected":%d},{"id":8,"path":"/Release/readme.txt","bytes":10,"selected":0},{"id":9,"path":"/Release/second.mkv","bytes":2000,"selected":%d}],"links":["https://example.test/first","https://example.test/second"]}`, status, selected, selected)
		case "POST /torrents/selectFiles/torrent-id":
			selections.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			ids := strings.Split(r.Form.Get("files"), ",")
			slices.Sort(ids)
			if !slices.Equal(ids, []string{"7", "9"}) {
				t.Errorf("selected IDs = %v", ids)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := &RealDebrid{Host: server.URL, client: request.New(request.WithMaxRetries(0), request.WithHeaders(map[string]string{"Authorization": "Bearer test-key"})), config: config.Debrid{Name: "realdebrid"}, logger: zerolog.Nop()}
	torrent, err := provider.CheckStatus(&types.Torrent{Id: "torrent-id"})
	if err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 2 || selections.Load() != 1 {
		t.Fatalf("GETs = %d, selections = %d", gets.Load(), selections.Load())
	}
	if torrent.Status != types.TorrentStatusDownloaded || torrent.InfoHash != "hash" || torrent.Debrid != "realdebrid" || torrent.Name != "Release" || torrent.OriginalFilename != "Original" || torrent.Bytes != 3000 || len(torrent.Files) != 2 {
		t.Fatalf("torrent = %#v", torrent)
	}
	for _, want := range []struct {
		name, id, link string
		size           int64
	}{{"first.mkv", "7", "https://example.test/first", 1000}, {"second.mkv", "9", "https://example.test/second", 2000}} {
		file := torrent.Files[want.name]
		if file.Id != want.id || file.Name != want.name || file.Link != want.link || file.Size != want.size || file.TorrentId != "torrent-id" {
			t.Errorf("file = %#v, want %#v", file, want)
		}
	}
}

func TestCheckStatusFailureAndUncachedContracts(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	if _, err := config.Update(func(c *config.Config) error { c.AllowedExt = []string{"mkv"}; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, state   string
		selectStatus  int
		allowUncached bool
		wantStatus    types.TorrentStatus
		wantErr       error
		wantText      string
	}{
		{name: "selection limit", state: "waiting_files_selection", selectStatus: 509, wantErr: customerror.TooManyActiveDownloadsError},
		{name: "selection rejected", state: "waiting_files_selection", selectStatus: 400, wantStatus: types.TorrentStatusDownloading, wantText: "Status: 400"},
		{name: "uncached rejected", state: "downloading", wantStatus: types.TorrentStatusDownloading, wantErr: customerror.TorrentNotCachedError},
		{name: "uncached allowed", state: "queued", allowUncached: true, wantStatus: types.TorrentStatusDownloading},
		{name: "magnet error", state: "magnet_error", wantStatus: types.TorrentStatusError, wantText: "magnet_error"},
		{name: "virus", state: "virus", wantStatus: types.TorrentStatusError, wantText: "virus"},
		{name: "dead", state: "dead", wantStatus: types.TorrentStatusError, wantText: "dead"},
		{name: "unknown", state: "future_state", wantStatus: types.TorrentStatusError, wantText: "future_state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var gets, selects atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method + " " + r.URL.Path {
				case "GET /torrents/info/id":
					if gets.Add(1) > 1 {
						http.Error(w, "unexpected poll", 500)
						return
					}
					fmt.Fprintf(w, `{"status":%q,"filename":"movie","files":[{"id":1,"path":"/movie.mkv","bytes":1000}]}`, tc.state)
				case "POST /torrents/selectFiles/id":
					selects.Add(1)
					if r.FormValue("files") != "1" {
						t.Errorf("files = %q", r.FormValue("files"))
					}
					w.WriteHeader(tc.selectStatus)
				default:
					t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			provider := &RealDebrid{Host: server.URL, client: request.New(request.WithMaxRetries(0)), logger: zerolog.Nop()}
			result, err := provider.CheckStatus(&types.Torrent{Id: "id", DownloadUncached: tc.allowUncached})
			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
			case tc.wantText != "":
				if err == nil || !strings.Contains(err.Error(), tc.wantText) {
					t.Fatalf("error = %v, want %q", err, tc.wantText)
				}
			case err != nil:
				t.Fatal(err)
			}
			if tc.wantStatus != "" && (result == nil || result.Status != tc.wantStatus) {
				t.Fatalf("result = %#v, want status %s", result, tc.wantStatus)
			}
			wantSelects := int32(0)
			if tc.selectStatus != 0 {
				wantSelects = 1
			}
			if gets.Load() != 1 || selects.Load() != wantSelects {
				t.Fatalf("GETs = %d, selections = %d", gets.Load(), selects.Load())
			}
		})
	}
}
