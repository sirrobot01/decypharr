package types

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"sync"
	"time"
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
		account := newAccount(token, idx)
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
	Order    int
	Disabled bool
	Token    string
	links    map[string]*DownloadLink
	mu       sync.RWMutex
}

func (a *Accounts) All() []*Account {
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

func newAccount(token string, index int) *Account {
	return &Account{
		Token: token,
		Order: index,
		links: make(map[string]*DownloadLink),
	}
}

func (a *Account) getLink(fileLink string) (*DownloadLink, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	dl, ok := a.links[fileLink[0:39]]
	return dl, ok
}
func (a *Account) setLink(fileLink string, dl *DownloadLink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.links[fileLink[0:39]] = dl
}
func (a *Account) deleteLink(fileLink string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	delete(a.links, fileLink[0:39])
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
		a.links[dl.Link[0:39]] = dl
	}
}
