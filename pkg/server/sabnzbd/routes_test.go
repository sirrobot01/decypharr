package sabnzbd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestRouterQueueContracts(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	if _, err := config.Update(func(c *config.Config) error {
		c.UseAuth = true
		c.Auth = &config.Auth{APIToken: "test-token", TokenOnly: true}
		c.DownloadFolder = t.TempDir()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mgr := manager.New()
	t.Cleanup(func() {
		if err := mgr.Stop(); err != nil {
			t.Error(err)
		}
	})
	sab := New(mgr)
	router := sab.Routes()
	for i, tc := range []struct {
		category string
		protocol config.Protocol
		state    storage.TorrentState
	}{
		{"tv", config.ProtocolNZB, storage.EntryStateDownloading},
		{"movies", config.ProtocolNZB, storage.EntryStateDownloading},
		{"tv", config.ProtocolTorrent, storage.EntryStateDownloading},
		{"tv", config.ProtocolNZB, storage.EntryStatePausedUP},
	} {
		entry := &storage.Entry{InfoHash: fmt.Sprintf("entry-%d", i), Name: fmt.Sprintf("Release%d.nzb", i), Category: tc.category, Protocol: tc.protocol, State: tc.state, Size: 4 << 20, Progress: 0.25, SavePath: t.TempDir()}
		if err := mgr.Queue().Add(entry); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, method, key, token string
		wantStatus               int
	}{
		{"query", http.MethodGet, "category", "test-token", 200},
		{"form", http.MethodPost, "category", "test-token", 200},
		{"category alias", http.MethodGet, "cat", "test-token", 200},
		{"form category alias", http.MethodPost, "cat", "test-token", 200},
		{"wrong token", http.MethodGet, "category", "wrong", 401},
		{"missing token", http.MethodGet, "category", "", 401},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := url.Values{"mode": {"queue"}, tc.key: {"tv"}, "ma_password": {tc.token}}
			var req *http.Request
			if tc.method == http.MethodPost {
				req = httptest.NewRequest(tc.method, "/api/", strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req = httptest.NewRequest(tc.method, "/api/?"+values.Encode(), nil)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if tc.wantStatus != 200 {
				return
			}
			var got QueueResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if !got.Status || got.Version != Version || len(got.Queue.Slots) != 1 {
				t.Fatalf("queue = %#v", got)
			}
			slot := got.Queue.Slots[0]
			if slot.NzoId != "entry-0" || slot.Cat != "tv" || slot.Filename != "Release0.nzb" || slot.Mb != "4.00" || slot.MBLeft != "3.00" || slot.Percentage != "25" || slot.Status != StatusDownloading || slot.Labels == nil {
				t.Fatalf("slot = %#v", slot)
			}
		})
	}
	t.Run("unknown mode", func(t *testing.T) {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/?mode=unknown&ma_password=test-token", nil))
		if response.Code != 404 {
			t.Fatalf("status = %d", response.Code)
		}
	})
	t.Run("authenticated Arr survives mode parsing", func(t *testing.T) {
		uncached := true
		mgr.Arr().AddOrUpdate(arr.Arr{Name: "tv", Host: "https://arr.example.test", Token: "arr-token", Source: arr.SourceManual, DownloadUncached: &uncached})
		reached := false
		handler := sab.categoryContext(sab.authContext(sab.modeContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reached = true
			a := getArrFromContext(r.Context())
			if a.Name != "tv" || a.Host != "https://arr.example.test" || a.Source != arr.SourceManual || a.DownloadUncached == nil || !*a.DownloadUncached {
				t.Errorf("Arr = %#v", a)
			}
		}))))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/?mode=queue&category=tv&ma_password=test-token", nil))
		if !reached {
			t.Fatalf("handler rejected request: %d", response.Code)
		}
	})
	for _, tc := range []struct {
		name, value, method string
		wantStatus          int
		wantDeleted         bool
	}{
		{"one", "target", http.MethodGet, 200, true},
		{"form deletion", "target", http.MethodPost, 200, true},
		{"partial", "target,missing", http.MethodGet, 200, true},
		{"all missing", "missing", http.MethodGet, 500, false},
		{"empty", "", http.MethodGet, 400, false},
		{"cleanup failure", "target", http.MethodGet, 500, false},
		{"failed category", "failed", http.MethodGet, 200, true},
	} {
		t.Run("delete/"+tc.name, func(t *testing.T) {
			entries := make([]*storage.Entry, 4)
			for i := range entries {
				category := "delete-tv"
				if i == 1 {
					category = "delete-movies"
				}
				entries[i] = &storage.Entry{InfoHash: fmt.Sprintf("%s-%d", tc.name, i), Name: "Release.nzb", Category: category, Protocol: config.ProtocolNZB, State: storage.EntryStateError, SavePath: t.TempDir(), Magnet: filepath.Join(t.TempDir(), "staged.nzb")}
				if i == 2 {
					entries[i].Protocol = config.ProtocolTorrent
				}
				if i == 3 {
					entries[i].State = storage.EntryStateDownloading
				}
				if tc.name == "cleanup failure" && i == 0 {
					if err := os.Mkdir(entries[i].Magnet, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(entries[i].Magnet, "block-removal"), []byte("keep"), 0600); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(entries[i].Magnet, []byte("staged"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(entries[i].DownloadPath(), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(entries[i].DownloadPath(), "movie.mkv"), []byte("movie"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := mgr.Queue().Add(entries[i]); err != nil {
					t.Fatal(err)
				}
			}
			value := strings.ReplaceAll(tc.value, "target", entries[0].InfoHash)
			values := url.Values{"mode": {"queue"}, "name": {"delete"}, "value": {value}, "category": {"delete-tv"}, "ma_password": {"test-token"}}
			var req *http.Request
			if tc.method == http.MethodPost {
				req = httptest.NewRequest(tc.method, "/api/", strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			} else {
				req = httptest.NewRequest(tc.method, "/api/?"+values.Encode(), nil)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != tc.wantStatus {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var got StatusResponse
			if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status != (tc.wantStatus == 200) {
				t.Fatalf("response = %#v", got)
			}
			if tc.name == "partial" && !strings.Contains(got.Error, "missing") {
				t.Errorf("partial failure is absent: %#v", got)
			}
			for i, entry := range entries {
				deleted := i == 0 && tc.wantDeleted
				_, err := mgr.Queue().GetTorrent(entry.InfoHash)
				if deleted && err == nil || !deleted && err != nil {
					t.Errorf("entry %s, deleted=%v, error=%v", entry.InfoHash, deleted, err)
				}
				for _, path := range []string{entry.Magnet, entry.DownloadPath()} {
					_, err := os.Stat(path)
					if deleted && !errors.Is(err, os.ErrNotExist) || !deleted && err != nil {
						t.Errorf("path %s, deleted=%v, error=%v", path, deleted, err)
					}
				}
			}
		})
	}
}
