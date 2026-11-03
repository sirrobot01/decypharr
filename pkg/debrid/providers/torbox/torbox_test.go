package torbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestSubmissionRequestsUseDedicatedClient(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	var (
		mu    sync.Mutex
		lanes []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		lanes = append(lanes, r.Header.Get("X-Lane"))
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/torrents/createtorrent":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"torrent_id":17,"hash":"ABC"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/torrents/mylist":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"id":17,"name":"Release.mkv","size":100,"progress":1,"download_state":"completed","download_finished":true,"created_at":"2026-01-02T03:04:05Z","hash":"ABC","files":[{"id":1,"name":"Release.mkv","absolute_path":"Release.mkv","size":100}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	tb.client = request.New(
		request.WithHeaders(map[string]string{"X-Lane": "main"}),
		request.WithMaxRetries(0),
	)
	tb.submitClient = request.New(
		request.WithHeaders(map[string]string{"X-Lane": "submit"}),
		request.WithMaxRetries(0),
	)

	torrent := &types.Torrent{
		Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:ABC"},
	}
	added, err := tb.SubmitMagnet(torrent)
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if _, err := tb.CheckStatus(added); err != nil {
		t.Fatalf("CheckStatus() error = %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(lanes, []string{"submit", "submit"}) {
		t.Fatalf("request lanes = %v, want dedicated submission lane", lanes)
	}
}

func TestGetTorrentsBypassesTorboxCache(t *testing.T) {
	config.SetConfigPath(t.TempDir())

	var (
		mu      sync.Mutex
		offsets []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("bypass_cache"); got != "true" {
			t.Errorf("bypass_cache = %q, want true", got)
		}

		offset := r.URL.Query().Get("offset")
		mu.Lock()
		offsets = append(offsets, offset)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		if offset == "0" {
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":17,"name":"Release.mkv","size":100,"progress":1,"download_state":"completed","download_finished":true,"created_at":"2026-01-02T03:04:05Z","hash":"ABC","files":[{"id":1,"name":"Release.mkv","absolute_path":"Release.mkv","size":100}]}]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	torrents, err := tb.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(torrents) != 1 || torrents[0].Id != "17" {
		t.Fatalf("GetTorrents() = %#v, want torrent 17", torrents)
	}

	mu.Lock()
	defer mu.Unlock()
	if !slices.Equal(offsets, []string{"0", "1"}) {
		t.Fatalf("offsets = %v, want [0 1]", offsets)
	}
}

func TestGetTorrentsReturnsPaginationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":17,"name":"Release.mkv","created_at":"2026-01-02T03:04:05Z"}]}`)
			return
		}
		http.Error(w, "temporary failure", http.StatusInternalServerError)
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	torrents, err := tb.GetTorrents()
	if err == nil {
		t.Fatal("GetTorrents() error = nil, want pagination error")
	}
	if torrents != nil {
		t.Fatalf("GetTorrents() torrents = %#v, want nil after pagination error", torrents)
	}
	if got := err.Error(); !strings.Contains(got, "get TorBox torrents at offset 1:") {
		t.Fatalf("GetTorrents() error = %q, want offset context", got)
	}
}

func TestGetTorrentAcceptsObjectAndArrayResponses(t *testing.T) {
	tests := map[string]string{
		"object": `{"success":true,"data":{"id":17,"name":"Release.mkv","size":100,"progress":1,"download_state":"completed","download_finished":true,"created_at":"2026-01-02T03:04:05Z","hash":"ABC","files":[{"id":1,"name":"Release.mkv","absolute_path":"Release.mkv","size":100}]}}`,
		"array":  `{"success":true,"data":[{"id":17,"name":"Release.mkv","size":100,"progress":1,"download_state":"completed","download_finished":true,"created_at":"2026-01-02T03:04:05Z","hash":"ABC","files":[{"id":1,"name":"Release.mkv","absolute_path":"Release.mkv","size":100}]}]}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			t.Cleanup(server.Close)

			torrent, err := testTorbox(server.URL).GetTorrent("17")
			if err != nil {
				t.Fatalf("GetTorrent() error = %v", err)
			}
			if torrent.Id != "17" || torrent.InfoHash != "ABC" || len(torrent.Files) != 1 {
				t.Fatalf("GetTorrent() = %#v, want torrent 17 with one file", torrent)
			}
		})
	}
}

func TestDeleteTorrentUsesControlEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/torrents/controltorrent" {
			t.Errorf("path = %q, want /api/torrents/controltorrent", r.URL.Path)
		}
		var payload struct {
			TorrentID int    `json:"torrent_id"`
			Operation string `json:"operation"`
			All       bool   `json:"all"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload.TorrentID != 42 || payload.Operation != "delete" || payload.All {
			t.Errorf("payload = %#v, want torrent 42 delete operation", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"detail":"Torrent deleted successfully"}`)
	}))
	t.Cleanup(server.Close)

	if err := testTorbox(server.URL).DeleteTorrent("42"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
}

func testTorbox(host string) *Torbox {
	return &Torbox{
		Host:   host,
		client: request.New(request.WithMaxRetries(0)),
		logger: zerolog.Nop(),
		config: config.Debrid{Name: "torbox"},
	}
}

func TestAvailabilityPreservesKeysAndReportsIncompleteBatches(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls > 1 {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"success":true,"data":{"abc":{"size":100}}}`)
	}))
	defer server.Close()
	hashes := []string{"abc", "AbC", "missing"}
	for len(hashes) < 100 {
		hashes = append(hashes, fmt.Sprintf("hash%d", len(hashes)))
	}
	hashes = append(hashes, "unchecked")
	result, err := testTorbox(server.URL).IsAvailable(hashes)
	if err == nil {
		t.Fatal("failed batch returned no error")
	}
	if !result["abc"] || !result["AbC"] {
		t.Fatalf("input spelling was lost: %v", result)
	}
	if cached, checked := result["missing"]; !checked || cached {
		t.Fatalf("cache miss = %v, %v", cached, checked)
	}
	if _, checked := result["unchecked"]; checked {
		t.Fatal("failed batch reported a result")
	}
}

