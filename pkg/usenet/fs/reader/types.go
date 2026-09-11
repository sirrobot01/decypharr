// Package reader provides seekable, prefetched access to Usenet segments.
package reader

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

// SegmentMeta holds metadata for a single Usenet segment.
// This is a simplified view of the segment data needed for downloading and caching.
type SegmentMeta struct {
	MessageID string // NNTP message ID (e.g., "<abc123@news.example.com>")
	Number    int    // Segment number within the file (1-indexed from NZB)
	Bytes     int64  // Expected decoded size of the segment

	// Byte offsets within the virtual file
	StartOffset int64 // Inclusive start offset
	EndOffset   int64 // Inclusive end offset

	// yEnc decoding hints
	SegmentDataStart int64 // Offset within decoded data where actual file data begins
}

// NewSegmentMeta creates a SegmentMeta from a storage.NZBSegment.
func NewSegmentMeta(seg storage.NZBSegment) SegmentMeta {
	return SegmentMeta{
		MessageID:        seg.MessageID,
		Number:           seg.Number,
		Bytes:            seg.Bytes,
		StartOffset:      seg.StartOffset,
		EndOffset:        seg.EndOffset,
		SegmentDataStart: seg.SegmentDataStart,
	}
}

// NewSegmentMetaSlice converts a slice of storage.NZBSegment to []SegmentMeta.
func NewSegmentMetaSlice(segments []storage.NZBSegment) []SegmentMeta {
	result := make([]SegmentMeta, len(segments))
	for i, seg := range segments {
		result[i] = NewSegmentMeta(seg)
	}
	return result
}

// SegmentState represents the cache state of a segment.
type SegmentState uint32

const (
	// StateEmpty indicates the segment has no cached data.
	StateEmpty SegmentState = iota

	// StateOnDisk indicates the segment is ready in its configured storage tier.
	StateOnDisk

	// StateFetching indicates the segment is currently being downloaded.
	StateFetching

	// StateFailed indicates the segment download failed permanently.
	StateFailed

	// StateEvicting reserves a resident extent until its pointer is unpublished.
	StateEvicting
)

func (s SegmentState) String() string {
	switch s {
	case StateEmpty:
		return "Empty"
	case StateOnDisk:
		return "OnDisk"
	case StateFetching:
		return "Fetching"
	case StateFailed:
		return "Failed"
	case StateEvicting:
		return "Evicting"
	default:
		return "Unknown"
	}
}

// Retention selects who is responsible for rewind data.
type Retention uint8

const (
	// RetentionWindow keeps only the active delivery window. Use it when a
	// caller wants bounded in-memory rewind without a persistent disk tier.
	RetentionWindow Retention = iota
	// RetentionRewind adds a sparse disk tier so consumed data remains locally
	// readable without another NNTP request.
	RetentionRewind
	// RetentionDelivery is a window-mode reader underneath a persistent
	// downstream cache such as DFS. The downstream acknowledges ranges after
	// copying them, allowing their duplicate Usenet extents to be released.
	RetentionDelivery
)

// Config holds configuration for StreamingReader.
type Config struct {
	// DiskPath is the base directory for disk cache (default: system temp dir).
	DiskPath string

	// Retention declares whether this reader or its downstream owns rewind.
	Retention Retention

	// Scheduler is shared by all readers using the same NNTP client. When nil,
	// a private scheduler is created for compatibility with standalone readers.
	Scheduler *FetchScheduler

	// MaxConnections is the private-scheduler width for standalone readers.
	MaxConnections int

	// PrefetchAhead is the number of segments to prefetch ahead of reads (default: 8).
	PrefetchAhead int

	// DownloadTimeout is the timeout for a single segment download (default: 60s).
	DownloadTimeout time.Duration

	// MaxRetries is the maximum retry attempts for failed downloads (default: 3).
	MaxRetries int

	// RetryDelay is the delay between retry attempts (default: 1s).
	RetryDelay time.Duration
}

// DefaultConfig returns a ReaderConfig with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxConnections:  8,
		PrefetchAhead:   8,
		DownloadTimeout: 60 * time.Second,
		MaxRetries:      3,
		RetryDelay:      time.Second,
		Retention:       RetentionWindow,
	}
}

// PrefetchAheadSegments converts a byte-based read-ahead size (from
// config.Usenet.ReadAhead) into a segment count for the given segments.
// This is what makes the configured read-ahead actually take effect — the
// window was previously hardcoded to 8 segments (~6MB) regardless of config,
// which was far too shallow to absorb provider jitter during playback. A zero
// size explicitly disables read-ahead for parsing and other probe-style reads.
func PrefetchAheadSegments(readAheadBytes int64, segments []SegmentMeta) int {
	const (
		fallbackSegBytes = 750 * 1024 // typical usenet segment
		minAhead         = 16
		maxAhead         = 256 // matches the prefetch channel depth
	)
	if readAheadBytes <= 0 {
		return 0
	}
	segBytes := int64(fallbackSegBytes)
	if len(segments) > 0 && segments[0].Bytes > 0 {
		segBytes = segments[0].Bytes
	}
	ahead := max(int(readAheadBytes/segBytes), minAhead)
	if ahead > maxAhead {
		ahead = maxAhead
	}
	return ahead
}

