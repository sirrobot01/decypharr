package account

import (
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type Account struct {
	Debrid      string                                 `json:"debrid"` // The debrid service name, e.g. "realdebrid"
	links       *xsync.Map[string, types.DownloadLink] // key is the sliced file link
	Index       int                                    `json:"index"` // The index of the account in the config
	Disabled    atomic.Bool                            `json:"disabled"`
	Token       string                                 `json:"token"`
	TrafficUsed atomic.Int64                           `json:"traffic_used"` // Traffic used in bytes
	Username    string                                 `json:"username"`     // Username for the account
	httpClient  *request.Client
	Expiration  time.Time `json:"expiration"`

	// Account reactivation tracking
	DisableCount atomic.Int32 `json:"disable_count"`
}

func (a *Account) Equals(other *Account) bool {
	if other == nil {
		return false
	}
	return a.Token == other.Token && a.Debrid == other.Debrid
}

func (a *Account) Client() *request.Client {
	return a.httpClient
}

// slice download link
func (a *Account) sliceFileLink(fileLink string) string {
	if a.Debrid != "realdebrid" {
		return fileLink
	}
	if len(fileLink) < 39 {
		return fileLink
	}
	return fileLink[0:39]
}

func (a *Account) GetDownloadLink(id string, file *types.File, fetcher LinkFetcher) (types.DownloadLink, error) {
	slicedLink := a.sliceFileLink(file.Link)
	dl, ok := a.links.Load(slicedLink)
	if !ok || (!dl.ExpiresAt.IsZero() && time.Now().After(dl.ExpiresAt)) {
		var err error
		dl, err = fetcher(a, id, file)
		if err != nil {
			return dl, err
		}
		a.storeLink(dl)
	}
	if err := dl.Valid(); err != nil {
		return types.DownloadLink{}, err
	}
	return dl, nil
}

func (a *Account) storeLink(dl types.DownloadLink) {
	slicedLink := a.sliceFileLink(dl.Link)
	a.links.Store(slicedLink, dl)
}
func (a *Account) DeleteLink(link types.DownloadLink, deleter LinkDeleter) error {
	slicedLink := a.sliceFileLink(link.Link)
	a.links.Delete(slicedLink)
	if deleter != nil {
		return deleter(a, link)
	}
	return nil
}
func (a *Account) ClearDownloadLinks() {
	a.links.Clear()
}
func (a *Account) DownloadLinksCount() int {
	return a.links.Size()
}

// GetRandomLink returns any cached download link for speed testing
// Returns empty link if no links are cached
func (a *Account) GetRandomLink() (types.DownloadLink, bool) {
	var result types.DownloadLink
	found := false
	a.links.Range(func(_ string, link types.DownloadLink) bool {
		if !link.Empty() {
			result = link
			found = true
			return false // stop iteration
		}
		return true
	})
	return result, found
}

func (a *Account) StoreDownloadLinks(dls map[string]*types.DownloadLink) {
	for _, dl := range dls {
		a.storeLink(*dl)
	}
}

// MarkDisabled marks the account as disabled and increments the disable count
func (a *Account) MarkDisabled() {
	a.Disabled.Store(true)
	a.DisableCount.Add(1)
}

func (a *Account) Reset() {
	a.DisableCount.Store(0)
	a.Disabled.Store(false)
}
