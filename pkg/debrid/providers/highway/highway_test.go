package highway

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const testHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func testHighway(t *testing.T, handler http.HandlerFunc) *Highway {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dc := config.Debrid{Name: "highway", Provider: "highway", APIKey: "token", DownloadAPIKeys: []string{"token"}}
	return &Highway{
		Host:                  server.URL,
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, nil, zerolog.Nop()),
		autoExpiresLinksAfter: maxLinkLifetime,
		client:                request.New(request.WithHeaders(map[string]string{"Authorization": "Bearer token"}), request.WithMaxRetries(0)),
		logger:                zerolog.Nop(),
		config:                dc,
	}
}

func writeJSON(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, body)
}

const completedTransfer = `{"id":"11","service":"torrent","name":"Film A","state":"completed","progress":100,"size":9,
	"hash":"` + testHash + `","added_at":1767225600,"seeder":3,
	"files":[{"id":"1111111111111111","index":0,"path":"Film A/film.mkv","size":5,"partial":false,"download_url":"https://dl.example/film"},
	{"id":"2222222222222222","index":1,"path":"Film A/rest.mkv.part","size":4,"partial":true,"download_url":"https://dl.example/rest"}]}`

func TestIsAvailableBatchesAndKeepsInputSpelling(t *testing.T) {
	var (
		mu     sync.Mutex
		checks []string
	)
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/transfers.php" || r.URL.Query().Get("service") != "torrent" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		checks = append(checks, r.URL.Query().Get("check"))
		mu.Unlock()
		writeJSON(w, 200, `{"service":"torrent","results":{"`+testHash+`":{"known":true,"ready":true,"id":null},
			"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb":{"known":true,"ready":false,"id":null}}}`)
	})

	hashes := []string{strings.ToUpper(testHash), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "BASE32HASHBASE32HASHBASE32HASH12", ""}
	for i := 0; i < 120; i++ {
		hashes = append(hashes, fmt.Sprintf("%040x", i+1))
	}
	got, err := hw.IsAvailable(hashes)
	if err != nil {
		t.Fatalf("IsAvailable() error = %v", err)
	}
	if !got[strings.ToUpper(testHash)] {
		t.Errorf("cached hash not reported available")
	}
	if got["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"] {
		t.Errorf("known but unfinished hash reported available")
	}
	if v, ok := got["BASE32HASHBASE32HASHBASE32HASH12"]; !ok || v {
		t.Errorf("non-hex hash = %v, %v; want false, true", v, ok)
	}
	if _, ok := got[""]; ok {
		t.Errorf("empty hash must not be in the result")
	}
	if len(checks) != 2 {
		t.Fatalf("requests = %d, want 2 batches", len(checks))
	}
	if n := len(strings.Split(checks[0], ",")); n != checkBatchSize {
		t.Errorf("first batch = %d hashes, want %d", n, checkBatchSize)
	}
}

func TestSubmitMagnetOnlyWhenCached(t *testing.T) {
	var posted []string
	ready := false
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Query().Get("check") != "":
			writeJSON(w, 200, fmt.Sprintf(`{"results":{"%s":{"known":%t,"ready":%t,"id":null}}}`, testHash, ready, ready))
		case r.Method == http.MethodPost:
			if err := r.ParseForm(); err != nil {
				t.Fatalf("ParseForm: %v", err)
			}
			if r.Header.Get("Authorization") != "Bearer token" {
				t.Errorf("missing bearer token")
			}
			posted = append(posted, r.PostForm.Get("magnet"))
			writeJSON(w, 200, `{"id":"11","service":"torrent","state":"completed"}`)
		default:
			http.NotFound(w, r)
		}
	})

	torrent := &types.Torrent{InfoHash: testHash, Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:" + testHash}}
	if _, err := hw.SubmitMagnet(torrent); !errors.Is(err, customerror.TorrentNotCachedError) {
		t.Fatalf("SubmitMagnet() uncached error = %v, want TorrentNotCachedError", err)
	}
	if len(posted) != 0 {
		t.Fatalf("uncached torrent must not be added")
	}

	ready = true
	added, err := hw.SubmitMagnet(torrent)
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if added.Id != "11" || added.Debrid != "highway" || len(posted) != 1 {
		t.Fatalf("added = %+v, posted = %v", added, posted)
	}

	uncached := &types.Torrent{InfoHash: testHash, DownloadUncached: true, Magnet: torrent.Magnet}
	ready = false
	if _, err := hw.SubmitMagnet(uncached); err != nil {
		t.Fatalf("SubmitMagnet() with DownloadUncached error = %v", err)
	}
	if len(posted) != 2 {
		t.Fatalf("DownloadUncached must add without checking the cache")
	}
}

