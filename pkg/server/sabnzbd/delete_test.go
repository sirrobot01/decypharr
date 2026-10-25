package sabnzbd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestRouterDeleteRespectsDelFiles(t *testing.T) {
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
	router := New(mgr).Routes()

	for _, mode := range []string{"queue", "history"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, delFiles := range []string{"", "0", "1"} {
				for _, failed := range []bool{false, true} {
					name := fmt.Sprintf("%s/%s/del_files=%s/failed=%t", mode, method, delFiles, failed)
					t.Run(name, func(t *testing.T) {
						entries := make([]*storage.Entry, 5)
						for i := range entries {
							entry := &storage.Entry{
								InfoHash: fmt.Sprintf("%s-%d", name, i),
								Name:     "Release.nzb",
								Category: name,
								Protocol: config.ProtocolNZB,
								State:    storage.EntryStateError,
								SavePath: t.TempDir(),
								Magnet:   filepath.Join(t.TempDir(), "staged.nzb"),
							}
							switch i {
							case 2:
								entry.Category = "other-category"
							case 3:
								entry.Protocol = config.ProtocolTorrent
							case 4:
								entry.State = storage.EntryStateDownloading
							}
							if err := os.MkdirAll(entry.DownloadPath(), 0700); err != nil {
								t.Fatal(err)
							}
							for _, path := range []string{entry.Magnet, filepath.Join(entry.DownloadPath(), "movie.mkv")} {
								if err := os.WriteFile(path, []byte("keep this content"), 0600); err != nil {
									t.Fatal(err)
								}
							}
							if err := mgr.Queue().Add(entry); err != nil {
								t.Fatal(err)
							}
							entries[i] = entry
						}
						value := entries[0].InfoHash + ", " + entries[1].InfoHash
						if failed {
							value = "failed"
						}
						values := url.Values{
							"mode": {mode}, "name": {"delete"}, "value": {value},
							"category": {name}, "ma_password": {"test-token"},
						}
						if delFiles != "" {
							values.Set("del_files", delFiles)
						}
						var req *http.Request
						if method == http.MethodPost {
							req = httptest.NewRequest(method, "/api/", strings.NewReader(values.Encode()))
							req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
						} else {
							req = httptest.NewRequest(method, "/api/?"+values.Encode(), nil)
						}
						response := httptest.NewRecorder()
						router.ServeHTTP(response, req)
						var got StatusResponse
						if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if response.Code != http.StatusOK || !got.Status {
							t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
						}
						for i, entry := range entries {
							removed := i < 2
							_, err := mgr.Queue().GetTorrent(entry.InfoHash)
							if removed && err == nil || !removed && err != nil {
								t.Errorf("entry %s: removed=%t, error=%v", entry.InfoHash, removed, err)
							}
							for _, path := range []string{entry.Magnet, filepath.Join(entry.DownloadPath(), "movie.mkv")} {
								content, err := os.ReadFile(path)
								if removed && delFiles == "1" {
									if !os.IsNotExist(err) {
										t.Errorf("file %s should be removed, error=%v", path, err)
									}
								} else if err != nil || string(content) != "keep this content" {
									t.Errorf("file %s should be preserved, content=%q, error=%v", path, content, err)
								}
							}
						}
					})
				}
			}
		}
	}
}
