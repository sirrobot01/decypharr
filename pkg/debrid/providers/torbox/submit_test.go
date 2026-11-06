package torbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func magnetTorrent(uncached bool) *types.Torrent {
	return &types.Torrent{
		InfoHash:         "abc",
		DownloadUncached: uncached,
		Magnet:           &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"},
	}
}

func TestStatusFailsOnlyOnDocumentedFailures(t *testing.T) {
	tb := testTorbox("")
	for _, test := range []struct {
		state    string
		finished bool
		want     types.TorrentStatus
	}{
		{"downloading", false, types.TorrentStatusDownloading},
		{"checking", false, types.TorrentStatusDownloading},
		{"stalled (no seeds)", false, types.TorrentStatusDownloading},
		{"Stalled (No seeds)", false, types.TorrentStatusDownloading},
		{"metaDL", false, types.TorrentStatusDownloading},
		{"queued", false, types.TorrentStatusDownloading},
		{"a state added next year", false, types.TorrentStatusDownloading},
		{"a state added next year", true, types.TorrentStatusDownloaded},
		{"cached", false, types.TorrentStatusDownloaded},
		{"uploading (no peers)", true, types.TorrentStatusDownloaded},
		{"failed", false, types.TorrentStatusError},
		{"failed (processing)", false, types.TorrentStatusError},
		{"expired", true, types.TorrentStatusError},
		{"missing", true, types.TorrentStatusError},
		{"reported missing", true, types.TorrentStatusError},
		{"(Reported) Missing", true, types.TorrentStatusError},
	} {
		if got := tb.getTorboxStatus(test.state, test.finished); got != test.want {
			t.Errorf("getTorboxStatus(%q, %v) = %s, want %s", test.state, test.finished, got, test.want)
		}
	}
}

func TestSubmitMapsTorBoxRefusals(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"active limit", http.StatusBadRequest, `{"success":false,"error":"ACTIVE_LIMIT","detail":"too many"}`, customerror.TooManyActiveDownloadsError},
		{"too large", http.StatusOK, `{"success":false,"error":"DOWNLOAD_TOO_LARGE","detail":"plan limit"}`, customerror.TorrentFailedError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)

			_, err := testTorbox(server.URL).SubmitMagnet(magnetTorrent(true))
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitMagnet() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestSubmitPausesAddsAfterAddLimit(t *testing.T) {
	var adds, lists atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/torrents/mylist" {
			lists.Add(1)
			_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
			return
		}
		adds.Add(1)
		w.Header().Set("Retry-After", "600")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	tb.client = request.New(request.WithMaxRetries(3))

	begin := time.Now()
	for range 3 {
		_, err := tb.SubmitMagnet(magnetTorrent(true))
		throttled, ok := errors.AsType[*request.ThrottledError](err)
		if !ok {
			t.Fatalf("SubmitMagnet() error = %v, want ThrottledError", err)
		}
		if throttled.RetryAfter <= 590*time.Second {
			t.Fatalf("RetryAfter = %s", throttled.RetryAfter)
		}
	}
	if adds.Load() != 1 {
		t.Fatalf("createtorrent calls = %d, want 1", adds.Load())
	}
	if elapsed := time.Since(begin); elapsed > 2*time.Second {
		t.Fatalf("adds took %s, want fail fast", elapsed)
	}

	// The add limit must not stop listing or streaming.
	if _, err := tb.GetTorrents(); err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if lists.Load() == 0 {
		t.Fatal("listing did not reach the server")
	}
}

func TestSubmitDoesNotRetryAnAdd(t *testing.T) {
	var adds atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		adds.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	tb.client = request.New(request.WithMaxRetries(3), request.WithRetryableStatus(http.StatusBadGateway))
	if _, err := tb.SubmitMagnet(magnetTorrent(true)); err == nil {
		t.Fatal("SubmitMagnet() error = nil")
	}
	if adds.Load() != 1 {
		t.Fatalf("createtorrent calls = %d, want 1", adds.Load())
	}
}

func TestSubmitCachedOnlyChecksBeforeSpendingAnAdd(t *testing.T) {
	for _, test := range []struct {
		name     string
		check    func(http.ResponseWriter)
		wantAdds int64
		wantErr  error
	}{
		{"not cached", func(w http.ResponseWriter) { _, _ = fmt.Fprint(w, `{"success":true,"data":{}}`) }, 0, customerror.TorrentNotCachedError},
		{"cached", func(w http.ResponseWriter) { _, _ = fmt.Fprint(w, `{"success":true,"data":{"abc":{"size":100}}}`) }, 1, nil},
		{"failed check still adds", func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) }, 1, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			var adds atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/torrents/checkcached" {
					test.check(w)
					return
				}
				adds.Add(1)
				_, _ = fmt.Fprint(w, `{"success":true,"data":{"torrent_id":17,"hash":"abc"}}`)
			}))
			t.Cleanup(server.Close)

			_, err := testTorbox(server.URL).SubmitMagnet(magnetTorrent(false))
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("SubmitMagnet() error = %v, want %v", err, test.wantErr)
			}
			if adds.Load() != test.wantAdds {
				t.Fatalf("createtorrent calls = %d, want %d", adds.Load(), test.wantAdds)
			}
		})
	}
}