func TestSubmitMagnetAcceptsObjectAndArrayResponses(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	tests := map[string]string{
		"object": `{"success":true,"data":{"torrent_id":17,"hash":"abc"}}`,
		"array":  `{"success":true,"data":[{"id":9,"hash":"other"},{"id":17,"hash":"ABC"}]}`,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, body)
			}))
			t.Cleanup(server.Close)

			torrent, err := testTorbox(server.URL).SubmitMagnet(&types.Torrent{
				InfoHash: "abc",
				Magnet:   &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"},
			})
			if err != nil {
				t.Fatalf("SubmitMagnet() error = %v", err)
			}
			if torrent.Id != "17" {
				t.Fatalf("SubmitMagnet() id = %q, want 17", torrent.Id)
			}
		})
	}
}

func TestFetchDownloadLinkResolvesCDNURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/torrents/requestdl" || r.URL.Query().Get("redirect") != "false" {
			t.Errorf("request = %s, want requestdl with redirect=false", r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"data":"https://store-1.tb-cdn.st/file?token=x"}`)
	}))
	t.Cleanup(server.Close)

	dl, err := testTorbox(server.URL).fetchDownloadLink(t.Context(), &account.Account{Token: "key"}, "7", &types.File{Id: "3", Name: "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if dl.DownloadLink != "https://store-1.tb-cdn.st/file?token=x" {
		t.Fatalf("DownloadLink = %q, want the CDN URL", dl.DownloadLink)
	}
}

func TestFetchDownloadLinkFallsBackToRedirectURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dl, err := testTorbox(server.URL).fetchDownloadLink(t.Context(), &account.Account{Token: "key"}, "7", &types.File{Id: "3", Name: "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dl.DownloadLink, "/api/torrents/requestdl?") || !strings.Contains(dl.DownloadLink, "redirect=true") {
		t.Fatalf("DownloadLink = %q, want the redirect URL", dl.DownloadLink)
	}
}

// TorBox at the active download limit queues an add (queued_id, no torrent_id)
// and starts it later. It must stay a downloading torrent, then take the real
// torrent id once TorBox starts it, instead of being deleted as an error.
func TestQueuedAddIsTrackedUntilTorBoxStartsIt(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	started := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/torrents/createtorrent":
			_, _ = fmt.Fprint(w, `{"success":true,"detail":"Torrent queued successfully.","data":{"hash":"abc","name":"Release","queued_id":5}}`)
		case r.URL.Path == "/api/queued/getqueued":
			if started {
				_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
			} else {
				_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":5,"hash":"abc","name":"Release"}]}`)
			}
		case r.URL.Path == "/api/torrents/mylist" && r.URL.Query().Get("id") == "":
			if r.URL.Query().Get("offset") != "0" || !started {
				_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":17,"hash":"ABC","name":"Release","download_state":"downloading","progress":0.25}]}`)
		case r.URL.Path == "/api/torrents/mylist" && r.URL.Query().Get("id") == "17":
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"id":17,"hash":"ABC","name":"Release","download_state":"downloading","progress":0.25,"created_at":"2026-01-02T03:04:05Z"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	tb := testTorbox(server.URL)

	torrent := &types.Torrent{InfoHash: "abc", DownloadUncached: true, Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}}
	added, err := tb.SubmitMagnet(torrent)
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if added.Id != "queued:5" {
		t.Fatalf("Id = %q, want queued:5", added.Id)
	}
	checked, err := tb.CheckStatus(added)
	if err != nil || checked.Status != types.TorrentStatusDownloading {
		t.Fatalf("CheckStatus() = %v, %v; want downloading, nil", checked.Status, err)
	}

	started = true
	if err := tb.UpdateTorrent(checked); err != nil {
		t.Fatalf("UpdateTorrent() after start error = %v", err)
	}
	if checked.Id != "17" || checked.Progress != 25 {
		t.Fatalf("after start Id=%q Progress=%v, want 17 and 25", checked.Id, checked.Progress)
	}
}

