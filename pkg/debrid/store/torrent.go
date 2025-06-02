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

type torrentCache struct {
	mu                 sync.Mutex
	byID               map[string]CachedTorrent
	byName             map[string]CachedTorrent
	listing            atomic.Value
	folderListing      map[string][]os.FileInfo
	folderListingMu    sync.RWMutex
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
		byID:               make(map[string]CachedTorrent),
		byName:             make(map[string]CachedTorrent),
		folderListing:      make(map[string][]os.FileInfo),
		directoriesFilters: dirFilters,
	}

	tc.sortNeeded.Store(false)
	tc.listing.Store(make([]os.FileInfo, 0))
	return tc
}

func (tc *torrentCache) reset() {
	tc.mu.Lock()
	tc.byID = make(map[string]CachedTorrent)
	tc.byName = make(map[string]CachedTorrent)
	tc.mu.Unlock()

	// reset the sorted listing
	tc.sortNeeded.Store(false)
	tc.listing.Store(make([]os.FileInfo, 0))

	// reset any per-folder views
	tc.folderListingMu.Lock()
	tc.folderListing = make(map[string][]os.FileInfo)
	tc.folderListingMu.Unlock()
}

func (tc *torrentCache) getByID(id string) (CachedTorrent, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	torrent, exists := tc.byID[id]
	return torrent, exists
}

func (tc *torrentCache) getByName(name string) (CachedTorrent, bool) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	torrent, exists := tc.byName[name]
	return torrent, exists
}

func (tc *torrentCache) set(name string, torrent, newTorrent CachedTorrent) {
	tc.mu.Lock()
	// Set the id first
	tc.byID[newTorrent.Id] = torrent // This is the unadulterated torrent
	tc.byName[name] = newTorrent     // This is likely the modified torrent
	tc.mu.Unlock()
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
	tc.folderListingMu.RLock()
	defer tc.folderListingMu.RUnlock()
	if folderName == "" {
		return tc.getListing()
	}
	if folder, ok := tc.folderListing[folderName]; ok {
		return folder
	}
	// If folder not found, return empty slice
	return []os.FileInfo{}
}

func (tc *torrentCache) refreshListing() {

	tc.mu.Lock()
	all := make([]sortableFile, 0, len(tc.byName))
	for name, t := range tc.byName {
		all = append(all, sortableFile{t.Id, name, t.AddedOn, t.Bytes, t.Bad})
	}
	tc.sortNeeded.Store(false)
	tc.mu.Unlock()

	sort.Slice(all, func(i, j int) bool {
		if all[i].name != all[j].name {
			return all[i].name < all[j].name
		}
		return all[i].modTime.Before(all[j].modTime)
	})

	wg := sync.WaitGroup{}

	wg.Add(1) // for all listing
	go func() {
		listing := make([]os.FileInfo, len(all))
		for i, sf := range all {
			listing[i] = &fileInfo{sf.id, sf.name, sf.size, 0755 | os.ModeDir, sf.modTime, true}
		}
		tc.listing.Store(listing)
	}()
	wg.Done()

	wg.Add(1)
	// For __bad__
	go func() {
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
		tc.folderListingMu.Lock()
		if len(listing) > 0 {
			tc.folderListing["__bad__"] = listing
		} else {
			delete(tc.folderListing, "__bad__")
		}
		tc.folderListingMu.Unlock()
	}()
	wg.Done()

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

			tc.folderListingMu.Lock()
			if len(matched) > 0 {
				tc.folderListing[dir] = matched
			} else {
				delete(tc.folderListing, dir)
			}
			tc.folderListingMu.Unlock()
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
	tc.mu.Lock()
	defer tc.mu.Unlock()
	result := make(map[string]CachedTorrent)
	for name, torrent := range tc.byID {
		result[name] = torrent
	}
	return result
}

func (tc *torrentCache) getIdMaps() map[string]struct{} {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	res := make(map[string]struct{}, len(tc.byID))
	for id := range tc.byID {
		res[id] = struct{}{}
	}
	return res
}

func (tc *torrentCache) removeId(id string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	delete(tc.byID, id)
	tc.sortNeeded.Store(true)
}

func (tc *torrentCache) remove(name string) {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	delete(tc.byName, name)
	tc.sortNeeded.Store(true)
}
