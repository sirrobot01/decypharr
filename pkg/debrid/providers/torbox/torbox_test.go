package torbox

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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
		if got := r.URL.Query().Get("files"); got != "false" {
			t.Errorf("files = %q, want false", got)
		}
		if got := r.URL.Query().Get("limit"); got != strconv.Itoa(torrentListPageLimit) {
			t.Errorf("limit = %q, want %d", got, torrentListPageLimit)
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
	if !torrents[0].SummaryOnly {
		t.Fatal("list rows must be marked SummaryOnly")
	}

	mu.Lock()
	defer mu.Unlock()
	// A page shorter than the limit is the last one; no empty request follows.
	if !slices.Equal(offsets, []string{"0"}) {
		t.Fatalf("offsets = %v, want [0]", offsets)
	}
}

func TestGetTorrentsPagesUntilShortPage(t *testing.T) {
	var (
		mu      sync.Mutex
		offsets []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		mu.Lock()
		offsets = append(offsets, r.URL.Query().Get("offset"))
		mu.Unlock()

		count := torrentListPageLimit
		if offset > 0 {
			count = 2
		}
		rows := make([]string, 0, count)
		for i := range count {
			rows = append(rows, fmt.Sprintf(`{"id":%d,"name":"T%d","hash":"H%d","created_at":"2026-01-02T03:04:05Z"}`, offset+i, offset+i, offset+i))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(rows, ","))
	}))
	t.Cleanup(server.Close)

	torrents, err := testTorbox(server.URL).GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(torrents) != torrentListPageLimit+2 {
		t.Fatalf("GetTorrents() returned %d torrents, want %d", len(torrents), torrentListPageLimit+2)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"0", strconv.Itoa(torrentListPageLimit)}; !slices.Equal(offsets, want) {
		t.Fatalf("offsets = %v, want %v", offsets, want)
	}
}

func TestGetTorrentsReturnsPaginationErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") == "0" {
			// A full page, so GetTorrents asks for the next one.
			rows := make([]string, 0, torrentListPageLimit)
			for i := range torrentListPageLimit {
				rows = append(rows, fmt.Sprintf(`{"id":%d,"name":"Release.mkv","created_at":"2026-01-02T03:04:05Z"}`, i))
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"success":true,"data":[%s]}`, strings.Join(rows, ","))
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
	if got := err.Error(); !strings.Contains(got, "get TorBox torrents at offset "+strconv.Itoa(torrentListPageLimit)+":") {
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

func TestFetchDownloadLinkDoesNotFallBackToRedirectURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	dl, err := testTorbox(server.URL).fetchDownloadLink(t.Context(), &account.Account{Token: "key"}, "7", &types.File{Id: "3", Name: "a.mkv"})
	if err == nil {
		t.Fatalf("fetchDownloadLink() = %q, want an error", dl.DownloadLink)
	}
	if dl.DownloadLink != "" {
		t.Fatalf("DownloadLink = %q, want none (streaming the redirect URL bypasses the rate limiter)", dl.DownloadLink)
	}
}

func TestFetchDownloadLinkUsesTheMintingAccountToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("token"); got != "download-key" {
			t.Errorf("token = %q, want download-key", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"success":true,"data":"https://store-1.tb-cdn.st/file?token=x"}`)
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	tb.APIKey = "main-key"
	dl, err := tb.fetchDownloadLink(t.Context(), &account.Account{Token: "download-key"}, "7", &types.File{Id: "3", Name: "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if dl.Token != "download-key" {
		t.Fatalf("Token = %q, want download-key so link errors disable the right account", dl.Token)
	}
}
