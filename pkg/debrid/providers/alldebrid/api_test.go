package alldebrid

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestDocumentedEndpointVersions(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v4/magnet/upload", func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("magnets[]"); got != "magnet:?xt=urn:btih:abc" {
			t.Errorf("magnet = %q", got)
		}
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"magnets":[{"id":42,"ready":true}]}}`)
	})
	mux.HandleFunc("GET /v4.1/magnet/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"magnets":{"id":42,"filename":"a.mkv","statusCode":4,"size":10,"files":[{"n":"a.mkv","s":10,"l":"https://alldebrid.com/f/a"}]}}}`)
	})
	mux.HandleFunc("GET /v4/magnet/delete", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success"}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ad := testAllDebrid(server.URL + "/v4")
	ad.StatusHost = server.URL + "/v4.1"

	torrent, err := ad.SubmitMagnet(&types.Torrent{DownloadUncached: true, Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}})
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if torrent.Id != "42" || torrent.Debrid != "alldebrid" {
		t.Fatalf("torrent = %+v", torrent)
	}
	if err := ad.UpdateTorrent(torrent); err != nil {
		t.Fatalf("UpdateTorrent() error = %v", err)
	}
	if err := ad.DeleteTorrent("42"); err != nil {
		t.Fatalf("DeleteTorrent() error = %v", err)
	}
}

func TestUploadErrorsArrivePerMagnet(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want error
	}{
		{"too many active", `{"status":"success","data":{"magnets":[{"error":{"code":"MAGNET_TOO_MANY_ACTIVE","message":"30 active"}}]}}`, customerror.TooManyActiveDownloadsError},
		{"invalid magnet", `{"status":"success","data":{"magnets":[{"error":{"code":"MAGNET_INVALID_URI","message":"bad"}}]}}`, customerror.TorrentFailedError},
		{"envelope error", `{"status":"error","error":{"code":"MAGNET_TOO_MANY_ACTIVE","message":"30 active"}}`, customerror.TooManyActiveDownloadsError},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)

			torrent, err := testAllDebrid(server.URL).SubmitMagnet(&types.Torrent{Magnet: &utils.Magnet{Link: "magnet:?xt=urn:btih:abc"}})
			if !errors.Is(err, test.want) {
				t.Fatalf("SubmitMagnet() = %+v, error = %v, want %v", torrent, err, test.want)
			}
		})
	}
}

