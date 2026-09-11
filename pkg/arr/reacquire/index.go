package reacquire

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unique"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

type entryFileKey struct {
	entryID string
	fileID  string
}

type arrFileKey struct {
	arrName string
	fileID  int
}

type downloadKey struct {
	arrName    string
	downloadID string
}

type mediaKey struct {
	arrName string
	mediaID int
}

type Index struct {
	mu        sync.RWMutex
	bindings  map[entryFileKey]Binding
	byArr     map[string]map[entryFileKey]struct{}
	byArrFile map[arrFileKey]entryFileKey
	// A download, episode, or movie binds a handful of files, so these hold a
	// slice: a one-entry Go map costs several hundred bytes, which a library
	// with hundreds of thousands of files cannot afford. byArr and bySeries
	// keep maps because their buckets are large.
	byDownload  map[downloadKey][]entryFileKey
	byEpisode   map[mediaKey][]entryFileKey
	byMovie     map[mediaKey][]entryFileKey
	bySeries    map[mediaKey]map[entryFileKey]struct{}
	generations map[string]uint64
}

func NewIndex() *Index {
	return &Index{
		bindings:    make(map[entryFileKey]Binding),
		byArr:       make(map[string]map[entryFileKey]struct{}),
		byArrFile:   make(map[arrFileKey]entryFileKey),
		byDownload:  make(map[downloadKey][]entryFileKey),
		byEpisode:   make(map[mediaKey][]entryFileKey),
		byMovie:     make(map[mediaKey][]entryFileKey),
		bySeries:    make(map[mediaKey]map[entryFileKey]struct{}),
		generations: make(map[string]uint64),
	}
}

func (i *Index) Lookup(entryID, fileID string) (Binding, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	binding, ok := i.bindings[entryFileKey{entryID: entryID, fileID: fileID}]
	return cloneBinding(binding), ok
}

func (i *Index) ByArrFile(arrName string, fileID int) (Binding, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()

	key, ok := i.byArrFile[arrFileKey{arrName: arrName, fileID: fileID}]
	if !ok {
		return Binding{}, false
	}
	binding, ok := i.bindings[key]
	return cloneBinding(binding), ok
}

func (i *Index) ByDownloadID(arrName, downloadID string) []Binding {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return i.bindingsForKeysLocked(i.byDownload[downloadKey{arrName: arrName, downloadID: downloadID}])
}

func (i *Index) ByEpisodeID(arrName string, episodeID int) []Binding {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return i.bindingsForKeysLocked(i.byEpisode[mediaKey{arrName: arrName, mediaID: episodeID}])
}

func (i *Index) ByMovieID(arrName string, movieID int) []Binding {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return i.bindingsForKeysLocked(i.byMovie[mediaKey{arrName: arrName, mediaID: movieID}])
}

func (i *Index) BySeriesID(arrName string, seriesID int) []Binding {
	i.mu.RLock()
	defer i.mu.RUnlock()

	return i.bindingsForLocked(i.bySeries[mediaKey{arrName: arrName, mediaID: seriesID}])
}

func (i *Index) All() []Binding {
	i.mu.RLock()
	defer i.mu.RUnlock()

	bindings := make([]Binding, 0, len(i.bindings))
	for _, binding := range i.bindings {
		bindings = append(bindings, cloneBinding(binding))
	}
	sortBindings(bindings)
	return bindings
}

