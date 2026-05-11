package account

import (
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sourcegraph/conc/pool"
	"go.uber.org/ratelimit"
)

type LinkFetcher func(account *Account, id string, file *types.File) (types.DownloadLink, error)
type LinkDeleter func(account *Account, dl types.DownloadLink) error
type LinksFetcher func(account *Account) ([]types.DownloadLink, error)
type SyncFunc func(account *Account) error

type Manager struct {
	debrid   string
	current  atomic.Pointer[Account]
	accounts *xsync.Map[string, *Account]
	logger   zerolog.Logger
}

func NewManager(debridConf config.Debrid, downloadRL ratelimit.Limiter, logger zerolog.Logger) *Manager {
	m := &Manager{
		debrid:   debridConf.Name,
		accounts: xsync.NewMap[string, *Account](),
		logger:   logger,
	}
	cfg := config.Get()

	var firstAccount *Account
	for idx, token := range debridConf.DownloadAPIKeys {
		if token == "" {
			continue
		}
		headers := map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", token),
		}

		// Create request client with equivalent options
		opts := []request.ClientOption{
			request.WithRateLimiter(downloadRL),
			request.WithHeaders(headers),
			request.WithMaxRetries(cfg.Retries),
			request.WithRetryableStatus(http.StatusTooManyRequests, http.StatusBadGateway, 447),
		}
		if debridConf.Proxy != "" {
			opts = append(opts, request.WithProxy(debridConf.Proxy))
		}

		account := &Account{
			Debrid:     debridConf.Name,
			Token:      token,
			Index:      idx,
			links:      xsync.NewMap[string, types.DownloadLink](),
			httpClient: request.New(opts...),
		}
		m.accounts.Store(token, account)
		if firstAccount == nil {
			firstAccount = account
		}
	}
	m.current.Store(firstAccount)
	return m
}

func (m *Manager) Active() []*Account {
	activeAccounts := make([]*Account, 0)
	m.accounts.Range(func(key string, acc *Account) bool {
		if !acc.Disabled.Load() {
			activeAccounts = append(activeAccounts, acc)
		}
		return true
	})

	slices.SortFunc(activeAccounts, func(i, j *Account) int {
		return i.Index - j.Index
	})
	return activeAccounts
}

func (m *Manager) All() []*Account {
	allAccounts := make([]*Account, 0)
	m.accounts.Range(func(key string, acc *Account) bool {
		allAccounts = append(allAccounts, acc)
		return true
	})

	slices.SortFunc(allAccounts, func(i, j *Account) int {
		return i.Index - j.Index
	})
	return allAccounts
}

func (m *Manager) Current() *Account {
	// Fast path - most common case
	current := m.current.Load()
	if current != nil && !current.Disabled.Load() {
		return current
	}

	// Slow path - find new current account
	activeAccounts := m.Active()
	if len(activeAccounts) == 0 {
		// No active accounts left, try to use disabled ones
		m.logger.Warn().Str("debrid", m.debrid).Msg("No active accounts available, all accounts are disabled, falling back to disabled accounts")
		allAccounts := m.All()
		if len(allAccounts) == 0 {
			m.logger.Error().Str("debrid", m.debrid).Msg("Cannot set current account, no accounts available")
			m.current.Store(nil)
			return nil
		}
		m.current.Store(allAccounts[0])
		return allAccounts[0]
	}

	newCurrent := activeAccounts[0]
	m.current.Store(newCurrent)
	return newCurrent
}

func (m *Manager) Disable(account *Account) {
	if account == nil {
		return
	}

	account.MarkDisabled()

	// If the disabled account is currently in use, refresh the current account to switch to a new active one
	activeAccounts := m.Active()
	if len(activeAccounts) == 0 {
		m.logger.Warn().Str("debrid", m.debrid).Msg("No active accounts available after disabling, all accounts are disabled, falling back to disabled accounts")
		allAccounts := m.All()
		if len(allAccounts) == 0 {
			m.logger.Error().Str("debrid", m.debrid).Msg("Cannot set current account, no accounts available")
			m.current.Store(nil)
			return
		}
		m.current.Store(allAccounts[0])
		return
	}
	// Set current to first active account
	m.current.Store(activeAccounts[0])
}

func (m *Manager) Reset() {
	m.accounts.Range(func(key string, acc *Account) bool {
		acc.Reset()
		return true
	})

	// Set current to first active account
	activeAccounts := m.Active()
	if len(activeAccounts) > 0 {
		m.current.Store(activeAccounts[0])
	} else {
		m.current.Store(nil)
	}
}

