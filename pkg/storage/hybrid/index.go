package hybrid

import (
	"sort"
	"sync"

	"github.com/puzpuzpuz/xsync/v3"
)

// IndexEntry holds in-memory metadata for fast lookups
// Hot fields are stored here to avoid disk reads for common queries
type IndexEntry struct {
	// Disk location
	Offset int64
	Size   int32

	// Hot fields (queried without disk access)
	Category  string
	Provider  string
	Status    string
	Name      string
	TotalSize int64
	Protocol  string // "torrent" or "nzb"
	Bad       bool
	AddedOn   int64 // Unix timestamp
}

// Index is the in-memory index with secondary indexes for fast filtering
type Index struct {
	// Primary index: key -> entry (lock-free reads)
	entries *xsync.MapOf[string, *IndexEntry]

	// Secondary indexes need mutex for consistent iteration
	mu         sync.RWMutex
	byCategory map[string]map[string]struct{} // category -> set of keys
	byProvider map[string]map[string]struct{} // provider -> set of keys
	byStatus   map[string]map[string]struct{} // status -> set of keys

	// Sorted keys for sequential iteration
	sortedKeys  []string
	sortedDirty bool
}


// newIndex creates a new empty index
func newIndex() *Index {
	return &Index{
		entries:    xsync.NewMapOf[string, *IndexEntry](),
		byCategory: make(map[string]map[string]struct{}),
		byProvider: make(map[string]map[string]struct{}),
		byStatus:   make(map[string]map[string]struct{}),
	}
}

// Put adds or updates an entry in the index
func (idx *Index) Put(key string, entry *IndexEntry) {
	// Check if updating - need to remove from secondary indexes first
	if existing, ok := idx.entries.Load(key); ok {
		idx.mu.Lock()
		idx.removeFromSecondary(key, existing)
		idx.mu.Unlock()
	}

	// Add to primary index (lock-free)
	idx.entries.Store(key, entry)

	// Add to secondary indexes
	idx.mu.Lock()
	idx.addToSecondary(key, entry)
	idx.sortedDirty = true
	idx.mu.Unlock()
}

// Get retrieves an entry by key (lock-free)
func (idx *Index) Get(key string) *IndexEntry {
	entry, _ := idx.entries.Load(key)
	return entry
}

// Delete removes an entry from the index
func (idx *Index) Delete(key string) {
	if entry, ok := idx.entries.Load(key); ok {
		idx.mu.Lock()
		idx.removeFromSecondary(key, entry)
		idx.sortedDirty = true
		idx.mu.Unlock()
		idx.entries.Delete(key)
	}
}

// Len returns the number of entries (O(1) with xsync.Map)
func (idx *Index) Len() int {
	return idx.entries.Size()
}

// Keys returns all keys
func (idx *Index) Keys() []string {
	keys := make([]string, 0, idx.entries.Size())
	idx.entries.Range(func(k string, v *IndexEntry) bool {
		keys = append(keys, k)
		return true
	})
	return keys
}

// KeysSortedByOffset returns keys sorted by disk offset for sequential I/O
func (idx *Index) KeysSortedByOffset() []string {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if !idx.sortedDirty && len(idx.sortedKeys) == idx.entries.Size() {
		// Return copy to avoid data races
		result := make([]string, len(idx.sortedKeys))
		copy(result, idx.sortedKeys)
		return result
	}

	// Rebuild sorted keys. Snapshot (key, offset) pairs in a single Range so
	// the sort compares cached offsets instead of doing two map loads per
	// comparison (which dominated for large stores: ~2·n·log(n) lookups).
	type keyOffset struct {
		key string
		off int64
	}
	pairs := make([]keyOffset, 0, idx.entries.Size())
	idx.entries.Range(func(k string, v *IndexEntry) bool {
		pairs = append(pairs, keyOffset{key: k, off: v.Offset})
		return true
	})

	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].off < pairs[j].off
	})

	idx.sortedKeys = make([]string, len(pairs))
	for i := range pairs {
		idx.sortedKeys[i] = pairs[i].key
	}

	idx.sortedDirty = false

	result := make([]string, len(idx.sortedKeys))
	copy(result, idx.sortedKeys)
	return result
}

