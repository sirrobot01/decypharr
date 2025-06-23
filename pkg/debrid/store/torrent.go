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

type torrents struct {
	sync.RWMutex
	byID   map[string]CachedTorrent
	byName map[string]CachedTorrent
}

type folders struct {
	sync.RWMutex
	listing map[string][]os.FileInfo // folder name to file listing
}

type torrentCache struct {
	torrents torrents

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
		torrents: torrents{
			byID:   make(map[string]CachedTorrent),
			byName: make(map[string]CachedTorrent),
		},
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
	tc.torrents.Lock()
	tc.torrents.byID = make(map[string]CachedTorrent)
	tc.torrents.byName = make(map[string]CachedTorrent)
	tc.torrents.Unlock()

	// reset the sorted listing
	tc.sortNeeded.Store(false)
	tc.listing.Store(make([]os.FileInfo, 0))

	// reset any per-folder views
	tc.folders.Lock()
	tc.folders.listing = make(map[string][]os.FileInfo)
	tc.folders.Unlock()
}

func (tc *torrentCache) getByID(id string) (CachedTorrent, bool) {
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	torrent, exists := tc.torrents.byID[id]
	return torrent, exists
}

func (tc *torrentCache) getByName(name string) (CachedTorrent, bool) {
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	torrent, exists := tc.torrents.byName[name]
	return torrent, exists
}

func (tc *torrentCache) set(name string, torrent, newTorrent CachedTorrent) {
	tc.torrents.Lock()
	// Set the id first

	tc.torrents.byName[name] = torrent
	tc.torrents.byID[torrent.Id] = torrent // This is the unadulterated torrent
	tc.torrents.Unlock()
	tc.sortNeeded.Store(true)
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

func (tc *torrentCache) refreshListing() {

	tc.torrents.RLock()
	all := make([]sortableFile, 0, len(tc.torrents.byName))
	for name, t := range tc.torrents.byName {
		all = append(all, sortableFile{t.Id, name, t.AddedOn, t.Bytes, t.Bad})
	}
	tc.sortNeeded.Store(false)
	tc.torrents.RUnlock()

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
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	result := make(map[string]CachedTorrent, len(tc.torrents.byID))
	for name, torrent := range tc.torrents.byID {
		result[name] = torrent
	}
	return result
}

func (tc *torrentCache) getAllCount() int {
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	return len(tc.torrents.byID)
}

func (tc *torrentCache) getAllByName() map[string]CachedTorrent {
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	results := make(map[string]CachedTorrent, len(tc.torrents.byName))
	for name, torrent := range tc.torrents.byName {
		results[name] = torrent
	}
	return results
}

func (tc *torrentCache) getIdMaps() map[string]struct{} {
	tc.torrents.RLock()
	defer tc.torrents.RUnlock()
	res := make(map[string]struct{}, len(tc.torrents.byID))
	for id := range tc.torrents.byID {
		res[id] = struct{}{}
	}
	return res
}

func (tc *torrentCache) removeId(id string) {
	tc.torrents.Lock()
	defer tc.torrents.Unlock()
	delete(tc.torrents.byID, id)
	tc.sortNeeded.Store(true)
}

func (tc *torrentCache) remove(name string) {
	tc.torrents.Lock()
	defer tc.torrents.Unlock()
	delete(tc.torrents.byName, name)
	tc.sortNeeded.Store(true)
}
