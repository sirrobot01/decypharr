package account

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func newTestManager(logOutput *bytes.Buffer) (*Manager, *Account) {
	acc := &Account{
		Debrid: "realdebrid",
		Token:  "token",
		links:  xsync.NewMap[string, types.DownloadLink](),
	}
	accounts := xsync.NewMap[string, *Account]()
	accounts.Store(acc.Token, acc)
	m := &Manager{
		debrid:   acc.Debrid,
		accounts: accounts,
		logger:   zerolog.New(logOutput),
	}
	m.current.Store(acc)
	return m, acc
}

func TestSyncReenablesHealthyDisabledAccount(t *testing.T) {
	var logs bytes.Buffer
	m, acc := newTestManager(&logs)
	m.Disable(acc)

	m.Sync(func(*Account) error { return nil })

	if acc.Disabled.Load() {
		t.Fatal("expected a successful sync to re-enable the account")
	}
	if got := m.Current(); got != acc {
		t.Fatalf("expected the recovered account to be current, got %#v", got)
	}
}

func TestSyncKeepsAccountDisabledOnFailure(t *testing.T) {
	var logs bytes.Buffer
	m, acc := newTestManager(&logs)
	m.Disable(acc)

	m.Sync(func(*Account) error { return errors.New("temporary failure") })

	if !acc.Disabled.Load() {
		t.Fatal("expected a failed sync to leave the account disabled")
	}
}

func TestNoActiveAccountWarningIsThrottled(t *testing.T) {
	var logs bytes.Buffer
	m, acc := newTestManager(&logs)
	m.Disable(acc)

	for range 10 {
		_ = m.Current()
	}

	if got := strings.Count(logs.String(), "No active accounts"); got != 1 {
		t.Fatalf("expected one no-active-account warning, got %d: %s", got, logs.String())
	}
}
