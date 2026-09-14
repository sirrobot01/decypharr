package config

import (
	"testing"

	json "github.com/bytedance/sonic"
)

func TestHearsayIsZero(t *testing.T) {
	if !(Hearsay{}).IsZero() {
		t.Fatal("empty Hearsay config should be zero")
	}
	if (Hearsay{Participate: new(false)}).IsZero() {
		t.Fatal("explicit participation should not be zero")
	}
	if (Hearsay{MaxSeededTorrents: 64}).IsZero() {
		t.Fatal("explicit seeded torrent limit should not be zero")
	}
}

func TestHearsaySeededTorrentLimitRoundTrip(t *testing.T) {
	for _, limit := range []int{0, 64} {
		cfg := Config{Hearsay: Hearsay{MaxSeededTorrents: limit}}
		raw, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		var decoded Config
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Hearsay.MaxSeededTorrents != limit {
			t.Fatalf("seeded torrent limit = %d, want %d: %s", decoded.Hearsay.MaxSeededTorrents, limit, raw)
		}
	}
}

func TestHearsaySeededTorrentLimitRequiresRestart(t *testing.T) {
	before := &Config{}
	after := &Config{Hearsay: Hearsay{MaxSeededTorrents: 64}}
	if !before.RequiresRestart(after) {
		t.Fatal("seeded torrent limit change did not require restart")
	}
}

func TestHearsayNetworkDefaults(t *testing.T) {
	if !(Hearsay{}).Participates() || !(Hearsay{}).Publishes() {
		t.Fatal("network participation and publishing should default on")
	}
	if (Hearsay{Participate: new(false)}).Participates() {
		t.Fatal("explicit participation opt-out ignored")
	}
	if (Hearsay{Publish: new(false)}).Publishes() {
		t.Fatal("explicit publishing opt-out ignored")
	}
}

func TestHearsayExplicitOptOutRoundTrip(t *testing.T) {
	cfg := Config{Hearsay: Hearsay{
		Participate: new(false),
		Publish:     new(false),
	}}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Config
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Hearsay.Participate == nil || decoded.Hearsay.Publish == nil ||
		decoded.Hearsay.Participates() || decoded.Hearsay.Publishes() {
		t.Fatalf("network opt-out did not survive round trip: %s", raw)
	}
}

func TestHearsayEnvironment(t *testing.T) {
	t.Setenv("DECYPHARR_HEARSAY__PARTICIPATE", "true")
	t.Setenv("DECYPHARR_HEARSAY__PUBLISH", "true")
	t.Setenv("DECYPHARR_HEARSAY__ADVICE_MODE", "ACTIVE")
	t.Setenv("DECYPHARR_HEARSAY__MIN_SUPPORT", "0.7")
	t.Setenv("DECYPHARR_HEARSAY__MIN_EVIDENCE", "0.4")
	t.Setenv("DECYPHARR_HEARSAY__MIN_SOURCES", "3")
	t.Setenv("DECYPHARR_HEARSAY__MAX_STORAGE_BYTES", "2048")
	t.Setenv("DECYPHARR_HEARSAY__MAX_FEEDS_PER_NAMESPACE", "32")
	t.Setenv("DECYPHARR_HEARSAY__MAX_SEEDED_TORRENTS", "64")
	t.Setenv("DECYPHARR_HEARSAY__FOLLOW", "ed25519:aa, ed25519:bb")

	var cfg Config
	cfg.applyHearsayEnvVars()
	h := cfg.Hearsay
	if h.Participate == nil || h.Publish == nil || !h.Participates() || !h.Publishes() || h.AdviceMode != "active" {
		t.Fatalf("consent and mode = %+v", h)
	}
	if h.MinSupport != 0.7 || h.MinEvidence != 0.4 || h.MinSources != 3 {
		t.Fatalf("policy = %+v", h)
	}
	if h.MaxStorageBytes != 2048 || h.MaxFeedsPerNamespace != 32 || h.MaxSeededTorrents != 64 {
		t.Fatalf("limits = %+v", h)
	}
	if len(h.Follow) != 2 || h.Follow[1] != "ed25519:bb" {
		t.Fatalf("follow = %v", h.Follow)
	}
}

func TestHearsaySeededTorrentLimitEnvironment(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int
	}{
		{value: "0", want: 0},
		{value: "-1", want: -1},
		{value: "invalid", want: 64},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("DECYPHARR_HEARSAY__MAX_SEEDED_TORRENTS", test.value)
			cfg := Config{Hearsay: Hearsay{MaxSeededTorrents: 64}}
			cfg.applyHearsayEnvVars()
			if cfg.Hearsay.MaxSeededTorrents != test.want {
				t.Fatalf("seeded torrent limit = %d, want %d", cfg.Hearsay.MaxSeededTorrents, test.want)
			}
		})
	}
}