func TestSubmitUploadsTorrentFiles(t *testing.T) {
	var file []byte
	var magnet string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upload, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("FormFile() error = %v", err)
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		file, _ = io.ReadAll(upload)
		magnet = r.FormValue("magnet")
		_, _ = fmt.Fprint(w, `{"success":true,"data":{"torrent_id":17,"hash":"abc"}}`)
	}))
	t.Cleanup(server.Close)

	torrent := magnetTorrent(true)
	torrent.Magnet.File = []byte("d8:announce...")
	if _, err := testTorbox(server.URL).SubmitMagnet(torrent); err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if string(file) != "d8:announce..." || magnet != "" {
		t.Fatalf("uploaded file %q and magnet %q", file, magnet)
	}
}

// TorBox at the active download limit queues an add (queued_id, no torrent_id)
// and starts it later. It stays a downloading torrent, then takes the real
// torrent id once TorBox starts it.
func TestQueuedAddIsTrackedUntilTorBoxStartsIt(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var started atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/torrents/createtorrent":
			_, _ = fmt.Fprint(w, `{"success":true,"detail":"Torrent queued successfully.","data":{"hash":"abc","name":"Release","queued_id":5}}`)
		case r.URL.Path == "/api/queued/getqueued":
			if started.Load() {
				_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"success":true,"data":[{"id":5,"hash":"abc","name":"Release"}]}`)
		case r.URL.Path == "/api/torrents/mylist" && r.URL.Query().Get("id") == "":
			if r.URL.Query().Get("offset") != "0" || !started.Load() {
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

	added, err := tb.SubmitMagnet(magnetTorrent(true))
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if added.Id != "queued:5" {
		t.Fatalf("Id = %q, want queued:5", added.Id)
	}
	checked, err := tb.CheckStatus(added)
	if err != nil || checked.Status != types.TorrentStatusDownloading {
		t.Fatalf("CheckStatus() = %v, %v; want downloading", checked.Status, err)
	}

	started.Store(true)
	if err := tb.UpdateTorrent(checked); err != nil {
		t.Fatalf("UpdateTorrent() after start error = %v", err)
	}
	if checked.Id != "17" || checked.Progress != 25 {
		t.Fatalf("after start Id=%q Progress=%v, want 17 and 25", checked.Id, checked.Progress)
	}
}

func TestMissingTorrentResolvesByHash(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	for _, test := range []struct {
		name    string
		queued  string
		wantId  string
		wantErr error
	}{
		{"moved to the queue", `{"success":true,"data":[{"id":9,"hash":"OTHER"},{"id":8,"hash":"ABC"}]}`, "queued:8", nil},
		{"gone from the account", `{"success":true,"data":[]}`, "135", customerror.TorrentNotFoundError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/api/torrents/mylist" && r.URL.Query().Get("id") == "135":
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"success":true,"error":"ITEM_NOT_FOUND","data":{}}`)
				case r.URL.Path == "/api/torrents/mylist":
					_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
				case r.URL.Path == "/api/queued/getqueued":
					_, _ = fmt.Fprint(w, test.queued)
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(server.Close)

			got, err := testTorbox(server.URL).CheckStatus(&types.Torrent{Id: "135", InfoHash: "abc", DownloadUncached: true})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("CheckStatus() error = %v, want %v", err, test.wantErr)
			}
			if got.Id != test.wantId {
				t.Fatalf("Id = %q, want %q", got.Id, test.wantId)
			}
		})
	}
}

