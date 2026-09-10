package debridlink

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestTorrentResponses(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	config.Get().AllowedExt = []string{"mkv"}
	for _, test := range []struct {
		name      string
		body      string
		files     int
		wantError bool
	}{
		{"accepted file", `{"success":true,"value":[{"id":"torrent","hashString":"hash","status":100,"files":[{"id":"file","name":"movie.mkv","size":1000,"downloadUrl":"https://files.example/movie"}]}]}`, 1, false},
		{"filtered file", `{"success":true,"value":[{"id":"torrent","status":100,"files":[{"id":"file","name":"note.txt","size":1000}]}]}`, 0, false},
		{"no files", `{"success":true,"value":[{"id":"torrent","status":100,"files":[]}]}`, 0, false},
		{"missing value", `{"success":true}`, 0, true},
		{"failed response", `{"success":false,"value":[]}`, 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, test.body) }))
			defer server.Close()
			provider, err := New(config.Debrid{Name: "debridlink", APIKey: "token"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			provider.Host = server.URL
			for _, list := range []bool{false, true} {
				var torrent *types.Torrent
				if list {
					var torrents []*types.Torrent
					torrents, err = provider.getTorrents(1, 100)
					if err == nil && len(torrents) == 1 {
						torrent = torrents[0]
					}
				} else {
					torrent, err = provider.GetTorrent("torrent")
				}
				if test.wantError {
					if err == nil {
						t.Fatalf("list=%v: invalid envelope accepted", list)
					}
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if torrent == nil || torrent.Files == nil || len(torrent.Files) != test.files || torrent.Status != types.TorrentStatusDownloaded {
					t.Fatalf("list=%v: torrent=%#v", list, torrent)
				}
				if test.files > 0 {
					file := torrent.Files["movie.mkv"]
					if file.Id != "file" || file.Link != "https://files.example/movie" || torrent.InfoHash != "hash" {
						t.Fatalf("lost file identity: %#v", torrent)
					}
				}
			}
		})
	}
}
