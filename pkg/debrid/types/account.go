package types

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"slices"
	"sync"
	"sync/atomic"
)

type Accounts struct {
	current  atomic.Value
	accounts sync.Map // map[string]*Account // key is token
}

func NewAccounts(debridConf config.Debrid) *Accounts {
	a := &Accounts{
		accounts: sync.Map{},
	}
	var current *Account
	for idx, token := range debridConf.DownloadAPIKeys {
		if token == "" {
			continue
		}
		account := newAccount(debridConf.Name, token, idx)
		a.accounts.Store(token, account)
		if current == nil {
			current = account
		}
	}
	a.setCurrent(current)
	return a
}

type Account struct {
	Debrid      string // e.g., "realdebrid", "torbox", etc.
	Order       int
	Disabled    bool
	InUse       bool
	Token       string `json:"token"`
	links       map[string]*DownloadLink
	mu          sync.RWMutex
	TrafficUsed int64  `json:"traffic_used"` // Traffic used in bytes
	Username    string `json:"username"`     // Username for the account
}

func (a *Accounts) Active() []*Account {
	activeAccounts := make([]*Account, 0)
	a.accounts.Range(func(key, value interface{}) bool {
		acc, ok := value.(*Account)
		if ok && !acc.Disabled {
			activeAccounts = append(activeAccounts, acc)
		}
		return true
	})

	// Sort active accounts by their Order field
	slices.SortFunc(activeAccounts, func(i, j *Account) int {
		return i.Order - j.Order
	})
	return activeAccounts
}

func (a *Accounts) All() []*Account {
	allAccounts := make([]*Account, 0)
	a.accounts.Range(func(key, value interface{}) bool {
		acc, ok := value.(*Account)
		if ok {
			allAccounts = append(allAccounts, acc)
		}
		return true
	})
	// Sort all accounts by their Order field
	slices.SortFunc(allAccounts, func(i, j *Account) int {
		return i.Order - j.Order
	})
	return allAccounts
}

func (a *Accounts) getCurrent() *Account {
	if acc := a.current.Load(); acc != nil {
		if current, ok := acc.(*Account); ok {
			return current
		}
	}
	return nil
}

func (a *Accounts) Current() *Account {
	current := a.getCurrent()
	if current != nil && !current.Disabled {
		return current
	}
	activeAccounts := a.Active()
	if len(activeAccounts) == 0 {
		return current
	}
	current = activeAccounts[0]
	a.setCurrent(current)
	return current
}

func (a *Accounts) setCurrent(account *Account) {
	if account == nil {
		return
	}
	// Set every account InUse to false
	a.accounts.Range(func(key, value interface{}) bool {
		acc, ok := value.(*Account)
		if ok {
			acc.InUse = false
			a.accounts.Store(key, acc)
		}
		return true
	})
	account.InUse = true
	a.current.Store(account)
}

func (a *Accounts) Disable(account *Account) {
	account.Disabled = true
	a.accounts.Store(account.Token, account)

	current := a.getCurrent()

	if current.Equals(account) {
		var newCurrent *Account

		a.accounts.Range(func(key, value interface{}) bool {
			acc, ok := value.(*Account)
			if ok && !acc.Disabled {
				newCurrent = acc
				return false // Break the loop
			}
			return true // Continue the loop
		})
		a.setCurrent(newCurrent)
	}
}

func (a *Accounts) Reset() {
	var current *Account
	a.accounts.Range(func(key, value interface{}) bool {
		acc, ok := value.(*Account)
		if ok {
			acc.resetDownloadLinks()
			acc.Disabled = false
			a.accounts.Store(key, acc)
			if current == nil {
				current = acc
			}

		}
		return true
	})
	a.setCurrent(current)
}

func (a *Accounts) GetDownloadLink(fileLink string) (*DownloadLink, *Account, error) {
	current := a.Current()
	if current == nil {
		return nil, nil, NoActiveAccountsError
	}
	dl, ok := current.getLink(fileLink)
	if !ok {
		return nil, current, NoDownloadLinkError
	}
	if err := dl.Valid(); err != nil {
		return nil, current, err
	}
	return dl, current, nil
}

func (a *Accounts) GetAccountFromLink(fileLink string) (*Account, error) {
	currentAccount := a.Current()
	if currentAccount == nil {
		return nil, NoActiveAccountsError
	}
	dl, ok := currentAccount.getLink(fileLink)
	if !ok {
		return nil, NoDownloadLinkError
	}
	if dl.DownloadLink == "" {
		return currentAccount, EmptyDownloadLinkError
	}
	return currentAccount, nil
}

// SetDownloadLink sets the download link for the current account
func (a *Accounts) SetDownloadLink(account *Account, dl *DownloadLink) {
	if dl == nil {
		return
	}
	if account == nil {
		account = a.getCurrent()
	}
	account.setLink(dl.Link, dl)
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

func (a *Accounts) SetDownloadLinks(account *Account, links map[string]*DownloadLink) {
	if account == nil {
		account = a.Current()
	}
	account.setLinks(links)
	a.accounts.Store(account.Token, account)
}

func (a *Accounts) Update(account *Account) {
	if account == nil {
		return
	}
	a.accounts.Store(account.Token, account)
}

func newAccount(debridName, token string, index int) *Account {
	return &Account{
		Debrid: debridName,
		Token:  token,
		Order:  index,
		links:  make(map[string]*DownloadLink),
	}
}

func (a *Account) Equals(other *Account) bool {
	if other == nil {
		return false
	}
	return a.Token == other.Token && a.Debrid == other.Debrid
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

func (a *Account) setLinks(links map[string]*DownloadLink) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, dl := range links {
		if err := dl.Valid(); err != nil {
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