// ForEach iterates over all entries (lock-free)
func (idx *Index) ForEach(fn func(key string, entry *IndexEntry) error) error {
	var err error
	idx.entries.Range(func(k string, v *IndexEntry) bool {
		if e := fn(k, v); e != nil {
			err = e
			return false
		}
		return true
	})
	return err
}

// GetByCategory returns all keys matching a category
func (idx *Index) GetByCategory(category string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	keySet := idx.byCategory[category]
	if keySet == nil {
		return nil
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	return keys
}

// GetByProvider returns all keys matching a provider
func (idx *Index) GetByProvider(provider string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	keySet := idx.byProvider[provider]
	if keySet == nil {
		return nil
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	return keys
}

// GetByStatus returns all keys matching a status
func (idx *Index) GetByStatus(status string) []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	keySet := idx.byStatus[status]
	if keySet == nil {
		return nil
	}

	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	return keys
}

// Categories returns all known categories
func (idx *Index) Categories() []string {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	cats := make([]string, 0, len(idx.byCategory))
	for c := range idx.byCategory {
		cats = append(cats, c)
	}
	return cats
}

// TotalSize returns the total size of all indexed values
func (idx *Index) TotalSize() int64 {
	var total int64
	idx.entries.Range(func(k string, e *IndexEntry) bool {
		total += int64(e.Size)
		return true
	})
	return total
}

// MemoryUsage returns approximate memory usage of the index
func (idx *Index) MemoryUsage() int64 {
	// Estimate: per entry overhead
	// - xsync.Map entry: ~100 bytes
	// - IndexEntry struct: ~80 bytes
	// - strings: variable
	perEntry := int64(180)

	// Secondary index overhead
	idx.mu.RLock()
	secondaryOverhead := int64(0)
	for _, keySet := range idx.byCategory {
		secondaryOverhead += int64(len(keySet)) * 50
	}
	for _, keySet := range idx.byProvider {
		secondaryOverhead += int64(len(keySet)) * 50
	}
	for _, keySet := range idx.byStatus {
		secondaryOverhead += int64(len(keySet)) * 50
	}
	idx.mu.RUnlock()

	return int64(idx.entries.Size())*perEntry + secondaryOverhead
}

// addToSecondary adds a key to secondary indexes (must hold write lock)
func (idx *Index) addToSecondary(key string, entry *IndexEntry) {
	// Category index
	if entry.Category != "" {
		if idx.byCategory[entry.Category] == nil {
			idx.byCategory[entry.Category] = make(map[string]struct{})
		}
		idx.byCategory[entry.Category][key] = struct{}{}
	}

	// Provider index
	if entry.Provider != "" {
		if idx.byProvider[entry.Provider] == nil {
			idx.byProvider[entry.Provider] = make(map[string]struct{})
		}
		idx.byProvider[entry.Provider][key] = struct{}{}
	}

	// Status index
	if entry.Status != "" {
		if idx.byStatus[entry.Status] == nil {
			idx.byStatus[entry.Status] = make(map[string]struct{})
		}
		idx.byStatus[entry.Status][key] = struct{}{}
	}
}

// removeFromSecondary removes a key from secondary indexes (must hold write lock)
func (idx *Index) removeFromSecondary(key string, entry *IndexEntry) {
	// Category index
	if entry.Category != "" {
		if keySet := idx.byCategory[entry.Category]; keySet != nil {
			delete(keySet, key)
			if len(keySet) == 0 {
				delete(idx.byCategory, entry.Category)
			}
		}
	}

	// Provider index
	if entry.Provider != "" {
		if keySet := idx.byProvider[entry.Provider]; keySet != nil {
			delete(keySet, key)
			if len(keySet) == 0 {
				delete(idx.byProvider, entry.Provider)
			}
		}
	}

	// Status index
	if entry.Status != "" {
		if keySet := idx.byStatus[entry.Status]; keySet != nil {
			delete(keySet, key)
			if len(keySet) == 0 {
				delete(idx.byStatus, entry.Status)
			}
		}
	}
}