func TestStatusEnvelopeErrorsAreNotMissingTorrents(t *testing.T) {
	for _, test := range []struct {
		code     string
		notFound bool
	}{
		{"MAGNET_INVALID_ID", true},
		{"MAINTENANCE", false},
	} {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"status":"error","error":{"code":%q,"message":"nope"}}`, test.code)
			}))
			t.Cleanup(server.Close)

			err := testAllDebrid(server.URL).UpdateTorrent(&types.Torrent{Id: "42"})
			if err == nil {
				t.Fatal("UpdateTorrent() error = nil")
			}
			if got := errors.Is(err, customerror.TorrentNotFoundError); got != test.notFound {
				t.Fatalf("UpdateTorrent() error = %v, not found = %v, want %v", err, got, test.notFound)
			}
		})
	}
}

func TestGetTorrentsLoadsFilesInBatches(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	config.Get().AllowedExt = []string{"mkv"}

	const count = magnetFilesBatchSize + 5
	magnets := make([]string, count)
	for i := range magnets {
		magnets[i] = fmt.Sprintf(`{"id":%d,"filename":"m%d","hash":"h%d","statusCode":4,"size":2000}`, i+1, i, i)
	}
	var batches atomic.Int64
	var statusCalls atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /magnet/status", func(w http.ResponseWriter, r *http.Request) {
		statusCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"magnets":[%s]}}`, strings.Join(magnets, ","))
	})
	mux.HandleFunc("POST /magnet/files", func(w http.ResponseWriter, r *http.Request) {
		batches.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Error(err)
		}
		items := make([]string, 0, len(r.Form["id[]"]))
		for _, id := range r.Form["id[]"] {
			if id == "3" {
				items = append(items, `{"id":"3","error":{"code":"MAGNET_INVALID_ID","message":"gone"}}`)
				continue
			}
			// The id is a string in the docs, so cover both forms.
			quote := `"`
			if id == "4" {
				quote = ""
			}
			items = append(items, fmt.Sprintf(`{"id":%s%s%s,"files":[{"n":"Sub","e":[{"n":"m%s.mkv","s":2000,"l":"https://alldebrid.com/f/%s"}]}]}`, quote, id, quote, id, id))
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"magnets":[%s]}}`, strings.Join(items, ","))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	torrents, err := testAllDebrid(server.URL).GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(torrents) != count {
		t.Fatalf("torrents = %d, want %d", len(torrents), count)
	}
	if batches.Load() != 2 || statusCalls.Load() != 1 {
		t.Fatalf("%d file batches and %d status calls, want 2 and 1", batches.Load(), statusCalls.Load())
	}
	empty := slices.IndexFunc(torrents, func(t *types.Torrent) bool { return len(t.Files) == 0 })
	if empty < 0 || torrents[empty].Id != "3" {
		t.Fatalf("empty torrent index = %d, want only the magnet with an error", empty)
	}
	for _, torrent := range torrents {
		if torrent.Id == "3" {
			continue
		}
		name := "m" + torrent.Id + ".mkv"
		if file, ok := torrent.Files[name]; !ok || file.Link == "" {
			t.Fatalf("torrent %s files = %+v", torrent.Id, torrent.Files)
		}
	}
}

func TestGetTorrentsFailsWhenFilesFail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /magnet/status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"success","data":{"magnets":[{"id":1,"statusCode":4}]}}`)
	})
	mux.HandleFunc("POST /magnet/files", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	if _, err := testAllDebrid(server.URL).GetTorrents(); err == nil {
		t.Fatal("GetTorrents() error = nil")
	}
}

func TestCheckStatusRestartsNoPeerStatus(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var restarts atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("GET /magnet/status", func(w http.ResponseWriter, r *http.Request) {
		code := 15
		if restarts.Load() > 0 {
			code = 1
		}
		_, _ = fmt.Fprintf(w, `{"status":"success","data":{"magnets":[{"id":42,"filename":"a","statusCode":%d}]}}`, code)
	})
	mux.HandleFunc("POST /magnet/restart", func(w http.ResponseWriter, r *http.Request) {
		restarts.Add(1)
		_, _ = fmt.Fprint(w, `{"status":"success"}`)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	torrent, err := testAllDebrid(server.URL).CheckStatus(&types.Torrent{Id: "42", DownloadUncached: true})
	if err != nil {
		t.Fatalf("CheckStatus() error = %v", err)
	}
	if torrent.Status != types.TorrentStatusDownloading || restarts.Load() != 1 {
		t.Fatalf("status = %s after %d restarts", torrent.Status, restarts.Load())
	}
}

func TestCheckFileKeepsTemporaryLinkFailuresOutOfRepair(t *testing.T) {
	for _, test := range []struct {
		code   string
		broken bool
	}{
		{"LINK_DOWN", true},
		{"LINK_TEMPORARY_UNAVAILABLE", false},
		{"LINK_HOST_UNAVAILABLE", false},
	} {
		t.Run(test.code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = fmt.Fprintf(w, `{"status":"success","data":{"infos":[{"error":{"code":%q,"message":"nope"}}]}}`, test.code)
			}))
			t.Cleanup(server.Close)

			ad := testAllDebrid(server.URL)
			ad.repairClient = ad.client
			err := ad.CheckFile(t.Context(), "", "https://alldebrid.com/f/a")
			if err == nil {
				t.Fatal("CheckFile() error = nil")
			}
			if got := errors.Is(err, customerror.HosterUnavailableError); got != test.broken {
				t.Fatalf("CheckFile() error = %v, broken = %v, want %v", err, got, test.broken)
			}
		})
	}
}
