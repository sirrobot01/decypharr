package account

import (
	"context"
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func newTestAccount() *Account {
	return &Account{
		Debrid: "premiumize",
		Token:  "token",
		links:  xsync.NewMap[string, types.DownloadLink](),
	}
}

func TestGetDownloadLinkRefetchesExpiredLink(t *testing.T) {
	acc := newTestAccount()
	file := &types.File{Link: "https://cdn.example/stored"}
	acc.storeLink(types.DownloadLink{
		Link:         file.Link,
		DownloadLink: "https://cdn.example/stale",
		ExpiresAt:    time.Now().Add(-time.Minute),
	})

	calls := 0
	fetcher := func(_ context.Context, _ *Account, _ string, f *types.File) (types.DownloadLink, error) {
		calls++
		return types.DownloadLink{
			Link:         f.Link,
			DownloadLink: "https://cdn.example/fresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		}, nil
	}

	dl, err := acc.GetDownloadLink(context.Background(), "id", file, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fetcher called %d times, want 1", calls)
	}
	if dl.DownloadLink != "https://cdn.example/fresh" {
		t.Fatalf("got %q, want the fresh link", dl.DownloadLink)
	}

	// The fresh link is cached: a second lookup must not fetch again.
	if _, err := acc.GetDownloadLink(context.Background(), "id", file, fetcher); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("fetcher called %d times after a cache hit, want 1", calls)
	}
}

func TestGetDownloadLinkKeepsLinkWithoutExpiry(t *testing.T) {
	acc := newTestAccount()
	file := &types.File{Link: "https://cdn.example/stored"}
	acc.storeLink(types.DownloadLink{Link: file.Link, DownloadLink: "https://cdn.example/cached"})

	fetcher := func(context.Context, *Account, string, *types.File) (types.DownloadLink, error) {
		t.Fatal("fetcher must not run for a link with zero ExpiresAt")
		return types.DownloadLink{}, nil
	}
	dl, err := acc.GetDownloadLink(context.Background(), "id", file, fetcher)
	if err != nil {
		t.Fatal(err)
	}
	if dl.DownloadLink != "https://cdn.example/cached" {
		t.Fatalf("got %q, want the cached link", dl.DownloadLink)
	}
}
