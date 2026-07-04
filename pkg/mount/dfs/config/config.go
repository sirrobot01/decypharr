package config

import (
	"fmt"
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

	// BufferMemory is the RAM budget (bytes) for the DFS streaming-buffer pool,
	// shared across all open files. 0 disables the cap.
	BufferMemory int64

	CacheExpiry time.Duration

	// Performance settings
	ChunkSize     int64
	ReadAheadSize int64
	DaemonTimeout time.Duration

	// DropBehindMargin, when > 0, makes the read path release the disk file's
	// page cache for data more than this many bytes behind the current read
	// offset (keeping the trailing margin resident so readahead/short
	// seek-backs are unaffected, and keeping the bytes on disk so a longer
	// seek-back re-reads locally rather than re-downloading). 0 disables it —
	// the default, since the page cache it trims is reclaimable and only worth
	// dropping under a tight memory cap.
	DropBehindMargin int64

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
		ChunkSize:     4 * 1024 * 1024,  // 4MB chunks (matches beta baseline)
		ReadAheadSize: 16 * 1024 * 1024, // 16MB read-ahead (4 chunks ahead)

		Retries: 3,

		// File system defaults
		UID:   1000,
		GID:   1000,
		Umask: 0022,
	}
}

// ParseFuseConfig converts config.DFS to internal FuseConfig
func ParseFuseConfig() *FuseConfig {
	fuseConfig := DefaultFuseConfig()
	mainCfg := config.Get()
	cfg := mainCfg.Mount.DFS

	fuseConfig.CacheDir = cfg.CacheDir
	fuseConfig.MountPath = mainCfg.Mount.MountPath
	fuseConfig.BufferMemory = cfg.BufferMemoryBytes()

	if cfg.DaemonTimeout != "" {
		timeout, err := utils.ParseDuration(cfg.DaemonTimeout)
		if err == nil {
			fuseConfig.DaemonTimeout = timeout
		}

	}
	if cfg.DiskCacheSize != "" {
		// The DFS mount uses a single shared on-disk cache (one CacheDir, one
		// vfs.Cache), so the configured size is the cache budget verbatim. Do
		// not divide by the number of debrid providers.
		size, err := config.ParseSize(cfg.DiskCacheSize)
		if err == nil {
			fuseConfig.CacheDiskSize = size
		}
	}

	if cfg.CacheCleanupInterval != "" {
		interval, err := utils.ParseDuration(cfg.CacheCleanupInterval)
		if err == nil {
			fuseConfig.CacheCleanupInterval = interval
		}
	}

	if cfg.ChunkSize != "" {
		size, err := config.ParseSize(cfg.ChunkSize)
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
		size, err := config.ParseSize(cfg.ReadAheadSize)
		if err == nil {
			fuseConfig.ReadAheadSize = size
		}
	}
	if cfg.DropBehindMargin != "" {
		size, err := config.ParseSize(cfg.DropBehindMargin)
		if err == nil {
			fuseConfig.DropBehindMargin = size
		}
	}
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
