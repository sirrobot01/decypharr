package config

import "strconv"

type Rclone struct {
	// Global mount folder where all providers will be mounted as subfolders
	Enabled   bool   `json:"enabled,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	Port      string `json:"port,omitempty"`
	// Cache settings
	CacheDir string `json:"cache_dir,omitempty"`
	// VFS settings
	VfsCacheMode          string `json:"vfs_cache_mode,omitempty"`            // off, minimal, writes, full
	VfsCacheMaxAge        string `json:"vfs_cache_max_age,omitempty"`         // Maximum age of objects in the cache (default 1h)
	VfsDiskSpaceTotal     string `json:"vfs_disk_space_total,omitempty"`      // Total disk space available for the cache (default off)
	VfsCacheMaxSize       string `json:"vfs_cache_max_size,omitempty"`        // Maximum size of the cache (default off)
	VfsCachePollInterval  string `json:"vfs_cache_poll_interval,omitempty"`   // How often to poll for changes (default 1m)
	VfsReadChunkSize      string `json:"vfs_read_chunk_size,omitempty"`       // Read chunk size (default 128M)
	VfsReadChunkSizeLimit string `json:"vfs_read_chunk_size_limit,omitempty"` // Max chunk size (default off)
	VfsReadAhead          string `json:"vfs_read_ahead,omitempty"`            // read ahead size
	BufferSize            string `json:"buffer_size,omitempty"`               // Buffer size for reading files (default 16M)
	BwLimit               string `json:"bw_limit,omitempty"`                  // Bandwidth limit (default off)

	VfsCacheMinFreeSpace string `json:"vfs_cache_min_free_space,omitempty"`
	VfsFastFingerprint   bool   `json:"vfs_fast_fingerprint,omitempty"`
	VfsReadChunkStreams  int    `json:"vfs_read_chunk_streams,omitempty"`
	AsyncRead            *bool  `json:"async_read,omitempty"` // Use async read for files
	Transfers            int    `json:"transfers,omitempty"`  // Number of transfers to use (default 4)
	UseMmap              bool   `json:"use_mmap,omitempty"`

	// File system settings
	UID   uint32 `json:"uid,omitempty"` // User ID for mounted files
	GID   uint32 `json:"gid,omitempty"` // Group ID for mounted files
	Umask string `json:"umask,omitempty"`

	// Timeout settings
	AttrTimeout  string `json:"attr_timeout,omitempty"`   // Attribute cache timeout (default 1s)
	DirCacheTime string `json:"dir_cache_time,omitempty"` // Directory cache time (default 5m)

	// Performance settings
	NoModTime  bool `json:"no_modtime,omitempty"`  // Don't read/write modification time
	NoChecksum bool `json:"no_checksum,omitempty"` // Don't checksum files on upload

	LogLevel string `json:"log_level,omitempty"`
}

func (r Rclone) IsZero() bool {
	return !r.Enabled && r.MountPath == "" && r.Port == "" && r.CacheDir == ""
}

type DFS struct {
	// Core settings
	CacheExpiry          string `json:"cache_expiry,omitempty"`           // 1h, 30m etc
	CacheDir             string `json:"cache_dir,omitempty"`              // /tmp/decypharr-cache
	DiskCacheSize        string `json:"disk_cache_size,omitempty"`        // 10GB, 50GB etc
	CacheCleanupInterval string `json:"cache_cleanup_interval,omitempty"` // 10m, 1h etc

	// Performance settings
	ChunkSize     string `json:"chunk_size,omitempty"`      // Initial chunk size, e.g 10MB
	ReadAheadSize string `json:"read_ahead_size,omitempty"` // Read ahead size (deprecated, use MaxChunkSize)

	DaemonTimeout string `json:"daemon_timeout,omitempty"` // Time after which the FUSE daemon will exit if idle

	// File system settings
	UID                uint32 `json:"uid,omitempty"`                 // User ID for mounted files
	GID                uint32 `json:"gid,omitempty"`                 // Group ID for mounted files
	Umask              string `json:"umask,omitempty"`               // File permissions mask
}

type ExternalRclone struct {
	RCUrl      string `json:"rc_url,omitempty"`
	RCUsername string `json:"rc_username,omitempty"`
	RCPassword string `json:"rc_password,omitempty"`
}

type Mount struct {
	Type      MountType `json:"type,omitempty"`
	MountPath string    `json:"mount_path,omitempty"`

	Rclone         Rclone         `json:"rclone,omitempty"`
	DFS            DFS            `json:"dfs,omitempty"`
	ExternalRclone ExternalRclone `json:"external_rclone,omitempty"`
}

func (c *Config) applyMountEnvVars() {
	// DFS settings
	if val := getEnv("MOUNT__DFS__CACHE_DIR"); val != "" {
		c.Mount.DFS.CacheDir = val
	}
	if val := getEnv("MOUNT__DFS__CHUNK_SIZE"); val != "" {
		c.Mount.DFS.ChunkSize = val
	}
	if val := getEnv("MOUNT__DFS__READ_AHEAD_SIZE"); val != "" {
		c.Mount.DFS.ReadAheadSize = val
	}
	if val := getEnv("MOUNT__DFS__CACHE_EXPIRY"); val != "" {
		c.Mount.DFS.CacheExpiry = val
	}
	if val := getEnv("MOUNT__DFS__DISK_CACHE_SIZE"); val != "" {
		c.Mount.DFS.DiskCacheSize = val
	}
	if val := getEnv("MOUNT__DFS__CACHE_CLEANUP_INTERVAL"); val != "" {
		c.Mount.DFS.CacheCleanupInterval = val
	}
	if val := getEnv("MOUNT__DFS__DAEMON_TIMEOUT"); val != "" {
		c.Mount.DFS.DaemonTimeout = val
	}
	if val := getEnv("MOUNT__DFS__UID"); val != "" {
		if v, err := strconv.ParseUint(val, 10, 32); err == nil {
			c.Mount.DFS.UID = uint32(v)
		}
	}
	if val := getEnv("MOUNT__DFS__GID"); val != "" {
		if v, err := strconv.ParseUint(val, 10, 32); err == nil {
			c.Mount.DFS.GID = uint32(v)
		}
	}
	if val := getEnv("MOUNT__DFS__UMASK"); val != "" {
		c.Mount.DFS.Umask = val
	}
	// Rclone settings
	if val := getEnv("RCLONE__RC_PORT"); val != "" {
		c.Mount.Rclone.Port = val
	}
	if val := getEnv("RCLONE__LOG_LEVEL"); val != "" {
		c.Mount.Rclone.LogLevel = val
	}
	if val := getEnv("RCLONE__VFS_CACHE_MODE"); val != "" {
		c.Mount.Rclone.VfsCacheMode = val
	}
	if val := getEnv("RCLONE__CACHE_DIR"); val != "" {
		c.Mount.Rclone.CacheDir = val
	}
	if val := getEnv("RCLONE__TRANSFERS"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			c.Mount.Rclone.Transfers = v
		}
	}
}
