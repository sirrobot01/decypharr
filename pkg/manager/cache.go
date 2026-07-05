package manager

import (
	"strings"

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
}

func NewEntryCache(manager *Manager) *EntryCache {
	return &EntryCache{
		manager: manager,
		entries: xsync.NewMap[string, EntryCacheItem](),
	}
}

func (e *EntryCache) Get(name string) (*FileInfo, []FileInfo) {
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
	if after, ok := strings.CutPrefix(name, torrentEntryCachePrefix); ok {
		// This is a torrent folder
		torrentName := after
		current, children := e.manager.getTorrentChildren(torrentName)
		item := EntryCacheItem{
			current:  current,
			children: children,
		}
		e.entries.Store(name, item)
		return item
	}

	// This is either a __all__, __bad__ or custom folder
	current, children := e.manager.getEntryChildren(name)
	item := EntryCacheItem{
		current:  current,
		children: children,
	}
	e.entries.Store(name, item)
	return item
}

// Refresh triggers a cache refresh with debouncing.
// If called multiple times rapidly, only one refresh will occur.
func (e *EntryCache) Refresh() {
	e.entries.Delete(EntryAllFolder)
	e.entries.Delete(EntryBadFolder)
	e.entries.Delete(EntryTorrentFolder)
	e.entries.Delete(EntryNZBFolder)
	for k := range e.manager.config.CustomFolders {
		e.entries.Delete(k)
	}
	// Also clear torrent-level cache entries to prevent stale file listings
	e.entries.Range(func(key string, _ EntryCacheItem) bool {
		if strings.HasPrefix(key, torrentEntryCachePrefix) {
			e.entries.Delete(key)
		}
		return true
	})
}
