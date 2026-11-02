package torbox

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const usenetDone = `{"id":2515089,"hash":"19028D8F31E388975068B9F11CAB4476","name":"Show.S01E01.1080p-GRP","size":300,"progress":1,"download_state":"completed","download_finished":true,"download_present":true,"created_at":"2026-09-23T09:03:01Z","files":[{"id":0,"name":"Show.S01E01.1080p-GRP/ebfd37cc.mkv","absolute_path":"/downloads/x/Show.S01E01.1080p-GRP/ebfd37cc.mkv","size":300}]}`
const usenetFailed = `{"id":2515090,"hash":"aa","name":"Broken","download_state":"failed (Aborted)","download_finished":true,"download_present":false,"created_at":"2026-09-23T09:03:01Z","files":[]}`

func usenetServer(t *testing.T, failUsenet bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		off := r.URL.Query().Get("offset")
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/usenet/mylist") && failUsenet:
			http.Error(w, "boom", http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/api/usenet/mylist") && r.URL.Query().Get("id") != "":
			_, _ = fmt.Fprint(w, `{"success":true,"data":`+usenetDone+`}`)
		case strings.HasPrefix(r.URL.Path, "/api/usenet/mylist") && off == "0":
			_, _ = fmt.Fprint(w, `{"success":true,"data":[`+usenetDone+`,`+usenetFailed+`]}`)
		default:
			_, _ = fmt.Fprint(w, `{"success":true,"data":[]}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetTorrentsIncludesFinishedUsenet(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	tb := testTorbox(usenetServer(t, false).URL)
	got, err := tb.GetTorrents()
	if err != nil {
		t.Fatalf("GetTorrents() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1 (failed usenet job must be skipped)", len(got))
	}
	u := got[0]
	if u.Id != "u2515089" || u.InfoHash != "19028d8f31e388975068b9f11cab4476" || u.OriginalFilename != "Show.S01E01.1080p-GRP" {
		t.Fatalf("unexpected usenet item: id=%s hash=%s folder=%s", u.Id, u.InfoHash, u.OriginalFilename)
	}
	f, ok := u.Files["ebfd37cc.mkv"]
	if !ok || f.Link != "torbox://u2515089/0" {
		t.Fatalf("file link = %#v", u.Files)
	}
}

func TestGetTorrentsFailsWholeRefreshOnUsenetError(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	tb := testTorbox(usenetServer(t, true).URL)
	got, err := tb.GetTorrents()
	if err == nil || got != nil {
		t.Fatalf("want error and nil list on usenet failure, got %v / %d items", err, len(got))
	}
}

func TestUsenetSafetyGuards(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	tb := testTorbox(usenetServer(t, false).URL)
	if _, err := tb.GetTorrents(); err != nil {
		t.Fatal(err)
	}
	if err := tb.DeleteTorrent("u2515089"); err != nil {
		t.Fatalf("usenet delete must be a no-op, got %v", err)
	}
	_, err := tb.SubmitMagnet(&types.Torrent{Name: "x", InfoHash: "19028D8F31E388975068B9F11CAB4476", Magnet: &utilsMagnet})
	if err == nil {
		t.Fatal("SubmitMagnet must refuse a known usenet hash")
	}
	if err := tb.CheckFile(t.Context(), "", "torbox://u2515089/0"); err != nil {
		t.Fatalf("present usenet item should pass CheckFile, got %v", err)
	}
	tr, err := tb.GetTorrent("u2515089")
	if err != nil || tr.Id != "u2515089" {
		t.Fatalf("GetTorrent(u…) = %v, %v", tr, err)
	}
}

func TestUsenetID(t *testing.T) {
	for in, want := range map[string]bool{"u123": true, "123": false, "u": false, "uabc": false} {
		if _, ok := usenetID(in); ok != want {
			t.Errorf("usenetID(%q) = %v, want %v", in, ok, want)
		}
	}
}

var utilsMagnet = utils.Magnet{Link: "magnet:?xt=urn:btih:19028d8f31e388975068b9f11cab4476"}