func (m *Manager) GetAccount(token string) (*Account, error) {
	if token == "" {
		return nil, fmt.Errorf("token cannot be empty")
	}
	acc, ok := m.accounts.Load(token)
	if !ok {
		return nil, fmt.Errorf("account not found for token")
	}
	return acc, nil
}

func (m *Manager) GetDownloadLink(id string, file *types.File, fetcher LinkFetcher) (types.DownloadLink, error) {
	current := m.Current()
	if current == nil {
		return types.DownloadLink{}, fmt.Errorf("no active account for debrid %s", m.debrid)
	}
	dl, err := current.GetDownloadLink(id, file, fetcher)
	if err != nil {
		activeAccounts := m.Active()
		for _, acc := range activeAccounts {
			if acc.Token == current.Token {
				continue
			}
			dl, err = acc.GetDownloadLink(id, file, fetcher)
			if err != nil {
				continue
			} else {
				// Successfully got link from another account. Just return it, no need to switch current account
				return dl, nil
			}
		}
	}
	return dl, nil
}

func (m *Manager) StoreDownloadLink(downloadLink types.DownloadLink) {
	if downloadLink.Link == "" || downloadLink.Token == "" {
		return
	}
	account, err := m.GetAccount(downloadLink.Token)
	if err != nil || account == nil {
		return
	}
	account.storeLink(downloadLink)
}

func (m *Manager) DeleteDownloadLink(downloadLink types.DownloadLink, deleter LinkDeleter) error {
	if downloadLink.Link == "" || downloadLink.Token == "" {
		return fmt.Errorf("invalid download link")
	}
	account, err := m.GetAccount(downloadLink.Token)
	if err != nil || account == nil {
		return fmt.Errorf("account not found for download link")
	}
	return account.DeleteLink(downloadLink, deleter)
}

func (m *Manager) Stats() []map[string]any {
	stats := make([]map[string]any, 0)

	for _, acc := range m.All() {
		maskedToken := utils.Mask(acc.Token)
		accountDetail := map[string]any{
			"in_use":       acc.Equals(m.Current()),
			"order":        acc.Index,
			"disabled":     acc.Disabled.Load(),
			"token_masked": maskedToken,
			"username":     acc.Username,
			"traffic_used": acc.TrafficUsed.Load(),
			"expiration":   acc.Expiration,
			"links_count":  acc.DownloadLinksCount(),
			"debrid":       acc.Debrid,
		}
		stats = append(stats, accountDetail)
	}
	return stats
}

func (m *Manager) RefreshLinks(fetcher LinksFetcher) error {
	wgPool := pool.New().WithMaxGoroutines(max(1, m.accounts.Size())).WithErrors()
	m.accounts.Range(func(key string, acc *Account) bool {
		wgPool.Go(func() error {
			links, err := fetcher(acc)
			if err != nil {
				m.logger.Error().Err(err).Str("debrid", m.debrid).Str("account_token", utils.Mask(acc.Token)).Msg("Failed to fetch download links for account")
				return err
			}
			for _, dl := range links {
				acc.storeLink(dl)
			}
			return nil
		})
		return true
	})
	return wgPool.Wait()
}

func (m *Manager) Sync(syncer SyncFunc) {
	workers := m.accounts.Size()
	if workers == 0 {
		return
	}
	wgPool := pool.New().WithMaxGoroutines(workers)
	m.accounts.Range(func(key string, acc *Account) bool {
		wgPool.Go(func() {
			if err := syncer(acc); err != nil {
				m.logger.Error().Err(err).Str("debrid", m.debrid).Str("account_token", utils.Mask(acc.Token)).Msg("Failed to sync account")
				return
			}
			// Check if account has expired
			if !acc.Expiration.IsZero() && utils.Now().After(acc.Expiration) {
				m.logger.Warn().Str("debrid", m.debrid).Str("account_token", utils.Mask(acc.Token)).Msg("Account has expired, disabling")
				m.Disable(acc)
			}
			m.UpdateAccount(acc)
		})
		return true
	})
	wgPool.Wait()
}

func (m *Manager) UpdateAccount(updatedAccount *Account) {
	if updatedAccount == nil {
		return
	}
	if updatedAccount.Token == "" {
		return
	}
	m.accounts.Store(updatedAccount.Token, updatedAccount)
}