func TestUpdateTorrentMapsFilesAndSkipsPartial(t *testing.T) {
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("id") == "11" && q.Get("service") == "torrent" {
			writeJSON(w, 200, completedTransfer)
			return
		}
		http.NotFound(w, r)
	})

	tr, err := hw.GetTorrent("11")
	if err != nil {
		t.Fatalf("GetTorrent() error = %v", err)
	}
	if tr.InfoHash != testHash || tr.Status != types.TorrentStatusDownloaded || tr.Bytes != 9 || tr.Added.Unix() != 1767225600 {
		t.Fatalf("torrent = %+v", tr)
	}
	if tr.OriginalFilename != "Film A" {
		t.Errorf("OriginalFilename = %q", tr.OriginalFilename)
	}
	if len(tr.Files) != 1 {
		t.Fatalf("files = %+v, want only the finished file", tr.Files)
	}
	file := tr.Files["film.mkv"]
	if file.Id != "1111111111111111" || file.Path != "Film A/film.mkv" || file.Link != "highway://11/1111111111111111" {
		t.Fatalf("file = %+v", file)
	}
}

func TestQueuedTransferIsResolvedByHash(t *testing.T) {
	queueId := strings.Repeat("c", 32)
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("id") == queueId:
			http.NotFound(w, r) // queue entry is gone once the transfer started
		case q.Get("check") == testHash:
			writeJSON(w, 200, `{"results":{"`+testHash+`":{"known":true,"ready":true,"id":"11"}}}`)
		case q.Get("id") == "11" && q.Get("service") == "torrent":
			writeJSON(w, 200, completedTransfer)
		default:
			http.NotFound(w, r)
		}
	})

	tr := &types.Torrent{Id: queueId, InfoHash: testHash}
	got, err := hw.CheckStatus(tr)
	if err != nil {
		t.Fatalf("CheckStatus() error = %v", err)
	}
	if got.Id != "11" || got.Status != types.TorrentStatusDownloaded {
		t.Fatalf("torrent = %+v", got)
	}
}

func TestCheckStatusNotCached(t *testing.T) {
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, `{"id":"11","service":"torrent","name":"Film A","state":"downloading","progress":40,"size":9,"hash":"`+testHash+`","files":[]}`)
	})
	if _, err := hw.CheckStatus(&types.Torrent{Id: "11"}); !errors.Is(err, customerror.TorrentNotCachedError) {
		t.Fatalf("CheckStatus() error = %v, want TorrentNotCachedError", err)
	}
	if _, err := hw.CheckStatus(&types.Torrent{Id: "11", DownloadUncached: true}); err != nil {
		t.Fatalf("CheckStatus() with DownloadUncached error = %v", err)
	}
}

func TestGetTorrentsPagesAndSkipsQueued(t *testing.T) {
	var offsets []string
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("service") != "torrent" || q.Get("limit") != "500" {
			t.Errorf("unexpected list query %v", q)
		}
		offsets = append(offsets, q.Get("offset"))
		if q.Get("offset") == "0" {
			items := make([]string, 0, 500)
			items = append(items, `{"id":"11","name":"Film A","state":"completed","progress":100,"size":9,"hash":"`+testHash+`","added_at":1767225600}`)
			items = append(items, `{"id":"`+strings.Repeat("c", 32)+`","name":"Queued","state":"queued","progress":0,"size":0,"hash":null}`)
			for i := len(items); i < 500; i++ {
				items = append(items, fmt.Sprintf(`{"id":"%d","name":"T%d","state":"downloading","progress":5,"size":1,"hash":"%040x"}`, 100+i, i, i))
			}
			writeJSON(w, 200, `{"transfers":[`+strings.Join(items, ",")+`],"total":501}`)
			return
		}
		writeJSON(w, 200, `{"transfers":[{"id":"99","name":"Last","state":"completed","progress":100,"size":1,"hash":"`+strings.Repeat("e", 40)+`"}],"total":501}`)
	})

	torrents, err := hw.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if strings.Join(offsets, ",") != "0,500" {
		t.Fatalf("offsets = %v", offsets)
	}
	if len(torrents) != 500 {
		t.Fatalf("torrents = %d, want 500 (queued entry without hash skipped)", len(torrents))
	}
	if torrents[0].InfoHash != testHash || torrents[0].Status != types.TorrentStatusDownloaded {
		t.Fatalf("first torrent = %+v", torrents[0])
	}
}

