package dfs

import (
	"sync/atomic"
)

// Stats provides unified statistics across all DFS mounts
// This aggregates stats from all mounted filesystems into a single view
type Stats struct {
	// Disk cache statistics
	CacheDirSize  atomic.Int64 // Total bytes used across all mounts
	CacheDirLimit atomic.Int64 // Total cache limit across all mounts

	// File operations
	OpenedFiles atomic.Int64 // Set of opened files
	ActiveReads atomic.Int64 // Total active read operations

	// Configuration (same across all mounts)
	ChunkSize     int64
	ReadAheadSize int64
	BufferSize    int64
}

// Reset resets all statistics to zero
func (s *Stats) Reset() {
	s.CacheDirSize.Store(0)
	s.CacheDirLimit.Store(0)
	s.OpenedFiles.Store(0)
	s.ActiveReads.Store(0)
}

// ToMap converts stats to a map for JSON serialization
func (s *Stats) ToMap() map[string]any {
	stats := map[string]any{
		"cache_dir_size":  s.CacheDirSize.Load(),
		"cache_dir_limit": s.CacheDirLimit.Load(),
		"active_reads":    s.ActiveReads.Load(),
		"opened_files":    s.OpenedFiles.Load(),
		"chunk_size":      s.ChunkSize,
		"read_ahead_size": s.ReadAheadSize,
		"buffer_size":     s.BufferSize,
	}

	return stats
}

// MountStats represents statistics for a single mount
type MountStats struct {
	Name      string
	Type      string
	Mounted   bool
	MountPath string

	// Cache
	CacheDirSize  int64
	CacheDirLimit int64

	// Operations
	OpenedFiles int
	ActiveReads int64
}

// ToMap converts mount stats to map
func (m *MountStats) ToMap() map[string]any {
	stats := map[string]any{
		"name":            m.Name,
		"type":            m.Type,
		"mounted":         m.Mounted,
		"mount_path":      m.MountPath,
		"cache_dir_size":  m.CacheDirSize,
		"cache_dir_limit": m.CacheDirLimit,
		"active_reads":    m.ActiveReads,
		"opened_files":    m.OpenedFiles,
	}

	return stats
}
