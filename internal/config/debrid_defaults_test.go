package config

import "testing"

func TestProviderDefaults(t *testing.T) {
	c := &Config{Debrids: []Debrid{{Name: "torbox"}}}
	for _, test := range []struct {
		input       Debrid
		rateLimit   string
		refreshTime string
	}{
		{Debrid{Provider: "torbox"}, "300/minute", "30m"},
		{Debrid{Provider: "debridlink"}, DefaultRateLimit, DefaultTorrentsRefreshInterval},
		{Debrid{Provider: "alldebrid"}, "600/minute", DefaultTorrentsRefreshInterval},
		{Debrid{Provider: "unknown"}, DefaultRateLimit, DefaultTorrentsRefreshInterval},
		{Debrid{Name: "premiumize"}, DefaultRateLimit, DefaultTorrentsRefreshInterval},
		{Debrid{Provider: "torbox", RateLimit: "10/minute", TorrentsRefreshInterval: "1h"}, "10/minute", "1h"},
	} {
		got := c.updateDebrid(test.input)
		if got.RateLimit != test.rateLimit || got.TorrentsRefreshInterval != test.refreshTime {
			t.Errorf("updateDebrid(%+v) = %s, %s, want %s, %s", test.input, got.RateLimit, got.TorrentsRefreshInterval, test.rateLimit, test.refreshTime)
		}
	}
}
