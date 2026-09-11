package premiumize

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
)

func TestGetTorrentsAssignsStableUniqueHashesWithoutMagnetSources(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/transfer/list", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"status":"success",
			"transfers":[
				{"id":"transfer-a","name":"Release A","status":"finished","progress":1,"folder_id":"folder-a","file_id":null},
				{"id":"transfer-b","name":"Release B","status":"finished","progress":1,"folder_id":"folder-b","file_id":null}
			]
		}`)
	})
	mux.HandleFunc("GET /api/folder/list", func(w http.ResponseWriter, r *http.Request) {
		folderID := r.URL.Query().Get("id")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{
			"status":"success",
			"folder_id":%q,
			"content":[{"id":%q,"name":%q,"type":"file","size":1024,"link":%q}]
		}`, folderID, "file-"+folderID, folderID+".mkv", "https://example.com/"+folderID)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	pm := &Premiumize{
		Host:          server.URL,
		client:        request.New(request.WithMaxRetries(0)),
		config:        config.Debrid{Name: "premiumize-primary"},
		isFileAllowed: func(string, int64) error { return nil },
	}

	first, err := pm.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("GetTorrents() returned %d torrents, want 2", len(first))
	}
	if first[0].InfoHash == "" || first[1].InfoHash == "" {
		t.Fatalf("GetTorrents() hashes = %q, %q; want non-empty", first[0].InfoHash, first[1].InfoHash)
	}
	if first[0].InfoHash == first[1].InfoHash {
		t.Fatalf("GetTorrents() assigned duplicate hash %q", first[0].InfoHash)
	}
	if len(first[0].InfoHash) != 40 || len(first[1].InfoHash) != 40 {
		t.Fatalf("GetTorrents() hash lengths = %d, %d; want 40", len(first[0].InfoHash), len(first[1].InfoHash))
	}

	second, err := pm.GetTorrents()
	if err != nil {
		t.Fatalf("second GetTorrents() error = %v", err)
	}
	for i := range first {
		if second[i].InfoHash != first[i].InfoHash {
			t.Errorf("GetTorrents() hash changed from %q to %q", first[i].InfoHash, second[i].InfoHash)
		}
	}
}

func TestTransferInfoHashPrefersRealHash(t *testing.T) {
	const infoHash = "8d2b41ef6a4cd8f42c601c396c1caeebe2aed47d"
	pm := &Premiumize{config: config.Debrid{Name: "premiumize-primary"}}
	transfer := premiumizeTransfer{
		ID:  "transfer-a",
		Src: "magnet:?xt=urn:btih:" + infoHash,
	}

	if got := pm.transferInfoHash(transfer, "fallback"); got != infoHash {
		t.Errorf("transferInfoHash() = %q, want %q", got, infoHash)
	}
}
