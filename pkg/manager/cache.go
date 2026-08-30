package manager

import (
	"strings"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"golang.org/x/sync/singleflight"
)

const (
	torrentEntryCachePrefix = "torrent::"
)

func (m *Manager) initEntryCache() {
	m.entry = NewEntryCache(m)
}

type EntryCacheItem struct {
	current  *FileInfo
	children []FileInfo
}

type EntryCache struct {
	manager    *Manager
	entries    *xsync.Map[string, EntryCacheItem]
	refreshing singleflight.Group
	generation atomic.Uint64
}

func NewEntryCache(manager *Manager) *EntryCache {
	return &EntryCache{
		manager: manager,
		entries: xsync.NewMap[string, EntryCacheItem](),
	}
}

func (e *EntryCache) Get(name string) (*FileInfo, []FileInfo) {
	// Relative-time views change as the clock advances even when library
	// metadata does not, so never retain their children in the entry cache.
	if !strings.HasPrefix(name, torrentEntryCachePrefix) && e.manager.virtualFoldersSnapshot().isTimeSensitive(name) {
		return e.manager.getEntryChildren(name)
	}
	item, ok := e.entries.Load(name)
	if !ok {
		item = e.refreshEntry(name)
	}
	return item.current, item.children
}

func (e *EntryCache) refreshEntry(name string) EntryCacheItem {
	result, _, _ := e.refreshing.Do(name, func() (any, error) {
		return e._refreshEntry(name), nil
	})
	return result.(EntryCacheItem)
}

func (e *EntryCache) _refreshEntry(name string) EntryCacheItem {
	generation := e.generation.Load()
	if after, ok := strings.CutPrefix(name, torrentEntryCachePrefix); ok {
		// This is a torrent folder
		torrentName := after
		current, children := e.manager.getTorrentChildren(torrentName)
		item := EntryCacheItem{
			current:  current,
			children: children,
		}
		if e.generation.Load() == generation {
			e.entries.Store(name, item)
		}
		return item
	}

	// This is a built-in, provider, or virtual folder.
	current, children := e.manager.getEntryChildren(name)
	item := EntryCacheItem{
		current:  current,
		children: children,
	}
	if e.generation.Load() == generation {
		e.entries.Store(name, item)
	}
	return item
}

// Refresh triggers a cache refresh with debouncing.
// If called multiple times rapidly, only one refresh will occur.
func (e *EntryCache) Refresh() {
	e.generation.Add(1)
	// Clear every group and torrent entry. This is deliberately independent of
	// the current config so renamed/removed virtual folders cannot survive in the
	// cache after a live update.
	e.entries.Clear()
}
