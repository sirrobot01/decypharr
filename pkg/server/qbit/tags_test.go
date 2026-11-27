package qbit

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	json "github.com/bytedance/sonic"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func queueTorrent(t *testing.T, q *QBit, hash string, tags ...string) {
	t.Helper()
	entry := &storage.Entry{InfoHash: hash, Name: hash, Protocol: config.ProtocolTorrent, Category: "test", Tags: tags}
	if err := q.manager.Queue().Add(entry); err != nil {
		t.Fatalf("queue %s: %v", hash, err)
	}
}

func infoRows(t *testing.T, q *QBit, target string) []map[string]any {
	t.Helper()
	recorder := httptest.NewRecorder()
	q.handleTorrentsInfo(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("info status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var rows []map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &rows); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	return rows
}

func infoHashes(rows []map[string]any) []string {
	hashes := make([]string, 0, len(rows))
	for _, row := range rows {
		hashes = append(hashes, row["hash"].(string))
	}
	slices.Sort(hashes)
	return hashes
}

func TestTorrentsInfoReportsTags(t *testing.T) {
	q := newAuthenticationTestQBit(t)
	queueTorrent(t, q, "aaa", "one", "two")

	rows := infoRows(t, q, "/api/v2/torrents/info")
	if len(rows) != 1 || rows[0]["tags"] != "one, two" {
		t.Fatalf("info rows = %v, want one row with tags %q", rows, "one, two")
	}
}

func TestTorrentsInfoFiltersByTag(t *testing.T) {
	q := newAuthenticationTestQBit(t)
	queueTorrent(t, q, "aaa", "one")
	queueTorrent(t, q, "bbb", "one", "two")
	queueTorrent(t, q, "ccc")

	tests := map[string]struct {
		target string
		want   []string
	}{
		"one tag":      {"/api/v2/torrents/info?tag=two", []string{"bbb"}},
		"shared tag":   {"/api/v2/torrents/info?tag=one", []string{"aaa", "bbb"}},
		"untagged":     {"/api/v2/torrents/info?tag=", []string{"ccc"}},
		"unknown tag":  {"/api/v2/torrents/info?tag=missing", []string{}},
		"no tag param": {"/api/v2/torrents/info", []string{"aaa", "bbb", "ccc"}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := infoHashes(infoRows(t, q, tt.target)); !slices.Equal(got, tt.want) {
				t.Fatalf("hashes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTorrentsAddAppliesTags(t *testing.T) {
	q := newAuthenticationTestQBit(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	queueTorrent(t, q, hash)

	form := url.Values{
		"urls":     {"magnet:?xt=urn:btih:" + hash},
		"category": {"test"},
		"tags":     {"abc123, other"},
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v2/torrents/add", strings.NewReader(form.Encode()))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	q.handleTorrentsAdd(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("add status = %d, want %d: %s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	entry, err := q.manager.Queue().GetTorrent(hash)
	if err != nil {
		t.Fatalf("get torrent: %v", err)
	}
	if want := []string{"abc123", "other"}; !slices.Equal(entry.Tags, want) {
		t.Fatalf("tags = %q, want %q", entry.Tags, want)
	}
}

func TestTorrentsFilePrio(t *testing.T) {
	q := newAuthenticationTestQBit(t)
	queueTorrent(t, q, "aaa")
	routes := q.Routes()

	tests := map[string]struct {
		form   url.Values
		status int
	}{
		"known torrent":   {url.Values{"hash": {"aaa"}, "id": {"0"}, "priority": {"0"}}, http.StatusOK},
		"unknown torrent": {url.Values{"hash": {"zzz"}, "id": {"0"}, "priority": {"0"}}, http.StatusNotFound},
		"no hash":         {url.Values{"id": {"0"}, "priority": {"0"}}, http.StatusBadRequest},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/torrents/filePrio", strings.NewReader(tt.form.Encode()))
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			recorder := httptest.NewRecorder()
			routes.ServeHTTP(recorder, request)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.status)
			}
		})
	}
}
