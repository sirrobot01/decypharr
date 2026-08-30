package config

import (
	"path/filepath"
	"time"
)

// ShareCache configures an optional on-disk read cache in front of the NFS and
// SMB servers. One cache serves both: they export the same catalog, and a
// second instance over the same directory would fight the first for the same
// files.
//
// The cache exists to reshape backend traffic, not to hold whole files. A
// client that seeks, or a media scanner that re-reads the same headers on
// every pass, otherwise costs a fresh debrid session each time. Behind the
// cache the backend sees one sequential stream per file, so a modest budget
// removes most of the traffic.
//
// It is off by default: it claims disk the operator did not ask for, and the
// budget is only advisory on a filesystem that cannot punch holes. Turn it on
// deliberately, on a filesystem that can spare the space.
type ShareCache struct {
	// Enabled turns the cache on. Nil or false streams straight from the
	// debrid with one session per open file.
	Enabled *bool `json:"enabled,omitempty"`

	// Dir holds the cached content. Empty = <config dir>/share-cache.
	Dir string `json:"dir,omitempty"`

	// MaxSize bounds cached content on disk, e.g. "20GB". Empty = 10GB. The
	// bound is soft against files a client currently holds open: the cache
	// reclaims inside them by punching holes, which some filesystems refuse.
	MaxSize string `json:"max_size,omitempty"`

	// MaxAge drops content nothing has read for this long, e.g. "24h".
	// Empty = 24h.
	MaxAge string `json:"max_age,omitempty"`

	// ChunkSize is the base backend fetch size, e.g. "4MB". It doubles up to
	// 16x while a stream stays sequential. Empty = 4MB.
	ChunkSize string `json:"chunk_size,omitempty"`

	// ReadAhead is fetched beyond each read, e.g. "16MB". Empty = 16MB.
	ReadAhead string `json:"read_ahead,omitempty"`
}

func (s ShareCache) IsZero() bool {
	return s.Enabled == nil && s.Dir == "" && s.MaxSize == "" && s.MaxAge == "" &&
		s.ChunkSize == "" && s.ReadAhead == ""
}

// IsEnabled reports whether to wrap the export in the cache. Unset = disabled.
func (s ShareCache) IsEnabled() bool {
	return s.Enabled != nil && *s.Enabled
}

// MaxSizeBytes, MaxAgeDuration, ChunkSizeBytes, and ReadAheadBytes resolve the
// configured values. Each returns zero when unset or unparseable, which the
// cache reads as "use the package default".

func (s ShareCache) MaxSizeBytes() int64   { return parseSizeOrZero(s.MaxSize) }
func (s ShareCache) ChunkSizeBytes() int64 { return parseSizeOrZero(s.ChunkSize) }
func (s ShareCache) ReadAheadBytes() int64 { return parseSizeOrZero(s.ReadAhead) }

func (s ShareCache) MaxAgeDuration() time.Duration {
	d, err := time.ParseDuration(s.MaxAge)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

func parseSizeOrZero(v string) int64 {
	if v == "" {
		return 0
	}
	n, err := ParseSize(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// setShareCacheDefaults fills the cache directory once the cache is turned on.
// A disabled cache keeps an empty section, so an operator who never enables it
// does not gain configuration for a feature they do not use.
func (c *Config) setShareCacheDefaults() {
	if !c.ShareCache.IsEnabled() || (!c.NFS.Enabled && !c.SMB.Enabled) {
		return
	}
	if c.ShareCache.Dir == "" {
		c.ShareCache.Dir = filepath.Join(GetMainPath(), "share-cache")
	}
}
