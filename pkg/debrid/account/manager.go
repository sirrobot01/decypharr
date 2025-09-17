package account

import (
	"fmt"
	"slices"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"
)

const (
	MaxDisableCount = 3
)

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

	var firstAccount *Account
	for idx, token := range debridConf.DownloadAPIKeys {
		if token == "" {
			continue
		}
		headers := map[string]string{
			"Authorization": fmt.Sprintf("Bearer %s", token),
		}
		account := &Account{
			Debrid: debridConf.Name,
			Token:  token,
			Index:  idx,
			links:  xsync.NewMap[string, types.DownloadLink](),
			httpClient: request.New(
				request.WithRateLimiter(downloadRL),
				request.WithLogger(logger),
				request.WithHeaders(headers),
				request.WithMaxRetries(3),
				request.WithRetryableStatus(429, 447, 502),
				request.WithProxy(debridConf.Proxy),
			),
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
		m.logger.Warn().Str("debrid", m.debrid).Msg("No active accounts available, all accounts are disabled")
		allAccounts := m.All()
		if len(allAccounts) == 0 {
			m.logger.Error().Str("debrid", m.debrid).Msg("No accounts configured")
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

	// If we're disabling the current account, it will be replaced
	// on the next Current() call - no need to proactively update
	current := m.current.Load()
	if current != nil && current.Token == account.Token {
		// Optional: immediately find replacement
		activeAccounts := m.Active()
		if len(activeAccounts) > 0 {
			m.current.Store(activeAccounts[0])
		} else {
			m.current.Store(nil)
		}
	}
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

func (m *Manager) GetDownloadLink(fileLink string) (types.DownloadLink, error) {
	current := m.Current()
	if current == nil {
		return types.DownloadLink{}, fmt.Errorf("no active account for debrid service %s", m.debrid)
	}
	return current.GetDownloadLink(fileLink)
}

func (m *Manager) GetAccountFromDownloadLink(downloadLink types.DownloadLink) (*Account, error) {
	if downloadLink.Link == "" {
		return nil, fmt.Errorf("cannot get account from empty download link")
	}
	if downloadLink.Token == "" {
		return nil, fmt.Errorf("cannot get account from download link without token")
	}
	return m.GetAccount(downloadLink.Token)
}

func (m *Manager) StoreDownloadLink(downloadLink types.DownloadLink) {
	if downloadLink.Link == "" || downloadLink.Token == "" {
		return
	}
	account, err := m.GetAccount(downloadLink.Token)
	if err != nil || account == nil {
		return
	}
	account.StoreDownloadLink(downloadLink)
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
			"links_count":  acc.DownloadLinksCount(),
			"debrid":       acc.Debrid,
		}
		stats = append(stats, accountDetail)
	}
	return stats
}

func (m *Manager) CheckAndResetBandwidth() {
	found := false
	m.accounts.Range(func(key string, acc *Account) bool {
		if acc.Disabled.Load() && acc.DisableCount.Load() < MaxDisableCount {
			if err := acc.CheckBandwidth(); err == nil {
				acc.Disabled.Store(false)
				found = true
				m.logger.Info().Str("debrid", m.debrid).Str("token", utils.Mask(acc.Token)).Msg("Re-activated disabled account")
			} else {
				m.logger.Debug().Err(err).Str("debrid", m.debrid).Str("token", utils.Mask(acc.Token)).Msg("Account still disabled")
			}
		}
		return true
	})
	if found {
		// If we re-activated any account, reset current to first active
		activeAccounts := m.Active()
		if len(activeAccounts) > 0 {
			m.current.Store(activeAccounts[0])
		}

	}
}
