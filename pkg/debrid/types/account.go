package types

import (
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

type Accounts struct {
	current  *Account
	accounts []*Account
	mu       sync.RWMutex
}

func NewAccounts(debridConf config.Debrid) *Accounts {
	accounts := make([]*Account, 0)
	for idx, token := range debridConf.DownloadAPIKeys {
		if token == "" {
			continue
		}

		account := newAccount(debridConf.Name, token, idx)
		accounts = append(accounts, account)
	}

	var current *Account
	if len(accounts) > 0 {
		current = accounts[0]
	}

	return &Accounts{
		accounts: accounts,
		current:  current,
	}
}

type Account struct {
	Debrid      string // e.g., "realdebrid", "torbox", etc.
	Order       int
	Disabled    bool
	Token       string `json:"token"`
	links       map[string]*DownloadLink
	mu          sync.RWMutex
	TrafficUsed int64  `json:"traffic_used"` // Traffic used in bytes
	Username    string `json:"username"`     // Username for the account
}

func (a *Accounts) Active() []*Account {
	a.mu.RLock()
	defer a.mu.RUnlock()

	activeAccounts := make([]*Account, 0)
	for _, acc := range a.accounts {
		if !acc.Disabled {
			activeAccounts = append(activeAccounts, acc)
		}
	}

	return activeAccounts
}

func (a *Accounts) All() []*Account {
	a.mu.RLock()
	defer a.mu.RUnlock()

	return a.accounts
}

func (a *Accounts) Current() *Account {
	a.mu.RLock()
	if a.current != nil {
		current := a.current
		a.mu.RUnlock()
		return current
	}
	a.mu.RUnlock()

	a.mu.Lock()
	defer a.mu.Unlock()

	// Double-check after acquiring write lock
	if a.current != nil {
		return a.current
	}

	activeAccounts := make([]*Account, 0)
	for _, acc := range a.accounts {
		if !acc.Disabled {
			activeAccounts = append(activeAccounts, acc)
		}
	}

	if len(activeAccounts) > 0 {
		a.current = activeAccounts[0]
	}

	return a.current
}

func (a *Accounts) Disable(account *Account) {
	a.mu.Lock()
	defer a.mu.Unlock()
	account.disable()

	if a.current == account {
		var newCurrent *Account
		for _, acc := range a.accounts {
			if !acc.Disabled {
				newCurrent = acc
				break
			}
		}
		a.current = newCurrent
	}
}

func (a *Accounts) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, acc := range a.accounts {
		acc.resetDownloadLinks()
		acc.Disabled = false
	}

	if len(a.accounts) > 0 {
		a.current = a.accounts[0]
	} else {
		a.current = nil
	}
}

func (a *Accounts) GetDownloadLink(fileLink string) (*DownloadLink, error) {
	if a.Current() == nil {
		return nil, NoActiveAccountsError
	}

	dl, ok := a.Current().getLink(fileLink)
	if !ok {
		return nil, NoDownloadLinkError
	}

	if dl.ExpiresAt.IsZero() || dl.ExpiresAt.Before(time.Now()) {
		return nil, DownloadLinkExpiredError
	}

	if dl.DownloadLink == "" {
		return nil, EmptyDownloadLinkError
	}

	return dl, nil
}

func (a *Accounts) GetDownloadLinkWithAccount(fileLink string) (*DownloadLink, *Account, error) {
	currentAccount := a.Current()
	if currentAccount == nil {
		return nil, nil, NoActiveAccountsError
	}

	dl, ok := currentAccount.getLink(fileLink)
	if !ok {
		return nil, nil, NoDownloadLinkError
	}

	if dl.ExpiresAt.IsZero() || dl.ExpiresAt.Before(time.Now()) {
		return nil, currentAccount, DownloadLinkExpiredError
	}

	if dl.DownloadLink == "" {
		return nil, currentAccount, EmptyDownloadLinkError
	}

	return dl, currentAccount, nil
}

func (a *Accounts) SetDownloadLink(fileLink string, dl *DownloadLink) {
	if a.Current() == nil {
		return
	}

	a.Current().setLink(fileLink, dl)
}

func (a *Accounts) DeleteDownloadLink(fileLink string) {
	if a.Current() == nil {
		return
	}

	a.Current().deleteLink(fileLink)
}

func (a *Accounts) GetLinksCount() int {
	if a.Current() == nil {
		return 0
	}

	return a.Current().LinksCount()
}

func (a *Accounts) SetDownloadLinks(links map[string]*DownloadLink) {
	if a.Current() == nil {
		return
	}

	a.Current().setLinks(links)
}

func (a *Accounts) Update(index int, account *Account) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if index < 0 || index >= len(a.accounts) {
		return // Index out of bounds
	}

	// Update the account at the specified index
	a.accounts[index] = account

	// If the updated account is the current one, update the current reference
	if a.current == nil || a.current.Order == index {
		a.current = account
	}
}

func newAccount(debridName, token string, index int) *Account {
	return &Account{
		Debrid: debridName,
		Token:  token,
		Order:  index,
		links:  make(map[string]*DownloadLink),
	}
}

func (a *Account) getLink(fileLink string) (*DownloadLink, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	dl, ok := a.links[a.sliceFileLink(fileLink)]
	return dl, ok
}

func (a *Account) setLink(fileLink string, dl *DownloadLink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.links[a.sliceFileLink(fileLink)] = dl
}

func (a *Account) deleteLink(fileLink string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.links, a.sliceFileLink(fileLink))
}

func (a *Account) resetDownloadLinks() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.links = make(map[string]*DownloadLink)
}

func (a *Account) LinksCount() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.links)
}

func (a *Account) disable() {
	a.Disabled = true
}

func (a *Account) setLinks(links map[string]*DownloadLink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	for _, dl := range links {
		if !dl.ExpiresAt.IsZero() && dl.ExpiresAt.Before(now) {
			// Expired, continue
			continue
		}

		a.links[a.sliceFileLink(dl.Link)] = dl
	}
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
