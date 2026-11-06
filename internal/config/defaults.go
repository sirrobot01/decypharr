package config

import "time"

const (
	DefaultNFSPort uint16 = 20490
	DefaultSMBPort uint16 = 1445
)

var (
	DefaultPort     = "8282"
	DefaultLogLevel = "info"

	DefaultRateLimit                = "250/minute"
	DefaultTorrentsRefreshInterval  = "10m"
	DefaultDownloadsRefreshInterval = "5m"
	DefaultAutoExpireLinksAfter     = "3d"

	DefaultRclonePort = "5572"

	DefaultDFSChunkSize     = "8MB"
	DefaultDFSReadAheadSize = "128MB"
	DefaultDFSCacheExpiry   = "24h"
	DefaultDFSDiskCacheSize = "500MB"

	DefaultAccountSyncInterval = "10m"
	DefaultAvailableSlots      = 100 // This is for providers that does not provide available slots info

	DefaultRetryDelay    = 500 * time.Millisecond
	DefaultRetryDelayMax = 30 * time.Second
)

// providerDefaults holds what differs between provider APIs. A rate limit stays
// below the documented limit, or is cautious where the provider publishes none.
var providerDefaults = map[string]struct {
	rateLimit       string
	refreshInterval string
}{
	"realdebrid": {"250/minute", DefaultTorrentsRefreshInterval},
	// TorBox documents 300/minute per key.
	"torbox": {"240/minute", "30m"},
	// AllDebrid documents 600/minute.
	"alldebrid": {"500/minute", DefaultTorrentsRefreshInterval},
	// Debrid-Link publishes no limit and blocks a key for about an hour.
	"debridlink": {"60/minute", "15m"},
	// Premiumize publishes no limit.
	"premiumize": {"120/minute", DefaultTorrentsRefreshInterval},
}

// ProviderRateLimit returns the default API rate limit for a provider.
func ProviderRateLimit(provider string) string {
	if defaults, ok := providerDefaults[provider]; ok {
		return defaults.rateLimit
	}
	return DefaultRateLimit
}

// ProviderRefreshInterval returns the default torrent list refresh interval for
// a provider.
func ProviderRefreshInterval(provider string) string {
	if defaults, ok := providerDefaults[provider]; ok {
		return defaults.refreshInterval
	}
	return DefaultTorrentsRefreshInterval
}