// ArrSummary is what one Arr contributes to the binding index.
type ArrSummary struct {
	ArrName    string    `json:"arrName"`
	ArrType    arr.Type  `json:"arrType"`
	Bindings   int       `json:"bindings"`
	Actionable int       `json:"actionable"`
	Generation uint64    `json:"generation"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Summary counts the index per Arr, so a caller can tell a warm index from an
// empty one without reading hundreds of thousands of bindings.
func (i *Index) Summary() []ArrSummary {
	i.mu.RLock()
	defer i.mu.RUnlock()

	byArr := make(map[string]*ArrSummary, len(i.byArr))
	for arrName, generation := range i.generations {
		byArr[arrName] = &ArrSummary{ArrName: arrName, Generation: generation}
	}
	for _, binding := range i.bindings {
		summary, ok := byArr[binding.ArrName]
		if !ok {
			summary = &ArrSummary{ArrName: binding.ArrName}
			byArr[binding.ArrName] = summary
		}
		summary.ArrType = binding.ArrType
		summary.Bindings++
		if binding.AuthorizesMutation() {
			summary.Actionable++
		}
		if binding.UpdatedAt.After(summary.UpdatedAt) {
			summary.UpdatedAt = binding.UpdatedAt
		}
	}

	summaries := make([]ArrSummary, 0, len(byArr))
	for _, summary := range byArr {
		summaries = append(summaries, *summary)
	}
	slices.SortFunc(summaries, func(left, right ArrSummary) int {
		return cmp.Compare(left.ArrName, right.ArrName)
	})
	return summaries
}

// Search returns bindings whose entry or file name contains query, capped at
// limit. It is for a person picking one file, never for bulk reads.
func (i *Index) Search(arrName, query string, limit int) []Binding {
	query = strings.ToLower(strings.TrimSpace(query))
	if limit <= 0 {
		limit = 25
	}

	i.mu.RLock()
	defer i.mu.RUnlock()

	matches := make([]Binding, 0, limit)
	for _, binding := range i.bindings {
		if arrName != "" && binding.ArrName != arrName {
			continue
		}
		if query != "" &&
			!strings.Contains(strings.ToLower(binding.EntryName), query) &&
			!strings.Contains(strings.ToLower(binding.EntryFileName), query) {
			continue
		}
		matches = append(matches, cloneBinding(binding))
		if len(matches) >= limit {
			break
		}
	}
	sortBindings(matches)
	return matches
}

func (i *Index) Generation(arrName string) uint64 {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.generations[arrName]
}

func (i *Index) Upsert(binding Binding) error {
	if err := binding.validate(); err != nil {
		return fmt.Errorf("invalid arr binding: %w", err)
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	i.upsertLocked(cloneBinding(binding))
	return nil
}

func (i *Index) DeleteEntryFile(entryID, fileID string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.removeLocked(entryFileKey{entryID: entryID, fileID: fileID})
}

func (i *Index) ReplaceArrGeneration(arrName string, generation uint64, bindings []Binding) error {
	if arrName == "" {
		return errors.New("arr name is required")
	}
	prepared := make([]Binding, len(bindings))
	for i, binding := range bindings {
		if binding.ArrName != "" && binding.ArrName != arrName {
			return fmt.Errorf("binding %q belongs to arr %q", binding.EntryFileID, binding.ArrName)
		}
		binding.ArrName = arrName
		binding.Generation = generation
		if err := binding.validate(); err != nil {
			return fmt.Errorf("invalid arr binding: %w", err)
		}
		prepared[i] = cloneBinding(binding)
	}
	if err := validateUniqueArrFiles(prepared); err != nil {
		return err
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	for key := range i.byArr[arrName] {
		i.removeLocked(key)
	}
	for _, binding := range prepared {
		i.upsertLocked(binding)
	}
	i.generations[arrName] = generation
	return nil
}

func validateUniqueArrFiles(bindings []Binding) error {
	seen := make(map[arrFileKey]entryFileKey, len(bindings))
	for _, binding := range bindings {
		if binding.ArrFileID <= 0 {
			continue
		}
		arrKey := arrFileKey{arrName: binding.ArrName, fileID: binding.ArrFileID}
		entryKey := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
		if previous, exists := seen[arrKey]; exists && previous != entryKey {
			return fmt.Errorf("arr file %d in %q maps to multiple managed files", binding.ArrFileID, binding.ArrName)
		}
		seen[arrKey] = entryKey
	}
	return nil
}

func (i *Index) replaceAll(bindings []Binding) error {
	for _, binding := range bindings {
		if err := binding.validate(); err != nil {
			return fmt.Errorf("invalid arr binding: %w", err)
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	clear(i.bindings)
	clear(i.byArr)
	clear(i.byArrFile)
	clear(i.byDownload)
	clear(i.byEpisode)
	clear(i.byMovie)
	clear(i.bySeries)
	clear(i.generations)
	for _, binding := range bindings {
		i.upsertLocked(cloneBinding(binding))
	}
	return nil
}

// internBinding replaces the fields every binding of an Arr repeats with one
// shared copy. A decode hands each binding its own copy of the Arr name, type,
// instance fingerprint, and confidence, which on a large library is hundreds of
// megabytes of identical strings.
func internBinding(binding Binding) Binding {
	binding.ArrName = unique.Make(binding.ArrName).Value()
	binding.ArrType = unique.Make(binding.ArrType).Value()
	binding.ArrInstanceFingerprint = unique.Make(binding.ArrInstanceFingerprint).Value()
	binding.Confidence = unique.Make(binding.Confidence).Value()
	return binding
}

func (i *Index) upsertLocked(binding Binding) {
	binding = internBinding(binding)
	key := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
	i.removeLocked(key)
	if binding.ArrFileID != 0 {
		arrKey := arrFileKey{arrName: binding.ArrName, fileID: binding.ArrFileID}
		if previous, exists := i.byArrFile[arrKey]; exists && previous != key {
			i.removeLocked(previous)
		}
	}

	i.bindings[key] = binding
	addIndexValue(i.byArr, binding.ArrName, key)
	if binding.ArrFileID != 0 {
		i.byArrFile[arrFileKey{arrName: binding.ArrName, fileID: binding.ArrFileID}] = key
	}
	if binding.DownloadID != "" {
		addIndexKey(i.byDownload, downloadKey{arrName: binding.ArrName, downloadID: binding.DownloadID}, key)
	}
	for _, episodeID := range binding.EpisodeIDs {
		if episodeID != 0 {
			addIndexKey(i.byEpisode, mediaKey{arrName: binding.ArrName, mediaID: episodeID}, key)
		}
	}
	if binding.MovieID != 0 {
		addIndexKey(i.byMovie, mediaKey{arrName: binding.ArrName, mediaID: binding.MovieID}, key)
	}
	if binding.SeriesID != 0 {
		addIndexValue(i.bySeries, mediaKey{arrName: binding.ArrName, mediaID: binding.SeriesID}, key)
	}
	i.generations[binding.ArrName] = max(i.generations[binding.ArrName], binding.Generation)
}

func (i *Index) removeLocked(key entryFileKey) bool {
	binding, ok := i.bindings[key]
	if !ok {
		return false
	}
	delete(i.bindings, key)
	removeIndexValue(i.byArr, binding.ArrName, key)
	if binding.ArrFileID != 0 {
		delete(i.byArrFile, arrFileKey{arrName: binding.ArrName, fileID: binding.ArrFileID})
	}
	if binding.DownloadID != "" {
		removeIndexKey(i.byDownload, downloadKey{arrName: binding.ArrName, downloadID: binding.DownloadID}, key)
	}
	for _, episodeID := range binding.EpisodeIDs {
		if episodeID != 0 {
			removeIndexKey(i.byEpisode, mediaKey{arrName: binding.ArrName, mediaID: episodeID}, key)
		}
	}
	if binding.MovieID != 0 {
		removeIndexKey(i.byMovie, mediaKey{arrName: binding.ArrName, mediaID: binding.MovieID}, key)
	}
	if binding.SeriesID != 0 {
		removeIndexValue(i.bySeries, mediaKey{arrName: binding.ArrName, mediaID: binding.SeriesID}, key)
	}
	return true
}

func (i *Index) bindingsForKeysLocked(keys []entryFileKey) []Binding {
	bindings := make([]Binding, 0, len(keys))
	for _, key := range keys {
		if binding, ok := i.bindings[key]; ok {
			bindings = append(bindings, cloneBinding(binding))
		}
	}
	sortBindings(bindings)
	return bindings
}

func (i *Index) bindingsForLocked(keys map[entryFileKey]struct{}) []Binding {
	bindings := make([]Binding, 0, len(keys))
	for key := range keys {
		if binding, ok := i.bindings[key]; ok {
			bindings = append(bindings, cloneBinding(binding))
		}
	}
	sortBindings(bindings)
	return bindings
}

func addIndexKey[K comparable](values map[K][]entryFileKey, indexKey K, bindingKey entryFileKey) {
	keys := values[indexKey]
	if slices.Contains(keys, bindingKey) {
		return
	}
	values[indexKey] = append(keys, bindingKey)
}

func removeIndexKey[K comparable](values map[K][]entryFileKey, indexKey K, bindingKey entryFileKey) {
	keys := values[indexKey]
	at := slices.Index(keys, bindingKey)
	if at < 0 {
		return
	}
	keys = slices.Delete(keys, at, at+1)
	if len(keys) == 0 {
		delete(values, indexKey)
		return
	}
	values[indexKey] = keys
}

func addIndexValue[K comparable](values map[K]map[entryFileKey]struct{}, indexKey K, bindingKey entryFileKey) {
	keys := values[indexKey]
	if keys == nil {
		keys = make(map[entryFileKey]struct{})
		values[indexKey] = keys
	}
	keys[bindingKey] = struct{}{}
}

func removeIndexValue[K comparable](values map[K]map[entryFileKey]struct{}, indexKey K, bindingKey entryFileKey) {
	keys := values[indexKey]
	delete(keys, bindingKey)
	if len(keys) == 0 {
		delete(values, indexKey)
	}
}

func sortBindings(bindings []Binding) {
	slices.SortFunc(bindings, func(left, right Binding) int {
		if result := cmp.Compare(left.ArrName, right.ArrName); result != 0 {
			return result
		}
		if result := cmp.Compare(left.EntryID, right.EntryID); result != 0 {
			return result
		}
		return cmp.Compare(left.EntryFileID, right.EntryFileID)
	})
}
