package account

import (
	"testing"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func newTestAccount(debrid string) *Account {
	return &Account{
		Debrid: debrid,
		Token:  "test-token",
		links:  xsync.NewMap[string, types.DownloadLink](),
	}
}

func countingFetcher(url string, expiresAt time.Time, calls *int) LinkFetcher {
	return func(_ *Account, _ string, file *types.File) (types.DownloadLink, error) {
		*calls++
		return types.DownloadLink{
			Filename:     file.Name,
			Link:         file.Link,
			DownloadLink: url,
			Debrid:       "torbox",
			Generated:    time.Now(),
			ExpiresAt:    expiresAt,
		}, nil
	}
}

func TestGetDownloadLinkServesCachedLinkThatIsStillValid(t *testing.T) {
	acc := newTestAccount("torbox")
	file := &types.File{Name: "file.mkv", Link: "torbox://1/0"}

	acc.storeLink(types.DownloadLink{
		Link:         file.Link,
		DownloadLink: "https://cdn.example.com/cached",
		Debrid:       "torbox",
		ExpiresAt:    time.Now().Add(time.Hour),
	})

	calls := 0
	dl, err := acc.GetDownloadLink("1", file, countingFetcher("https://cdn.example.com/fresh", time.Now().Add(time.Hour), &calls))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected the cached link to be served without a fetch, got %d fetches", calls)
	}
	if dl.DownloadLink != "https://cdn.example.com/cached" {
		t.Fatalf("expected the cached link, got %q", dl.DownloadLink)
	}
}

func TestGetDownloadLinkEvictsExpiredCachedLink(t *testing.T) {
	acc := newTestAccount("torbox")
	file := &types.File{Name: "file.mkv", Link: "torbox://1/0"}

	acc.storeLink(types.DownloadLink{
		Link:         file.Link,
		DownloadLink: "https://cdn.example.com/expired",
		Debrid:       "torbox",
		Generated:    time.Now().Add(-2 * time.Hour),
		ExpiresAt:    time.Now().Add(-time.Minute),
	})

	calls := 0
	dl, err := acc.GetDownloadLink("1", file, countingFetcher("https://cdn.example.com/fresh", time.Now().Add(time.Hour), &calls))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected exactly one fetch after eviction, got %d", calls)
	}
	if dl.DownloadLink != "https://cdn.example.com/fresh" {
		t.Fatalf("expected the refetched link, got %q", dl.DownloadLink)
	}

	cached, ok := acc.links.Load(acc.sliceFileLink(file.Link))
	if !ok {
		t.Fatal("expected the fresh link to be cached")
	}
	if cached.DownloadLink != "https://cdn.example.com/fresh" {
		t.Fatalf("expected the cache to hold the fresh link, got %q", cached.DownloadLink)
	}
}

// Providers that don't expose an expiry leave ExpiresAt zero; those entries must
// keep their previous never-evicted behaviour.
func TestGetDownloadLinkKeepsCachedLinkWithoutExpiry(t *testing.T) {
	acc := newTestAccount("torbox")
	file := &types.File{Name: "file.mkv", Link: "torbox://1/0"}

	acc.storeLink(types.DownloadLink{
		Link:         file.Link,
		DownloadLink: "https://cdn.example.com/no-expiry",
		Debrid:       "torbox",
		Generated:    time.Now().Add(-72 * time.Hour),
	})

	calls := 0
	dl, err := acc.GetDownloadLink("1", file, countingFetcher("https://cdn.example.com/fresh", time.Time{}, &calls))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("expected no fetch for a link without an expiry, got %d", calls)
	}
	if dl.DownloadLink != "https://cdn.example.com/no-expiry" {
		t.Fatalf("expected the cached link, got %q", dl.DownloadLink)
	}
}

func TestDownloadLinkExpired(t *testing.T) {
	cases := map[string]struct {
		expiresAt time.Time
		want      bool
	}{
		"zero expiry is never expired": {time.Time{}, false},
		"future expiry":                {time.Now().Add(time.Hour), false},
		"past expiry":                  {time.Now().Add(-time.Hour), true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dl := types.DownloadLink{DownloadLink: "https://cdn.example.com/x", ExpiresAt: tc.expiresAt}
			if got := dl.Expired(); got != tc.want {
				t.Fatalf("Expired() = %v, want %v", got, tc.want)
			}
		})
	}
}
