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