func TestUpdateTorrentSurvivesEmptyData(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	for _, body := range []string{`{"success":true,"data":null}`, `{"success":true,"data":{}}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, body)
		}))
		err := testTorbox(server.URL).UpdateTorrent(&types.Torrent{Id: "17"})
		server.Close()
		if !errors.Is(err, customerror.TorrentNotFoundError) {
			t.Fatalf("UpdateTorrent(%s) error = %v, want not found", body, err)
		}
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

	if err := testTorbox(server.URL).DeleteTorrent("queued:5337324"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
	if body["queued_id"] != float64(5337324) || body["operation"] != "delete" {
		t.Fatalf("body = %v", body)
	}
}

func TestCheckFileSeesTorrentsNewerThanTheSnapshot(t *testing.T) {
	var torrents atomic.Value
	torrents.Store(`{"id":1,"download_present":true}`)
	var listings atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("offset") != "0" {
			_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
			return
		}
		listings.Add(1)
		if r.URL.Query().Get("bypass_cache") != "true" {
			t.Error("repair must read the live list")
		}
		_, _ = fmt.Fprintf(w, `{"success":true,"data":[%s]}`, torrents.Load())
	}))
	t.Cleanup(server.Close)
	tb := testTorbox(server.URL)

	if err := tb.CheckFile(t.Context(), "hash", "torbox://1/0"); err != nil {
		t.Fatalf("CheckFile(known) error = %v", err)
	}
	if err := tb.CheckFile(t.Context(), "hash", "torbox://1/1"); err != nil {
		t.Fatalf("CheckFile(known) error = %v", err)
	}
	if listings.Load() != 1 {
		t.Fatalf("listings = %d, want one per sweep", listings.Load())
	}

	torrents.Store(`{"id":1,"download_present":true},{"id":2,"download_present":true},{"id":3,"download_present":false}`)
	tb.downloadPresentAt = time.Now().Add(-time.Minute)

	if err := tb.CheckFile(t.Context(), "hash", "torbox://2/0"); err != nil {
		t.Fatalf("CheckFile(newer torrent) error = %v", err)
	}
	if err := tb.CheckFile(t.Context(), "hash", "torbox://3/0"); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("CheckFile(not present) error = %v", err)
	}
	if err := tb.CheckFile(t.Context(), "hash", "torbox://4/0"); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("CheckFile(unknown) error = %v", err)
	}
	if listings.Load() != 2 {
		t.Fatalf("listings = %d, want a fresh snapshot to answer misses", listings.Load())
	}
}

func TestSyncAccountReadsTheAccountKey(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	for _, test := range []struct {
		plan    int
		expires bool
	}{{2, true}, {0, false}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.Header.Get("Authorization"); got != "Bearer account-key" {
				t.Errorf("Authorization = %q", got)
			}
			_, _ = fmt.Fprintf(w, `{"success":true,"data":{"id":1,"email":"a@example.com","plan":%d,"premium_expires_at":"2030-01-02T03:04:05Z"}}`, test.plan)
		}))
		tb := testTorbox(server.URL)
		tb.accountsManager = account.NewManager(config.Debrid{Name: "torbox", DownloadAPIKeys: []string{"account-key"}}, nil, zerolog.Nop())
		tb.SyncAccounts()
		server.Close()

		acc := tb.accountsManager.Current()
		if acc.Username != "a@example.com" {
			t.Fatalf("Username = %q", acc.Username)
		}
		if got := !acc.Expiration.IsZero(); got != test.expires {
			t.Fatalf("plan %d: Expiration = %s", test.plan, acc.Expiration)
		}
	}
}

func TestProfileRefreshesAfterTheCacheWindow(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = fmt.Fprint(w, `{"success":true,"data":{"id":1,"email":"a@example.com","plan":2}}`)
	}))
	t.Cleanup(server.Close)
	tb := testTorbox(server.URL)

	for range 2 {
		if _, err := tb.GetProfile(); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want the cached profile", calls.Load())
	}
	tb.profileFetched = time.Now().Add(-2 * profileCacheDuration)
	if _, err := tb.GetProfile(); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, a stale profile must refresh", calls.Load())
	}
}

func TestRefetchReplacesALinkFromASeparateDownloadKey(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var minted atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `{"success":true,"data":"https://cdn.example/%d"}`, minted.Add(1))
	}))
	t.Cleanup(server.Close)

	tb := testTorbox(server.URL)
	tb.APIKey = "main-key"
	tb.accountsManager = account.NewManager(config.Debrid{Name: "torbox", DownloadAPIKeys: []string{"download-key"}}, nil, zerolog.Nop())

	file := &types.File{Id: "1", Name: "a.mkv", Link: "torbox://7/1"}
	first, err := tb.GetDownloadLink(t.Context(), "7", file)
	if err != nil {
		t.Fatal(err)
	}
	if err := tb.DeleteLink(first); err != nil {
		t.Fatalf("DeleteLink() error = %v", err)
	}
	second, err := tb.GetDownloadLink(t.Context(), "7", file)
	if err != nil {
		t.Fatal(err)
	}
	if second.DownloadLink == first.DownloadLink {
		t.Fatalf("refetch returned the dead link %q", first.DownloadLink)
	}
}
