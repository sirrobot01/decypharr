package premiumize

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
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
		Host:                server.URL,
		client:              request.New(request.WithMaxRetries(0)),
		config:              config.Debrid{Name: "premiumize-primary"},
		validateFileAllowed: func(string, int64) error { return nil },
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

func TestAvailabilityRejectsIncompleteResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","response":[true]}`)
	}))
	defer server.Close()
	pm := &Premiumize{Host: server.URL, client: request.New(request.WithMaxRetries(0))}
	result, err := pm.IsAvailable([]string{"first", "second"})
	if err == nil || len(result) != 0 {
		t.Fatalf("incomplete response = %v, %v", result, err)
	}
}

// Premiumize mints a CDN link once, when the item is added, and that URL starts
// returning 403 about 24h later. The stored file.Link is therefore an identity,
// not a usable URL: fetchDownloadLink must re-mint through item/details rather
// than hand the stored URL back. The account cache keeps this to one call per
// file until a 403 evicts the entry and the link service re-fetches.
func TestFetchDownloadLinkReMintsStoredLink(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	const (
		staleLink = "https://cdn.example.com/dl/1757900000/aaa/Show.S01E01.mkv"
		freshLink = "https://cdn.example.com/dl/1758600000/bbb/Show.S01E01.mkv"
	)

	var detailCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/item/details", func(w http.ResponseWriter, r *http.Request) {
		detailCalls++
		if got := r.URL.Query().Get("id"); got != "file-1" {
			t.Errorf("item/details id = %q, want %q", got, "file-1")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"success","id":"file-1","name":"Show.S01E01.mkv","size":2048,"link":%q}`, freshLink)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	pm := &Premiumize{
		Host:                server.URL,
		client:              request.New(request.WithMaxRetries(0)),
		config:              config.Debrid{Name: "premiumize-primary"},
		validateFileAllowed: func(string, int64) error { return nil },
	}

	file := &types.File{Id: "file-1", Name: "Show.S01E01.mkv", Size: 1024, Link: staleLink}
	dl, err := pm.fetchDownloadLink(context.Background(), &account.Account{Token: "tok"}, "torrent-1", file)
	if err != nil {
		t.Fatalf("fetchDownloadLink() error = %v", err)
	}

	if detailCalls != 1 {
		t.Errorf("item/details called %d times, want 1", detailCalls)
	}
	if dl.DownloadLink != freshLink {
		t.Errorf("DownloadLink = %q, want the re-minted %q", dl.DownloadLink, freshLink)
	}
	// Link is the account cache key (Account.GetDownloadLink looks up
	// file.Link, storeLink stores under dl.Link). Re-minting into it would
	// key every entry under a URL nothing looks up, turning the cache into a
	// permanent miss and an item/details call per read.
	if dl.Link != staleLink {
		t.Errorf("Link = %q, want the stored %q so the cache key stays stable", dl.Link, staleLink)
	}
	if dl.Size != 2048 {
		t.Errorf("Size = %d, want 2048 from item/details", dl.Size)
	}
}

// A file with no Premiumize item id cannot be re-minted; the stored link is all
// there is, and it must still be served rather than erroring.
func TestFetchDownloadLinkWithoutIdUsesStoredLink(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	const staleLink = "https://cdn.example.com/dl/1757900000/aaa/Show.S01E02.mkv"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to %s", r.URL.Path)
	}))
	t.Cleanup(server.Close)

	pm := &Premiumize{
		Host:                server.URL,
		client:              request.New(request.WithMaxRetries(0)),
		config:              config.Debrid{Name: "premiumize-primary"},
		validateFileAllowed: func(string, int64) error { return nil },
	}

	file := &types.File{Name: "Show.S01E02.mkv", Size: 1024, Link: staleLink}
	dl, err := pm.fetchDownloadLink(context.Background(), &account.Account{Token: "tok"}, "torrent-1", file)
	if err != nil {
		t.Fatalf("fetchDownloadLink() error = %v", err)
	}
	if dl.DownloadLink != staleLink || dl.Link != staleLink {
		t.Errorf("got Link=%q DownloadLink=%q, want both %q", dl.Link, dl.DownloadLink, staleLink)
	}
}
