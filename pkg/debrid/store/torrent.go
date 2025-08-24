package store

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	filterByInclude string = "include"
	filterByExclude string = "exclude"

	filterByStartsWith    string = "starts_with"
	filterByEndsWith      string = "ends_with"
	filterByNotStartsWith string = "not_starts_with"
	filterByNotEndsWith   string = "not_ends_with"

	filterByRegex    string = "regex"
	filterByNotRegex string = "not_regex"

	filterByExactMatch    string = "exact_match"
	filterByNotExactMatch string = "not_exact_match"

	filterBySizeGT string = "size_gt"
	filterBySizeLT string = "size_lt"

	filterBLastAdded string = "last_added"
)

type directoryFilter struct {
	filterType    string
	value         string
	regex         *regexp.Regexp // only for regex/not_regex
	sizeThreshold int64          // only for size_gt/size_lt
	ageThreshold  time.Duration  // only for last_added
}

type folders struct {
	sync.RWMutex
	listing map[string][]os.FileInfo // folder name to file listing
}

type CachedTorrentEntry struct {
	CachedTorrent
	deleted bool // Tombstone flag
}

type torrentCache struct {
	mu       sync.RWMutex
	torrents []CachedTorrentEntry // Changed to store entries with tombstone

	// Lookup indices
	idIndex   map[string]int
	nameIndex map[string]int

	// Compaction tracking
	deletedCount     atomic.Int32
	compactThreshold int // Trigger compaction when deletedCount exceeds this

	listing            atomic.Value
	folders            folders
	directoriesFilters map[string][]directoryFilter
	sortNeeded         atomic.Bool
}

type sortableFile struct {
	id      string
	name    string
	modTime time.Time
	size    int64
	bad     bool
}

func newTorrentCache(dirFilters map[string][]directoryFilter) *torrentCache {
	tc := &torrentCache{
		torrents:         []CachedTorrentEntry{},
		idIndex:          make(map[string]int),
		nameIndex:        make(map[string]int),
		compactThreshold: 100, // Compact when 100+ deleted entries
		folders: folders{
			listing: make(map[string][]os.FileInfo),
		},
		directoriesFilters: dirFilters,
	}

	tc.sortNeeded.Store(false)
	tc.listing.Store(make([]os.FileInfo, 0))
	return tc
}

func (tc *torrentCache) reset() {
	tc.mu.Lock()
	tc.torrents = tc.torrents[:0]       // Clear the slice
	tc.idIndex = make(map[string]int)   // Reset the ID index
	tc.nameIndex = make(map[string]int) // Reset the name index
	tc.deletedCount.Store(0)
	tc.mu.Unlock()

	// reset the sorted listing
	tc.sortNeeded.Store(false)
	tc.listing.Store(make([]os.FileInfo, 0))

	// reset any per-folder views
	tc.folders.Lock()
	tc.folders.listing = make(map[string][]os.FileInfo)
	tc.folders.Unlock()
}

func (tc *torrentCache) getByID(id string) (CachedTorrent, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if index, exists := tc.idIndex[id]; exists && index < len(tc.torrents) {
		entry := tc.torrents[index]
		if !entry.deleted {
			return entry.CachedTorrent, true
		}
	}
	return CachedTorrent{}, false
}

func (tc *torrentCache) getByName(name string) (CachedTorrent, bool) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	if index, exists := tc.nameIndex[name]; exists && index < len(tc.torrents) {
		entry := tc.torrents[index]
		if !entry.deleted {
			return entry.CachedTorrent, true
		}
	}
	return CachedTorrent{}, false
}

func (tc *torrentCache) set(name string, torrent CachedTorrent) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Check if this torrent already exists (update case)
	if existingIndex, exists := tc.idIndex[torrent.Id]; exists && existingIndex < len(tc.torrents) {
		if !tc.torrents[existingIndex].deleted {
			// Update existing entry
			tc.torrents[existingIndex].CachedTorrent = torrent
			tc.sortNeeded.Store(true)
			return
		}
	}

	// Add new torrent
	entry := CachedTorrentEntry{
		CachedTorrent: torrent,
		deleted:       false,
	}

	tc.torrents = append(tc.torrents, entry)
	index := len(tc.torrents) - 1

	tc.idIndex[torrent.Id] = index
	tc.nameIndex[name] = index
	tc.sortNeeded.Store(true)
}