// Option is a functional option for configuring StreamingReader.
type Option func(*Config)

// WithDiskPath sets the base directory for disk cache.
func WithDiskPath(path string) Option {
	return func(c *Config) {
		c.DiskPath = path
	}
}

// WithMemoryBuffer is the compatibility spelling for WithRetention.
func WithMemoryBuffer(on bool) Option {
	return func(c *Config) {
		if on {
			c.Retention = RetentionWindow
		} else {
			c.Retention = RetentionRewind
		}
	}
}

// WithRetention declares whether this reader or its downstream owns rewind.
func WithRetention(retention Retention) Option {
	return func(c *Config) {
		c.Retention = retention
	}
}

// WithFetchScheduler shares provider concurrency across multiple readers.
func WithFetchScheduler(scheduler *FetchScheduler) Option {
	return func(c *Config) {
		c.Scheduler = scheduler
	}
}

// WithMaxConnections sets the private-scheduler width for standalone readers.
func WithMaxConnections(n int) Option {
	return func(c *Config) {
		c.MaxConnections = n
	}
}

// WithPrefetchAhead sets the number of segments to prefetch ahead of reads.
func WithPrefetchAhead(n int) Option {
	return func(c *Config) {
		c.PrefetchAhead = n
	}
}

// WithDownloadTimeout sets the timeout for a single segment download.
func WithDownloadTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.DownloadTimeout = d
	}
}

// EncryptionConfig holds encryption parameters for decrypting segment data.
type EncryptionConfig struct {
	// Enabled indicates whether encryption is active.
	Enabled bool

	// Key is the AES-256 encryption key (32 bytes).
	Key []byte

	// IV is the initial vector for CBC mode (16 bytes).
	IV []byte
}

// ReaderStats holds statistics for monitoring reader performance.
type ReaderStats struct {
	// Read operations
	Reads      atomic.Int64
	BytesRead  atomic.Int64
	ReadErrors atomic.Int64

	// Cache performance
	CacheHits   atomic.Int64
	CacheMisses atomic.Int64
	Evictions   atomic.Int64
	// DeliveryReleases count resident extents handed off to a downstream
	// cache, and the backing-array capacity reclaimed with them.
	DeliveryReleases atomic.Int64
	DeliveryBytes    atomic.Int64

	// Downloads
	Downloads       atomic.Int64
	DownloadBytes   atomic.Int64
	DownloadRetries atomic.Int64
	DownloadErrors  atomic.Int64

	// Prefetch
	PrefetchHits      atomic.Int64
	PrefetchMisses    atomic.Int64
	PrefetchCancelled atomic.Int64 // hints dropped because a seek abandoned their window
}

// Snapshot returns a copy of the current stats.
func (s *ReaderStats) Snapshot() map[string]int64 {
	return map[string]int64{
		"reads":              s.Reads.Load(),
		"bytes_read":         s.BytesRead.Load(),
		"read_errors":        s.ReadErrors.Load(),
		"cache_hits":         s.CacheHits.Load(),
		"cache_misses":       s.CacheMisses.Load(),
		"evictions":          s.Evictions.Load(),
		"delivery_releases":  s.DeliveryReleases.Load(),
		"delivery_bytes":     s.DeliveryBytes.Load(),
		"downloads":          s.Downloads.Load(),
		"download_bytes":     s.DownloadBytes.Load(),
		"download_retries":   s.DownloadRetries.Load(),
		"download_errors":    s.DownloadErrors.Load(),
		"prefetch_hits":      s.PrefetchHits.Load(),
		"prefetch_misses":    s.PrefetchMisses.Load(),
		"prefetch_cancelled": s.PrefetchCancelled.Load(),
	}
}

// PrefetchableReaderAt extends io.ReaderAt with prefetch capability.
// This allows callers to trigger segment downloads before starting reads.
type PrefetchableReaderAt interface {
	// ReadAt reads len(p) bytes from the reader starting at offset off.
	// Blocks until the data is available or an error occurs.
	ReadAt(p []byte, off int64) (n int, err error)

	// ReadAtContext reads with caller cancellation.
	ReadAtContext(ctx context.Context, p []byte, off int64) (n int, err error)

	// Prefetch triggers segment downloads for the given byte range without blocking.
	// This is a hint to the reader to start downloading segments that will be needed soon.
	Prefetch(ctx context.Context, off, length int64)
}