func TestDownloadLinkAndCheckFile(t *testing.T) {
	var auth []string
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		auth = append(auth, r.Header.Get("Authorization"))
		if q.Get("id") == "11" && q.Get("service") == "torrent" && q.Get("file") == "1111111111111111" {
			writeJSON(w, 200, `{"id":"11","service":"torrent","name":"Film A","state":"completed","file":{"id":"1111111111111111","index":0,"path":"Film A/film.mkv","size":5,"partial":false,"download_url":"https://dl.example/film?sig=x"}}`)
			return
		}
		writeJSON(w, 404, `{"error":"file_not_found","message":"x"}`)
	})

	file := &types.File{TorrentId: "11", Id: "1111111111111111", Name: "film.mkv", Size: 5, Link: "highway://11/1111111111111111"}
	link, err := hw.GetDownloadLink(t.Context(), "11", file)
	if err != nil {
		t.Fatalf("GetDownloadLink() error = %v", err)
	}
	if link.DownloadLink != "https://dl.example/film?sig=x" || link.ExpiresAt.IsZero() {
		t.Fatalf("link = %+v", link)
	}
	if auth[0] != "Bearer token" {
		t.Errorf("download link request auth = %q", auth[0])
	}

	if err := hw.CheckFile(t.Context(), testHash, "highway://11/1111111111111111"); err != nil {
		t.Fatalf("CheckFile() error = %v", err)
	}
	if err := hw.CheckFile(t.Context(), testHash, "highway://11/0000000000000000"); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("CheckFile() missing file error = %v", err)
	}
	if err := hw.CheckFile(t.Context(), testHash, "not-a-link"); !errors.Is(err, customerror.HosterUnavailableError) {
		t.Fatalf("CheckFile() bad link error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := hw.CheckFile(ctx, testHash, "highway://11/1111111111111111"); !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckFile() cancelled error = %v", err)
	}
}

func TestProfileSlotsAndDelete(t *testing.T) {
	var deleted []string
	hw := testHighway(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodDelete:
			deleted = append(deleted, q.Get("id")+"/"+q.Get("service"))
			writeJSON(w, 200, `{"removed":true}`)
		case q.Get("stats") == "1":
			writeJSON(w, 200, `{"username":"tester","user_id":5,"plan":"Pro","kind":"subscription","premium":true,"plan_expires_at":4102444800,
				"transfers":{"active":3,"limit":30,"available":27}}`)
		default:
			writeJSON(w, 401, `{"error":"invalid_token","message":"x"}`)
		}
	})

	profile, err := hw.GetProfile()
	if err != nil {
		t.Fatalf("GetProfile() error = %v", err)
	}
	if profile.Username != "tester" || profile.Id != 5 || profile.Type != "subscription" || profile.Expiration.Unix() != 4102444800 || profile.Premium <= 0 {
		t.Fatalf("profile = %+v", profile)
	}
	slots, err := hw.GetAvailableSlots()
	if err != nil || slots != 27 {
		t.Fatalf("GetAvailableSlots() = %d, %v", slots, err)
	}

	if err := hw.DeleteTorrent("11"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
	if err := hw.DeleteTorrent(strings.Repeat("c", 32)); err != nil {
		t.Fatalf("DeleteTorrent(queue id) error = %v", err)
	}
	if strings.Join(deleted, ",") != "11/torrent,"+strings.Repeat("c", 32)+"/" {
		t.Fatalf("deleted = %v", deleted)
	}

	if _, err := hw.GetTorrent("11"); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("GetTorrent() with bad token error = %v", err)
	}
}
