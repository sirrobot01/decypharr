// Package reader provides a high-performance, error-resilient streaming reader
// for Usenet segments. It implements io.ReaderAt with automatic caching,
// prefetching, and transparent re-download on cache misses.
//
// Architecture:
//   - StreamingReader: Top-level reader with encryption support
//   - SegmentCache: Disk storage with pin/unpin for safe eviction
//   - SegmentFetcher: NNTP downloads with deduplication and retry
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

	// StateOnDisk indicates the segment data is on disk.
	StateOnDisk

	// StateFetching indicates the segment is currently being downloaded.
	StateFetching

	// StateFailed indicates the segment download failed permanently.
	StateFailed

	// StateEvicting indicates the evictor has reserved the segment and is
	// punching its disk range. It is a transient state held only across the
	// buffer Discard: the slot was OnDisk, will become Empty once the punch
	// completes. Crucially, MarkFetching only transitions Empty->Fetching, so
	// while a segment is Evicting no re-fetch can begin writing into the range
	// being punched. This closes the race where a reader re-downloaded a
	// segment in the gap between the evictor's state flip and its deferred
	// Discard, only for the Discard to punch the freshly-written bytes back
	// out — leaving the slot OnDisk but unreadable.
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

// Config holds configuration for StreamingReader.
type Config struct {
	// MaxDisk is the maximum disk space to use for segment caching (default: 256MB).
	MaxDisk int64

	// DiskPath is the base directory for disk cache (default: system temp dir).
	DiskPath string

	// MaxConnections is the maximum concurrent NNTP downloads (default: 8).
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
		MaxDisk:         256 * 1024 * 1024, // 256MB
		MaxConnections:  8,
		PrefetchAhead:   8,
		DownloadTimeout: 60 * time.Second,
		MaxRetries:      3,
		RetryDelay:      time.Second,
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
	ahead := int(readAheadBytes / segBytes)
	if ahead < minAhead {
		ahead = minAhead
	}
	if ahead > maxAhead {
		ahead = maxAhead
	}
	return ahead
}

// Option is a functional option for configuring StreamingReader.
type Option func(*Config)

// WithMaxDisk sets the maximum disk space for segment caching.
func WithMaxDisk(bytes int64) Option {
	return func(c *Config) {
		c.MaxDisk = bytes
	}
}

// WithDiskPath sets the base directory for disk cache.
func WithDiskPath(path string) Option {
	return func(c *Config) {
		c.DiskPath = path
	}
}

// WithMaxConnections sets the maximum concurrent NNTP downloads.
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

	// Downloads
	Downloads       atomic.Int64
	DownloadBytes   atomic.Int64
	DownloadRetries atomic.Int64
	DownloadErrors  atomic.Int64

	// Prefetch
	PrefetchHits   atomic.Int64
	PrefetchMisses atomic.Int64
}

// Snapshot returns a copy of the current stats.
func (s *ReaderStats) Snapshot() map[string]int64 {
	return map[string]int64{
		"reads":            s.Reads.Load(),
		"bytes_read":       s.BytesRead.Load(),
		"read_errors":      s.ReadErrors.Load(),
		"cache_hits":       s.CacheHits.Load(),
		"cache_misses":     s.CacheMisses.Load(),
		"evictions":        s.Evictions.Load(),
		"downloads":        s.Downloads.Load(),
		"download_bytes":   s.DownloadBytes.Load(),
		"download_retries": s.DownloadRetries.Load(),
		"download_errors":  s.DownloadErrors.Load(),
		"prefetch_hits":    s.PrefetchHits.Load(),
		"prefetch_misses":  s.PrefetchMisses.Load(),
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
