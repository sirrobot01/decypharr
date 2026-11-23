package link

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// mintingClient counts how many download links the service asks for.
type mintingClient struct {
	debrid.Client
	url     string
	minted  atomic.Int64
	deleted atomic.Int64
}

func (c *mintingClient) GetDownloadLink(context.Context, string, *types.File) (types.DownloadLink, error) {
	c.minted.Add(1)
	return types.DownloadLink{Debrid: "provider", Filename: "file.mkv", Link: "file", DownloadLink: c.url}, nil
}

func (c *mintingClient) DeleteLink(types.DownloadLink) error {
	c.deleted.Add(1)
	return nil
}

func linkEntry() *storage.Entry {
	return &storage.Entry{
		InfoHash:       "hash",
		ActiveProvider: "provider",
		Files:          map[string]*storage.File{"file.mkv": {Name: "file.mkv"}},
		Providers: map[string]*storage.ProviderEntry{
			"provider": {ID: "1", Files: map[string]*storage.ProviderFile{"file.mkv": {Id: "1", Link: "file"}}},
		},
	}
}

func TestThrottledValidationKeepsTheLink(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusBadGateway, 520} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var healthy atomic.Bool
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if healthy.Load() {
					return
				}
				w.Header().Set("Retry-After", "60")
				w.WriteHeader(status)
			}))
			t.Cleanup(cdn.Close)

			client := &mintingClient{url: cdn.URL}
			clients := xsync.NewMap[string, debrid.Client]()
			clients.Store("provider", client)
			service := New(clients, nil, nil, nil, cdn.Client(), 0, zerolog.Nop())

			_, err := service.GetLink(t.Context(), linkEntry(), "file.mkv")
			linkErr := GetLinkError(err)
			if linkErr == nil {
				t.Fatalf("GetLink() error = %v, want a link error", err)
			}
			if status == http.StatusTooManyRequests && linkErr.RetryAfter != time.Minute {
				t.Fatalf("RetryAfter = %s", linkErr.RetryAfter)
			}
			if client.minted.Load() != 1 || client.deleted.Load() != 0 {
				t.Fatalf("minted %d and deleted %d links, want 1 and 0", client.minted.Load(), client.deleted.Load())
			}

			// The failure is not memoised, so the link works once the CDN does.
			healthy.Store(true)
			if _, err := service.GetLink(t.Context(), linkEntry(), "file.mkv"); err != nil {
				t.Fatalf("GetLink() after recovery error = %v", err)
			}
		})
	}
}