// A torrent id that stops resolving right after submission (TorBox moved it to
// its queue) is found again by hash, not reported as an error.
func TestMissingTorrentIdFallsBackToQueueByHash(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/torrents/mylist" && r.URL.Query().Get("id") == "135390814":
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"success":true,"error":"ITEM_NOT_FOUND","detail":"No torrents found for this user.","data":{}}`)
		case r.URL.Path == "/api/queued/getqueued":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":9,"hash":"OTHER"},{"id":8,"hash":"CCC7332D"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	tb := testTorbox(server.URL)

	torrent := &types.Torrent{Id: "135390814", InfoHash: "ccc7332d", DownloadUncached: true}
	got, err := tb.CheckStatus(torrent)
	if err != nil {
		t.Fatalf("CheckStatus() error = %v", err)
	}
	if got.Id != "queued:8" || got.Status != types.TorrentStatusDownloading {
		t.Fatalf("Id=%q Status=%v, want queued:8 downloading", got.Id, got.Status)
	}
}

func TestCheckingStateIsDownloading(t *testing.T) {
	tb := testTorbox("")
	if got := tb.getTorboxStatus("checking", false); got != types.TorrentStatusDownloading {
		t.Fatalf("checking = %v, want downloading", got)
	}
	if got := tb.getTorboxStatus("stalled (no seeds)", false); got != types.TorrentStatusError {
		t.Fatalf("stalled = %v, want error", got)
	}
}

func TestDeleteQueuedTorrentUsesQueueEndpoint(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/queued/controlqueued" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = fmt.Fprint(w, `{"success":true}`)
	}))
	t.Cleanup(server.Close)
	tb := testTorbox(server.URL)

	if err := tb.DeleteTorrent("queued:5337324"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
	if body["queued_id"] != float64(5337324) || body["operation"] != "delete" {
		t.Fatalf("body = %v, want queued_id 5337324 and operation delete", body)
	}
}
