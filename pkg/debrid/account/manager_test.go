package account

import (
	"bytes"
	"context"
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

func TestDownloadLinkPropagatesFailuresAndCancellation(t *testing.T) {
	for _, scenario := range []string{"all fail", "cancel before", "cancel during"} {
		t.Run(scenario, func(t *testing.T) {
			var logs bytes.Buffer
			m, first := newTestManager(&logs)
			second := &Account{Token: "second", Debrid: first.Debrid, links: xsync.NewMap[string, types.DownloadLink]()}
			m.accounts.Store(second.Token, second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "cancel before" {
				cancel()
			}
			firstErr, secondErr := errors.New("first failed"), errors.New("second failed")
			calls := 0
			_, err := m.GetDownloadLink(ctx, "id", &types.File{Link: "file"}, func(got context.Context, acc *Account, _ string, _ *types.File) (types.DownloadLink, error) {
				calls++
				if got != ctx {
					t.Error("caller context was replaced")
				}
				if scenario == "cancel during" {
					cancel()
					return types.DownloadLink{}, ctx.Err()
				}
				if acc == first {
					return types.DownloadLink{}, firstErr
				}
				return types.DownloadLink{}, secondErr
			})
			if scenario == "all fail" {
				if calls != 2 || !errors.Is(err, firstErr) || !errors.Is(err, secondErr) {
					t.Fatalf("calls=%d error=%v, want both failures", calls, err)
				}
			} else {
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("error=%v, want context.Canceled", err)
				}
				wantCalls := 1
				if scenario == "cancel before" {
					wantCalls = 0
				}
				if calls != wantCalls {
					t.Fatalf("fetch calls=%d, want %d", calls, wantCalls)
				}
			}
		})
	}
}
