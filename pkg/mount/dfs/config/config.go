package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// FuseConfig holds the simplified configuration for the FUSE filesystem
type FuseConfig struct {
	MountPath string
	CacheDir  string

	// Cache
	CacheDiskSize        int64 // in bytes
	CacheCleanupInterval time.Duration

	CacheExpiry time.Duration

	// Performance settings
	ChunkSize     int64
	ReadAheadSize int64
	DaemonTimeout time.Duration

	Retries int

	// File system settings
	UID                uint32
	GID                uint32
	Umask              uint32
}

// DefaultFuseConfig returns a streaming-optimized default configuration
func DefaultFuseConfig() *FuseConfig {
	return &FuseConfig{
		// Performance defaults optimized for streaming
		DaemonTimeout:        time.Second * 10, // Longer timeout for reliability
		CacheExpiry:          24 * time.Hour,   // Longer cache for popular content
		CacheCleanupInterval: 5 * time.Minute,  // More frequent cleanup
		ChunkSize:            4 * 1024 * 1024,  // 4MB chunk size (balance latency vs throughput)
		ReadAheadSize:        16 * 1024 * 1024, // 16MB read-ahead (4 chunks prefetch)

		Retries: 3,

		// File system defaults
		UID:                1000,
		GID:                1000,
		Umask:              0022,
	}
}

// ParseFuseConfig converts config.DFS to internal FuseConfig
func ParseFuseConfig() *FuseConfig {
	fuseConfig := DefaultFuseConfig()
	mainCfg := config.Get()
	cfg := mainCfg.Mount.DFS
	totalDebrids := len(mainCfg.Debrids)
	if totalDebrids == 0 {
		totalDebrids = 1
	}

	fuseConfig.CacheDir = cfg.CacheDir
	fuseConfig.MountPath = mainCfg.Mount.MountPath

	if cfg.DaemonTimeout != "" {
		timeout, err := utils.ParseDuration(cfg.DaemonTimeout)
		if err == nil {
			fuseConfig.DaemonTimeout = timeout
		}

	}
	if cfg.DiskCacheSize != "" {
		size, err := parseSize(cfg.DiskCacheSize)
		if err == nil {
			fuseConfig.CacheDiskSize = size / int64(totalDebrids)
		}
	}

	if cfg.CacheCleanupInterval != "" {
		interval, err := utils.ParseDuration(cfg.CacheCleanupInterval)
		if err == nil {
			fuseConfig.CacheCleanupInterval = interval
		}
	}

	if cfg.ChunkSize != "" {
		size, err := parseSize(cfg.ChunkSize)
		if err == nil {
			fuseConfig.ChunkSize = size
		}
	}

	if cfg.CacheExpiry != "" {
		ttl, err := utils.ParseDuration(cfg.CacheExpiry)
		if err == nil {
			fuseConfig.CacheExpiry = ttl
		}
	}

	if cfg.ReadAheadSize != "" {
		size, err := parseSize(cfg.ReadAheadSize)
		if err == nil {
			fuseConfig.ReadAheadSize = size
		}
	}
	// Otherwise keep the default (4) from DefaultFuseConfig()
	fuseConfig.UID = cfg.UID
	fuseConfig.GID = cfg.GID

	if cfg.Umask != "" {
		umask, err := parseUmask(cfg.Umask)
		if err == nil {
			fuseConfig.Umask = umask
		}
	}

	// retry settings
	fuseConfig.Retries = mainCfg.Retries

	return fuseConfig
}

// parseUmask parses umask strings like "0022"
func parseUmask(umaskStr string) (uint32, error) {
	var umask uint32
	if _, err := fmt.Sscanf(umaskStr, "%o", &umask); err != nil {
		return 0, fmt.Errorf("invalid umask format: %s", umaskStr)
	}
	return umask, nil
}

func parseSize(sizeStr string) (int64, error) {
	sizeStr = strings.TrimSpace(strings.ToUpper(sizeStr))

	var multiplier int64 = 1
	var numStr string

	switch {
	case strings.HasSuffix(sizeStr, "TB"):
		multiplier = 1024 * 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(sizeStr, "TB")
	case strings.HasSuffix(sizeStr, "GB"):
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(sizeStr, "GB")
	case strings.HasSuffix(sizeStr, "MB"):
		multiplier = 1024 * 1024
		numStr = strings.TrimSuffix(sizeStr, "MB")
	case strings.HasSuffix(sizeStr, "KB"):
		multiplier = 1024
		numStr = strings.TrimSuffix(sizeStr, "KB")
	case strings.HasSuffix(sizeStr, "B"):
		multiplier = 1
		numStr = strings.TrimSuffix(sizeStr, "B")
	default:
		numStr = sizeStr
	}

	num, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size format: %s", sizeStr)
	}

	return num * multiplier, nil
}

// StreamingStats tracks streaming-specific performance metrics
type StreamingStats struct {
	// Network stats
	NetworkRequests   int64
	NetworkBytes      int64
	NetworkErrors     int64
	ConnectionReuse   int64
	PipelinedRequests int64

	// Performance stats
	ReadLatencyMs    float64
	RangeFetches     int64
	CacheHitRate     float64
	PrefetchHitRate  float64
	StreamingLatency float64

	// Streaming quality metrics
	StreamingInterruptions int64
	BufferUnderrunsMs      int64
	SeekOperations         int64
	ConcurrentStreams      int64
}

type Stats struct {
	// Network stats
	NetworkRequests int64
	NetworkBytes    int64
	NetworkErrors   int64

	// Performance stats
	ReadLatencyMs float64
	RangeFetches  int64
}