func (tc *torrentCache) removeId(id string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if index, exists := tc.idIndex[id]; exists && index < len(tc.torrents) {
		if !tc.torrents[index].deleted {
			// Mark as deleted (tombstone)
			tc.torrents[index].deleted = true
			tc.deletedCount.Add(1)

			// Remove from indices
			delete(tc.idIndex, id)

			// Find and remove from name index
			for name, idx := range tc.nameIndex {
				if idx == index {
					delete(tc.nameIndex, name)
					break
				}
			}

			tc.sortNeeded.Store(true)

			// Trigger compaction if threshold exceeded
			if tc.deletedCount.Load() > int32(tc.compactThreshold) {
				go tc.compact()
			}
		}
	}
}

func (tc *torrentCache) remove(name string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if index, exists := tc.nameIndex[name]; exists && index < len(tc.torrents) {
		if !tc.torrents[index].deleted {
			// Mark as deleted (tombstone)
			torrentID := tc.torrents[index].CachedTorrent.Id
			tc.torrents[index].deleted = true
			tc.deletedCount.Add(1)

			// Remove from indices
			delete(tc.nameIndex, name)
			delete(tc.idIndex, torrentID)

			tc.sortNeeded.Store(true)

			// Trigger compaction if threshold exceeded
			if tc.deletedCount.Load() > int32(tc.compactThreshold) {
				go tc.compact()
			}
		}
	}
}

// Compact removes tombstoned entries and rebuilds indices
func (tc *torrentCache) compact() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	deletedCount := tc.deletedCount.Load()
	if deletedCount == 0 {
		return // Nothing to compact
	}

	// Create new slice with only non-deleted entries
	newTorrents := make([]CachedTorrentEntry, 0, len(tc.torrents)-int(deletedCount))
	newIdIndex := make(map[string]int, len(tc.idIndex))
	newNameIndex := make(map[string]int, len(tc.nameIndex))

	// Copy non-deleted entries
	for oldIndex, entry := range tc.torrents {
		if !entry.deleted {
			newIndex := len(newTorrents)
			newTorrents = append(newTorrents, entry)

			// Find the name for this torrent (reverse lookup)
			for name, nameIndex := range tc.nameIndex {
				if nameIndex == oldIndex {
					newNameIndex[name] = newIndex
					break
				}
			}

			newIdIndex[entry.CachedTorrent.Id] = newIndex
		}
	}

	// Replace old data with compacted data
	tc.torrents = newTorrents
	tc.idIndex = newIdIndex
	tc.nameIndex = newNameIndex

	tc.deletedCount.Store(0)
	tc.sortNeeded.Store(true)
}

func (tc *torrentCache) ForceCompact() {
	tc.compact()
}

func (tc *torrentCache) GetStats() (total, active, deleted int) {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	total = len(tc.torrents)
	deleted = int(tc.deletedCount.Load())
	active = total - deleted

	return total, active, deleted
}

func (tc *torrentCache) refreshListing() {
	tc.mu.RLock()
	all := make([]sortableFile, 0, len(tc.nameIndex))
	for name, index := range tc.nameIndex {
		if index < len(tc.torrents) && !tc.torrents[index].deleted {
			t := tc.torrents[index].CachedTorrent
			all = append(all, sortableFile{t.Id, name, t.AddedOn, t.Bytes, t.Bad})
		}
	}
	tc.sortNeeded.Store(false)
	tc.mu.RUnlock()

	sort.Slice(all, func(i, j int) bool {
		if all[i].name != all[j].name {
			return all[i].name < all[j].name
		}
		return all[i].modTime.Before(all[j].modTime)
	})

	wg := sync.WaitGroup{}

	wg.Add(1) // for all listing
	go func() {
		defer wg.Done()
		listing := make([]os.FileInfo, len(all))
		for i, sf := range all {
			listing[i] = &fileInfo{sf.id, sf.name, sf.size, 0755 | os.ModeDir, sf.modTime, true}
		}
		tc.listing.Store(listing)
	}()

	wg.Add(1)
	// For __bad__
	go func() {
		defer wg.Done()
		listing := make([]os.FileInfo, 0)
		for _, sf := range all {
			if sf.bad {
				listing = append(listing, &fileInfo{
					id:      sf.id,
					name:    fmt.Sprintf("%s || %s", sf.name, sf.id),
					size:    sf.size,
					mode:    0755 | os.ModeDir,
					modTime: sf.modTime,
					isDir:   true,
				})
			}
		}
		tc.folders.Lock()
		if len(listing) > 0 {
			tc.folders.listing["__bad__"] = listing
		} else {
			delete(tc.folders.listing, "__bad__")
		}
		tc.folders.Unlock()
	}()

	now := time.Now()
	wg.Add(len(tc.directoriesFilters)) // for each directory filter
	for dir, filters := range tc.directoriesFilters {
		go func(dir string, filters []directoryFilter) {
			defer wg.Done()
			var matched []os.FileInfo
			for _, sf := range all {
				if tc.torrentMatchDirectory(filters, sf, now) {
					matched = append(matched, &fileInfo{
						id:   sf.id,
						name: sf.name, size: sf.size,
						mode: 0755 | os.ModeDir, modTime: sf.modTime, isDir: true,
					})
				}
			}

			tc.folders.Lock()
			if len(matched) > 0 {
				tc.folders.listing[dir] = matched
			} else {
				delete(tc.folders.listing, dir)
			}
			tc.folders.Unlock()
		}(dir, filters)
	}

	wg.Wait()
}

