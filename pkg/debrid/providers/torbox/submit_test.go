package torbox

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const testHash = "81618a3711b5dd4c9374e5874a743fef9e838644"

// fakeTorbox answers checkcached and createtorrent and counts calls to each.
type fakeTorbox struct {
	mu             sync.Mutex
	checks         int
	creates        int
	cached         bool
	checkStatus    int
	createStatus   int
	retryAfter     string
	lastCreateForm map[string]string
}

func (f *fakeTorbox) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/torrents/checkcached":
			f.checks++
			if f.checkStatus != 0 {
				w.WriteHeader(f.checkStatus)
				return
			}
			if f.cached {
				_, _ = fmt.Fprintf(w, `{"success":true,"data":{"%s":{"name":"Release","size":100,"hash":"%s"}}}`, testHash, testHash)
				return
			}
			_, _ = fmt.Fprint(w, `{"success":true,"data":{}}`)
		case "/api/torrents/createtorrent":
			f.creates++
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse form: %v", err)
			}
			f.lastCreateForm = map[string]string{}
			for k := range r.PostForm {
				f.lastCreateForm[k] = r.PostForm.Get(k)
			}
			if f.createStatus == http.StatusTooManyRequests {
				if f.retryAfter != "" {
					w.Header().Set("Retry-After", f.retryAfter)
				}
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = fmt.Fprint(w, `{"success":false,"error":"RATE_LIMIT_EXCEEDED","detail":"60 per 1 hour","data":null}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"success":true,"data":{"torrent_id":42,"hash":"`+testHash+`"}}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// start runs fake behind a test server, with config pointed at a temp dir.
func (f *fakeTorbox) start(t *testing.T) string {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	server := httptest.NewServer(f.handler(t))
	t.Cleanup(server.Close)
	return server.URL
}

func (f *fakeTorbox) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks, f.creates
}

// retryingTorbox mirrors production: retries enabled and 429 retryable, so a
// test proves createtorrent is sent once regardless.
func retryingTorbox(host string) *Torbox {
	return &Torbox{
		Host: host,
		client: request.New(
			request.WithMaxRetries(3),
			request.WithRetryableStatus(http.StatusTooManyRequests, http.StatusBadGateway),
		),
		logger: zerolog.Nop(),
		config: config.Debrid{Name: "torbox"},
	}
}

func testTorrent(downloadUncached bool) *types.Torrent {
	return &types.Torrent{
		InfoHash:         testHash,
		Magnet:           &utils.Magnet{InfoHash: testHash, Link: "magnet:?xt=urn:btih:" + testHash},
		DownloadUncached: downloadUncached,
	}
}

func TestSubmitMagnetSkipsUncachedWithoutCreating(t *testing.T) {
	fake := &fakeTorbox{cached: false}
	host := fake.start(t)

	_, err := testTorbox(host).SubmitMagnet(testTorrent(false))
	if err == nil || !strings.Contains(err.Error(), "not cached") {
		t.Fatalf("SubmitMagnet() error = %v, want not cached", err)
	}
	if checks, creates := fake.counts(); checks != 1 || creates != 0 {
		t.Fatalf("checks=%d creates=%d, want 1 and 0", checks, creates)
	}
}

func TestSubmitMagnetAddsCached(t *testing.T) {
	fake := &fakeTorbox{cached: true}
	host := fake.start(t)

	got, err := testTorbox(host).SubmitMagnet(testTorrent(false))
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if got.Id != "42" {
		t.Fatalf("SubmitMagnet() id = %q, want 42", got.Id)
	}
	if fake.lastCreateForm["add_only_if_cached"] != "true" {
		t.Fatalf("add_only_if_cached = %q, want true", fake.lastCreateForm["add_only_if_cached"])
	}
}

func TestSubmitMagnetFallsBackWhenCacheCheckFails(t *testing.T) {
	fake := &fakeTorbox{checkStatus: http.StatusInternalServerError}
	host := fake.start(t)

	got, err := testTorbox(host).SubmitMagnet(testTorrent(false))
	if err != nil {
		t.Fatalf("SubmitMagnet() error = %v, want fallback add", err)
	}
	if got.Id != "42" {
		t.Fatalf("SubmitMagnet() id = %q, want 42", got.Id)
	}
	if _, creates := fake.counts(); creates != 1 {
		t.Fatalf("creates = %d, want 1", creates)
	}
}

func TestSubmitMagnetDownloadUncachedSkipsCacheCheck(t *testing.T) {
	fake := &fakeTorbox{cached: false}
	host := fake.start(t)

	if _, err := testTorbox(host).SubmitMagnet(testTorrent(true)); err != nil {
		t.Fatalf("SubmitMagnet() error = %v", err)
	}
	if checks, creates := fake.counts(); checks != 0 || creates != 1 {
		t.Fatalf("checks=%d creates=%d, want 0 and 1", checks, creates)
	}
	if _, ok := fake.lastCreateForm["add_only_if_cached"]; ok {
		t.Fatal("add_only_if_cached sent for an uncached-allowed torrent")
	}
}

func TestSubmitMagnetRateLimitSendsOnceAndPauses(t *testing.T) {
	fake := &fakeTorbox{cached: true, createStatus: http.StatusTooManyRequests, retryAfter: "120"}
	host := fake.start(t)

	tb := retryingTorbox(host)
	start := time.Now()
	_, err := tb.SubmitMagnet(testTorrent(false))
	if err == nil || !strings.Contains(err.Error(), "add limit reached") {
		t.Fatalf("SubmitMagnet() error = %v, want add limit reached", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("SubmitMagnet() took %s, want no retry backoff", elapsed)
	}
	if _, creates := fake.counts(); creates != 1 {
		t.Fatalf("creates = %d, want exactly 1 (no retries)", creates)
	}

	until := tb.addsPausedUntil()
	if wait := time.Until(until); wait < 110*time.Second || wait > 125*time.Second {
		t.Fatalf("paused for %s, want about 120s from Retry-After", wait)
	}

	// A second add during the pause never reaches TorBox.
	_, err = tb.SubmitMagnet(testTorrent(false))
	if err == nil || !strings.Contains(err.Error(), "paused until") {
		t.Fatalf("second SubmitMagnet() error = %v, want paused", err)
	}
	if checks, creates := fake.counts(); checks != 1 || creates != 1 {
		t.Fatalf("checks=%d creates=%d after pause, want 1 and 1", checks, creates)
	}
}

func TestPauseAddsDefaultsWithoutRetryAfter(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	tb := testTorbox("http://unused")
	until := tb.pauseAdds(&http.Response{Header: http.Header{}})
	if wait := time.Until(until); wait < defaultAddLimitPause-5*time.Second || wait > defaultAddLimitPause {
		t.Fatalf("paused for %s, want about %s", wait, defaultAddLimitPause)
	}
}

func TestPauseAddsCapsLargeRetryAfter(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	tb := testTorbox("http://unused")
	until := tb.pauseAdds(&http.Response{Header: http.Header{"Retry-After": []string{"86400"}}})
	if wait := time.Until(until); wait > maxAddLimitPause {
		t.Fatalf("paused for %s, want at most %s", wait, maxAddLimitPause)
	}
}
