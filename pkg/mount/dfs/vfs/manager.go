package vfs

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
)

// Manager manages VFS lifecycle
type Manager struct {
	manager *manager.Manager
	cache   *Cache
	logger  zerolog.Logger

	files *xsync.Map[string, *fileEntry]

	ctx    context.Context
	cancel context.CancelFunc

	totalFiles  atomic.Int32
	activeFiles atomic.Int32
}

// fileEntry tracks file metadata
type fileEntry struct {
	item     *CacheItem
	refCount atomic.Int32
	// deleted is set to true by ReleaseFile before the entry is removed from the
	// map. GetFile checks this after incrementing refCount so it can detect and
	// undo a concurrent deletion without holding a coarse lock.
	deleted atomic.Bool
}

// NewManager creates a new VFS manager
func NewManager(ctx context.Context, mgr *manager.Manager, config *config.FuseConfig) (*Manager, error) {
	ctx, cancel := context.WithCancel(ctx)

	cache, err := NewCache(ctx, mgr, config)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create cache: %w", err)
	}

	m := &Manager{
		manager: mgr,
		cache:   cache,
		logger:  logger.New("vfs"),
		files:   xsync.NewMap[string, *fileEntry](),
		ctx:     ctx,
		cancel:  cancel,
	}

	return m, nil
}

func (m *Manager) GetManager() *manager.Manager {
	return m.manager
}

// GetFile returns a streaming file handle
func (m *Manager) GetFile(info *manager.FileInfo) (*StreamingFile, error) {
	key := buildFileKey(info.Parent(), info.Name())

	// NewStreamingFile returns nil when the entry's cache item was claimed for
	// teardown by the cache janitor between handle releases — the fileEntry is
	// then stale and must be retired so a fresh item can be created. The loop
	// is bounded: each retry either succeeds or removes the stale entry it
	// observed, and the janitor's claim/delete pair is near-instantaneous.
	for attempt := 0; attempt < 8; attempt++ {
		// Fast path: existing file.
		// Increment refCount first, then verify the entry wasn't concurrently
		// deleted by ReleaseFile between our Load and the Add. If it was, undo
		// the increment and fall through to create a fresh entry.
		if entry, ok := m.files.Load(key); ok {
			entry.refCount.Add(1)
			if !entry.deleted.Load() {
				if sf := NewStreamingFile(entry.item); sf != nil {
					return sf, nil
				}
			}
			entry.refCount.Add(-1)
			m.retireEntry(key, entry)
		}

		// Get or create cache item
		item, err := m.cache.GetItem(info.Parent(), info.Name(), info.Size())
		if err != nil {
			return nil, fmt.Errorf("failed to get cache item: %w", err)
		}

		entry := &fileEntry{item: item}
		entry.refCount.Store(1)

		// Store or return existing
		actual, loaded := m.files.LoadOrStore(key, entry)
		if loaded {
			// Another goroutine created it first
			actual.refCount.Add(1)
			if !actual.deleted.Load() {
				if sf := NewStreamingFile(actual.item); sf != nil {
					return sf, nil
				}
			}
			actual.refCount.Add(-1)
			m.retireEntry(key, actual)
			continue
		}

		sf := NewStreamingFile(item)
		if sf == nil {
			// Our freshly-fetched item was claimed before we could open it
			// (possible during a forced purge). Retire and retry.
			m.retireEntry(key, entry)
			continue
		}
		m.totalFiles.Add(1)
		m.activeFiles.Add(1)
		return sf, nil
	}
	return nil, fmt.Errorf("file %s: cache item kept being torn down; giving up", key)
}

// retireEntry marks a stale fileEntry deleted and removes it from the map —
// but only if it is still the mapped entry, so a fresh replacement stored by
// a concurrent GetFile is never clobbered.
func (m *Manager) retireEntry(key string, entry *fileEntry) {
	entry.deleted.Store(true)
	m.files.Compute(key, func(old *fileEntry, loaded bool) (*fileEntry, xsync.ComputeOp) {
		if loaded && old == entry {
			return nil, xsync.DeleteOp
		}
		return old, xsync.CancelOp
	})
}

// ReleaseFile decrements the reference count
func (m *Manager) ReleaseFile(info *manager.FileInfo) {
	key := buildFileKey(info.Parent(), info.Name())

	if entry, ok := m.files.Load(key); ok {
		if entry.refCount.Add(-1) <= 0 {
			// Mark deleted before removing from the map so that any concurrent
			// GetFile that already loaded this entry can detect the deletion and
			// undo its refCount increment rather than using a stale entry.
			entry.deleted.Store(true)
			m.files.Delete(key)
			m.activeFiles.Add(-1)
			// Downloaders are stopped in CacheItem.Release() when opens reaches 0.
		}
	}
}

// Close shuts down the manager
func (m *Manager) Close() error {
	m.cancel()

	// Close all files
	m.files.Range(func(key string, entry *fileEntry) bool {
		if entry.item != nil {
			entry.item.Close()
		}
		return true
	})
	m.files.Clear()

	// Close cache
	if m.cache != nil {
		m.cache.Close()
	}

	return nil
}

// GetStats returns manager statistics
func (m *Manager) GetStats() map[string]interface{} {
	stats := map[string]interface{}{
		"type":         "dfs",
		"ready":        true,
		"enabled":      true,
		"total_files":  m.totalFiles.Load(),
		"active_files": m.activeFiles.Load(),
	}

	// Add cache stats
	if m.cache != nil {
		for k, v := range m.cache.GetStats() {
			stats["cache_"+k] = v
		}
	}

	return stats
}

func (m *Manager) CleanupCache() map[string]interface{} {
	if m.cache == nil {
		return map[string]interface{}{
			"cleanup_status": "unsupported",
			"cleanup_result": "cache is not initialized",
		}
	}
	return m.cache.RunCleanup()
}

func (m *Manager) PurgeCache() map[string]interface{} {
	if m.cache == nil {
		return map[string]interface{}{
			"purge_status": "unsupported",
			"purge_result": "cache is not initialized",
		}
	}
	return m.cache.PurgeCache()
}

func buildFileKey(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "/" + name
}