func (tc *torrentCache) getListing() []os.FileInfo {
	// Fast path: if we have a sorted list and no changes since last sort
	if !tc.sortNeeded.Load() {
		return tc.listing.Load().([]os.FileInfo)
	}

	// Slow path: need to sort
	tc.refreshListing()
	return tc.listing.Load().([]os.FileInfo)
}

func (tc *torrentCache) getFolderListing(folderName string) []os.FileInfo {
	tc.folders.RLock()
	defer tc.folders.RUnlock()
	if folderName == "" {
		return tc.getListing()
	}
	if folder, ok := tc.folders.listing[folderName]; ok {
		return folder
	}
	// If folder not found, return empty slice
	return []os.FileInfo{}
}

func (tc *torrentCache) torrentMatchDirectory(filters []directoryFilter, file sortableFile, now time.Time) bool {
	torrentName := strings.ToLower(file.name)
	for _, filter := range filters {
		matched := false

		switch filter.filterType {
		case filterByInclude:
			matched = strings.Contains(torrentName, filter.value)
		case filterByStartsWith:
			matched = strings.HasPrefix(torrentName, filter.value)
		case filterByEndsWith:
			matched = strings.HasSuffix(torrentName, filter.value)
		case filterByExactMatch:
			matched = torrentName == filter.value
		case filterByExclude:
			matched = !strings.Contains(torrentName, filter.value)
		case filterByNotStartsWith:
			matched = !strings.HasPrefix(torrentName, filter.value)
		case filterByNotEndsWith:
			matched = !strings.HasSuffix(torrentName, filter.value)
		case filterByRegex:
			matched = filter.regex.MatchString(torrentName)
		case filterByNotRegex:
			matched = !filter.regex.MatchString(torrentName)
		case filterByNotExactMatch:
			matched = torrentName != filter.value
		case filterBySizeGT:
			matched = file.size > filter.sizeThreshold
		case filterBySizeLT:
			matched = file.size < filter.sizeThreshold
		case filterBLastAdded:
			matched = file.modTime.After(now.Add(-filter.ageThreshold))
		}
		if !matched {
			return false // All filters must match
		}
	}

	// If we get here, all filters matched
	return true
}

func (tc *torrentCache) getAll() map[string]CachedTorrent {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	result := make(map[string]CachedTorrent)
	for _, entry := range tc.torrents {
		if !entry.deleted {
			result[entry.CachedTorrent.Id] = entry.CachedTorrent
		}
	}
	return result
}

func (tc *torrentCache) getAllCount() int {
	tc.mu.RLock()
	defer tc.mu.RUnlock()
	return len(tc.torrents) - int(tc.deletedCount.Load())
}

func (tc *torrentCache) getAllByName() map[string]CachedTorrent {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	results := make(map[string]CachedTorrent, len(tc.nameIndex))
	for name, index := range tc.nameIndex {
		if index < len(tc.torrents) && !tc.torrents[index].deleted {
			results[name] = tc.torrents[index].CachedTorrent
		}
	}
	return results
}

func (tc *torrentCache) getIdMaps() map[string]struct{} {
	tc.mu.RLock()
	defer tc.mu.RUnlock()

	res := make(map[string]struct{}, len(tc.idIndex))
	for id, index := range tc.idIndex {
		if index < len(tc.torrents) && !tc.torrents[index].deleted {
			res[id] = struct{}{}
		}
	}
	return res
}
