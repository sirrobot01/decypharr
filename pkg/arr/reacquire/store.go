package reacquire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/appendstore"
)

const (
	bindingAttributeArrName  = "arrName"
	bindingManifestKeyPrefix = "arr-binding-manifest:"
	bindingPageKeyPrefix     = "arr-binding-page:"
	bindingDeltaKeyPrefix    = "arr-binding-delta:"
	bindingSnapshotKeyPrefix = "arr-binding-snapshot:"
	bindingSnapshotVersion   = 1
	// bindingPageSize bounds one stored row. A library can hold hundreds of
	// thousands of bindings, and a single row would be encoded, held, and
	// written whole.
	bindingPageSize     = 5000
	jobAttributeArrName = "arrName"
	jobAttributeStatus  = "status"
)

type bindingRepositoryStore interface {
	ForEach(func(string, []byte) error) error
	Put(string, []byte, *appendstore.PutOptions) error
	Delete(string) error
	Sync() error
	Close() error
}

// BindingRepository persists bindings as a paged snapshot per Arr plus a delta
// row per targeted change. A full reconciliation writes the pages and then a
// manifest naming them, which is the commit point; single upserts write one
// delta row, because rewriting the snapshot per file does not scale.
type BindingRepository struct {
	mu     sync.RWMutex
	store  bindingRepositoryStore
	state  bindingRepositoryState
	loaded bool
}

// bindingManifest names the pages that make up one Arr's committed snapshot.
// It is written last, so a crash mid-write leaves the previous generation.
type bindingManifest struct {
	Version    int    `json:"version"`
	ArrName    string `json:"arrName"`
	Generation uint64 `json:"generation"`
	Pages      int    `json:"pages"`
	Bindings   int    `json:"bindings"`
}

type bindingPage struct {
	Version    int       `json:"version"`
	ArrName    string    `json:"arrName"`
	Generation uint64    `json:"generation"`
	Page       int       `json:"page"`
	Bindings   []Binding `json:"bindings"`
}

// bindingSnapshot is the single-row format paged snapshots replaced. It is
// still read so an index written by an older build survives an upgrade.
type bindingSnapshot struct {
	Version    int       `json:"version"`
	ArrName    string    `json:"arrName"`
	Generation uint64    `json:"generation"`
	Bindings   []Binding `json:"bindings"`
}

// bindingDelta is one change made since its arr.Arr's snapshot was written.
type bindingDelta struct {
	Version     int      `json:"version"`
	ArrName     string   `json:"arrName"`
	EntryID     string   `json:"entryId"`
	EntryFileID string   `json:"entryFileId"`
	Generation  uint64   `json:"generation,omitzero"`
	Deleted     bool     `json:"deleted,omitempty"`
	Binding     *Binding `json:"binding,omitempty"`
}

// bindingRepositoryState indexes what is on disk. It never retains binding
// payloads: the in-memory Index already holds those.
type bindingRepositoryState struct {
	owners      map[entryFileKey]string
	generations map[string]uint64
	deltas      map[entryFileKey]struct{}
	legacy      map[string]map[entryFileKey]struct{}
	stored      map[string][]storedPage
}

// storedPage is one snapshot row on disk, kept so the generation that replaces
// it can delete it.
type storedPage struct {
	key        string
	generation uint64
}

func OpenBindingRepository(path string) (*BindingRepository, error) {
	store, err := openArrStore(path, []string{bindingAttributeArrName})
	if err != nil {
		return nil, fmt.Errorf("open arr binding repository: %w", err)
	}
	return &BindingRepository{store: store}, nil
}

func (r *BindingRepository) LoadAll() ([]Binding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	bindings, state, err := r.scanLocked()
	if err != nil {
		return nil, err
	}
	r.state = state
	r.loaded = true
	sortBindings(bindings)
	return bindings, nil
}

func (r *BindingRepository) Save(binding Binding) error {
	if err := binding.validate(); err != nil {
		return fmt.Errorf("save arr binding: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := r.stateLocked()
	if err != nil {
		return err
	}
	key := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
	if owner, exists := state.owners[key]; exists && owner != binding.ArrName {
		return fmt.Errorf("save arr binding: managed file already belongs to arr %q", owner)
	}

	stored := cloneBinding(binding)
	delta := bindingDelta{
		Version:     bindingSnapshotVersion,
		ArrName:     binding.ArrName,
		EntryID:     binding.EntryID,
		EntryFileID: binding.EntryFileID,
		Generation:  binding.Generation,
		Binding:     &stored,
	}
	if err := r.persistDeltaLocked(delta); err != nil {
		return err
	}
	state.owners[key] = binding.ArrName
	state.deltas[key] = struct{}{}
	state.generations[binding.ArrName] = max(state.generations[binding.ArrName], binding.Generation)
	return nil
}

func (r *BindingRepository) Delete(entryID, fileID string) error {
	if entryID == "" || fileID == "" {
		return errors.New("entry ID and file ID are required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := r.stateLocked()
	if err != nil {
		return err
	}
	key := entryFileKey{entryID: entryID, fileID: fileID}
	owner, exists := state.owners[key]
	if !exists {
		return nil
	}
	delta := bindingDelta{
		Version:     bindingSnapshotVersion,
		ArrName:     owner,
		EntryID:     entryID,
		EntryFileID: fileID,
		Generation:  state.generations[owner],
		Deleted:     true,
	}
	if err := r.persistDeltaLocked(delta); err != nil {
		return err
	}
	delete(state.owners, key)
	state.deltas[key] = struct{}{}
	return nil
}

func (r *BindingRepository) ReplaceArrGeneration(arrName string, generation uint64, bindings []Binding) error {
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
			return fmt.Errorf("replace arr bindings: %w", err)
		}
		prepared[i] = cloneBinding(binding)
	}
	if err := validateUniqueArrFiles(prepared); err != nil {
		return err
	}
	if err := validateUniqueManagedFiles(prepared); err != nil {
		return fmt.Errorf("replace arr bindings: %w", err)
	}
	sortBindings(prepared)

	r.mu.Lock()
	defer r.mu.Unlock()

	state, err := r.stateLocked()
	if err != nil {
		return err
	}
	for _, binding := range prepared {
		bindingKey := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
		if owner, exists := state.owners[bindingKey]; exists && owner != arrName {
			return fmt.Errorf("replace arr bindings: managed file already belongs to arr %q", owner)
		}
	}

	options := &appendstore.PutOptions{Attributes: map[string]string{
		bindingAttributeArrName: arrName,
		"generation":            strconv.FormatUint(generation, 10),
	}}
	written := make([]storedPage, 0, len(prepared)/bindingPageSize+1)
	index := 0
	for chunk := range slices.Chunk(prepared, bindingPageSize) {
		page := bindingPage{
			Version:    bindingSnapshotVersion,
			ArrName:    arrName,
			Generation: generation,
			Page:       index,
			Bindings:   chunk,
		}
		data, err := json.Marshal(page)
		if err != nil {
			return fmt.Errorf("encode arr binding page: %w", err)
		}
		key := bindingPageStoreKey(arrName, generation, index)
		if err := r.store.Put(key, data, options); err != nil {
			return fmt.Errorf("persist arr binding page: %w", err)
		}
		written = append(written, storedPage{key: key, generation: generation})
		index++
	}
	// The pages must be durable before the manifest names them, or a crash
	// could leave a manifest pointing at a page that is not there.
	if err := r.store.Sync(); err != nil {
		r.invalidateLocked()
		return fmt.Errorf("sync arr binding pages: %w", err)
	}

	manifest := bindingManifest{
		Version:    bindingSnapshotVersion,
		ArrName:    arrName,
		Generation: generation,
		Pages:      len(written),
		Bindings:   len(prepared),
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode arr binding manifest: %w", err)
	}
	if err := r.store.Put(bindingManifestStoreKey(arrName), data, options); err != nil {
		return fmt.Errorf("persist arr binding manifest: %w", err)
	}
	if err := r.store.Sync(); err != nil {
		r.invalidateLocked()
		return fmt.Errorf("sync arr binding manifest: %w", err)
	}

	// The generation is committed from here on, so the rows it replaces are
	// dropped. A crash in between leaves rows the loader ignores.
	r.dropSupersededRowsLocked(state, arrName, prepared)
	state.stored[arrName] = written
	state.generations[arrName] = generation
	return nil
}

func (r *BindingRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.store.Close()
}

func (r *BindingRepository) stateLocked() (*bindingRepositoryState, error) {
	if r.loaded {
		return &r.state, nil
	}
	_, state, err := r.scanLocked()
	if err != nil {
		return nil, err
	}
	r.state = state
	r.loaded = true
	return &r.state, nil
}

func (r *BindingRepository) invalidateLocked() {
	r.state = bindingRepositoryState{}
	r.loaded = false
}

// scanLocked reads the store once and merges the three row kinds: a snapshot is
// authoritative for its arr.Arr, deltas written after it win per managed file, and
// legacy rows only apply to an arr.Arr that has no snapshot yet.
func (r *BindingRepository) scanLocked() ([]Binding, bindingRepositoryState, error) {
	manifests := make(map[string]bindingManifest)
	pages := make(map[string][]bindingPage)
	deltas := make(map[entryFileKey]bindingDelta)
	legacy := make(map[string][]Binding)
	stored := make(map[string][]storedPage)

	if err := r.store.ForEach(func(key string, value []byte) error {
		switch {
		case strings.HasPrefix(key, bindingManifestKeyPrefix):
			var manifest bindingManifest
			if err := json.Unmarshal(value, &manifest); err != nil {
				return fmt.Errorf("decode arr binding manifest %q: %w", key, err)
			}
			if err := validateBindingManifest(key, manifest); err != nil {
				return fmt.Errorf("decode arr binding manifest %q: %w", key, err)
			}
			manifests[manifest.ArrName] = manifest
		case strings.HasPrefix(key, bindingPageKeyPrefix):
			var page bindingPage
			if err := json.Unmarshal(value, &page); err != nil {
				return fmt.Errorf("decode arr binding page %q: %w", key, err)
			}
			if err := validateBindingPage(key, page); err != nil {
				return fmt.Errorf("decode arr binding page %q: %w", key, err)
			}
			pages[page.ArrName] = append(pages[page.ArrName], page)
			stored[page.ArrName] = append(stored[page.ArrName], storedPage{key: key, generation: page.Generation})
		case strings.HasPrefix(key, bindingSnapshotKeyPrefix):
			// The single-row format an older build wrote.
			var snapshot bindingSnapshot
			if err := json.Unmarshal(value, &snapshot); err != nil {
				return fmt.Errorf("decode arr binding snapshot %q: %w", key, err)
			}
			if err := validateBindingSnapshot(key, snapshot); err != nil {
				return fmt.Errorf("decode arr binding snapshot %q: %w", key, err)
			}
			pages[snapshot.ArrName] = append(pages[snapshot.ArrName], bindingPage{
				Version:    snapshot.Version,
				ArrName:    snapshot.ArrName,
				Generation: snapshot.Generation,
				Bindings:   snapshot.Bindings,
			})
			stored[snapshot.ArrName] = append(stored[snapshot.ArrName], storedPage{key: key, generation: snapshot.Generation})
		case strings.HasPrefix(key, bindingDeltaKeyPrefix):
			var delta bindingDelta
			if err := json.Unmarshal(value, &delta); err != nil {
				return fmt.Errorf("decode arr binding delta %q: %w", key, err)
			}
			if err := validateBindingDelta(key, delta); err != nil {
				return fmt.Errorf("decode arr binding delta %q: %w", key, err)
			}
			deltas[entryFileKey{entryID: delta.EntryID, fileID: delta.EntryFileID}] = delta
		default:
			var binding Binding
			if err := json.Unmarshal(value, &binding); err != nil {
				return fmt.Errorf("decode legacy arr binding %q: %w", key, err)
			}
			if err := binding.validate(); err != nil {
				return fmt.Errorf("decode legacy arr binding %q: %w", key, err)
			}
			legacy[binding.ArrName] = append(legacy[binding.ArrName], binding)
		}
		return nil
	}); err != nil {
		return nil, bindingRepositoryState{}, err
	}

	state := bindingRepositoryState{
		owners:      make(map[entryFileKey]string),
		generations: make(map[string]uint64, len(pages)),
		deltas:      make(map[entryFileKey]struct{}, len(deltas)),
		legacy:      make(map[string]map[entryFileKey]struct{}, len(legacy)),
		stored:      stored,
	}
	merged := make(map[entryFileKey]Binding)
	committed := make(map[string]uint64, len(pages))

	for arrName, arrPages := range pages {
		generation, ok := committedGeneration(arrName, manifests, arrPages)
		if !ok {
			// Pages with no manifest, or too few for the one on disk: an
			// interrupted write, which the previous generation still covers.
			continue
		}
		committed[arrName] = generation
		state.generations[arrName] = generation
		for _, page := range arrPages {
			if page.Generation != generation {
				continue
			}
			for _, binding := range page.Bindings {
				merged[entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}] = binding
			}
		}
	}

	for arrName, bindings := range legacy {
		keys := make(map[entryFileKey]struct{}, len(bindings))
		for _, binding := range bindings {
			keys[entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}] = struct{}{}
		}
		state.legacy[arrName] = keys
		if _, superseded := committed[arrName]; superseded {
			continue
		}
		for _, binding := range bindings {
			key := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
			merged[key] = binding
			state.generations[arrName] = max(state.generations[arrName], binding.Generation)
		}
	}

	for key, delta := range deltas {
		state.deltas[key] = struct{}{}
		if delta.Generation < committed[delta.ArrName] {
			continue // superseded by a generation written after this delta
		}
		if delta.Deleted {
			delete(merged, key)
			continue
		}
		merged[key] = *delta.Binding
		state.generations[delta.ArrName] = max(state.generations[delta.ArrName], delta.Generation)
	}

	bindings := make([]Binding, 0, len(merged))
	for key, binding := range merged {
		if owner, exists := state.owners[key]; exists {
			return nil, bindingRepositoryState{}, fmt.Errorf("managed file belongs to both arr %q and %q", owner, binding.ArrName)
		}
		state.owners[key] = binding.ArrName
		bindings = append(bindings, cloneBinding(binding))
	}
	return bindings, state, nil
}

// committedGeneration reports the newest generation whose pages are all on
// disk. A manifest names how many pages its generation has; a snapshot an
// older build wrote is a single page with no manifest.
func committedGeneration(arrName string, manifests map[string]bindingManifest, pages []bindingPage) (uint64, bool) {
	if manifest, ok := manifests[arrName]; ok {
		found := 0
		for _, page := range pages {
			if page.Generation == manifest.Generation {
				found++
			}
		}
		if found != manifest.Pages {
			return 0, false
		}
		return manifest.Generation, true
	}

	newest := uint64(0)
	found := false
	for _, page := range pages {
		if page.Page != 0 {
			continue // a paged generation is only valid with its manifest
		}
		if !found || page.Generation > newest {
			newest = page.Generation
			found = true
		}
	}
	return newest, found
}

func (r *BindingRepository) persistDeltaLocked(delta bindingDelta) error {
	key := bindingDeltaStoreKey(delta.EntryID, delta.EntryFileID)
	if err := validateBindingDelta(key, delta); err != nil {
		return fmt.Errorf("persist arr binding delta: %w", err)
	}
	data, err := json.Marshal(delta)
	if err != nil {
		return fmt.Errorf("encode arr binding delta: %w", err)
	}
	options := &appendstore.PutOptions{Attributes: map[string]string{
		bindingAttributeArrName: delta.ArrName,
	}}
	if err := r.store.Put(key, data, options); err != nil {
		return fmt.Errorf("persist arr binding delta: %w", err)
	}
	if err := r.store.Sync(); err != nil {
		r.invalidateLocked()
		return fmt.Errorf("sync arr binding delta: %w", err)
	}
	return nil
}

// dropSupersededRowsLocked removes the delta and legacy rows a fresh snapshot
// replaces. Failures are not fatal: the loader ignores stale rows.
// dropSupersededRowsLocked removes the pages, deltas, and legacy rows a fresh
// generation replaces. Failures are not fatal: the loader ignores stale rows.
func (r *BindingRepository) dropSupersededRowsLocked(state *bindingRepositoryState, arrName string, bindings []Binding) {
	for _, page := range state.stored[arrName] {
		if err := r.store.Delete(page.key); err != nil {
			continue
		}
	}
	delete(state.stored, arrName)

	for key := range state.legacy[arrName] {
		if err := r.store.Delete(bindingStoreKey(key.entryID, key.fileID)); err == nil {
			delete(state.owners, key)
		}
	}
	delete(state.legacy, arrName)

	for key := range state.deltas {
		if state.owners[key] != arrName {
			continue
		}
		if err := r.store.Delete(bindingDeltaStoreKey(key.entryID, key.fileID)); err != nil {
			continue
		}
		delete(state.deltas, key)
	}
	for key, owner := range state.owners {
		if owner == arrName {
			delete(state.owners, key)
		}
	}
	for _, binding := range bindings {
		key := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
		if _, stale := state.deltas[key]; stale {
			if err := r.store.Delete(bindingDeltaStoreKey(key.entryID, key.fileID)); err == nil {
				delete(state.deltas, key)
			}
		}
		state.owners[key] = arrName
	}
}

func validateBindingManifest(key string, manifest bindingManifest) error {
	switch {
	case manifest.Version != bindingSnapshotVersion:
		return fmt.Errorf("unsupported version %d", manifest.Version)
	case manifest.ArrName == "":
		return errors.New("arr name is required")
	case key != bindingManifestStoreKey(manifest.ArrName):
		return errors.New("manifest key does not match arr name")
	case manifest.Pages < 0 || manifest.Bindings < 0:
		return errors.New("manifest counts are negative")
	default:
		return nil
	}
}

func validateBindingPage(key string, page bindingPage) error {
	switch {
	case page.Version != bindingSnapshotVersion:
		return fmt.Errorf("unsupported version %d", page.Version)
	case page.ArrName == "":
		return errors.New("arr name is required")
	case key != bindingPageStoreKey(page.ArrName, page.Generation, page.Page):
		return errors.New("page key does not match its arr, generation, and index")
	}
	return validatePageBindings(page.ArrName, page.Generation, page.Bindings)
}

func validateBindingSnapshot(key string, snapshot bindingSnapshot) error {
	switch {
	case snapshot.Version != bindingSnapshotVersion:
		return fmt.Errorf("unsupported version %d", snapshot.Version)
	case snapshot.ArrName == "":
		return errors.New("arr name is required")
	case key != bindingSnapshotStoreKey(snapshot.ArrName):
		return errors.New("snapshot key does not match arr name")
	}
	if err := validatePageBindings(snapshot.ArrName, snapshot.Generation, snapshot.Bindings); err != nil {
		return err
	}
	return validateUniqueManagedFiles(snapshot.Bindings)
}

func validatePageBindings(arrName string, generation uint64, bindings []Binding) error {
	for _, binding := range bindings {
		if err := binding.validate(); err != nil {
			return err
		}
		if binding.ArrName != arrName {
			return fmt.Errorf("binding belongs to arr %q", binding.ArrName)
		}
		if binding.Generation > generation {
			return fmt.Errorf("binding generation %d exceeds generation %d", binding.Generation, generation)
		}
	}
	return nil
}

func validateUniqueManagedFiles(bindings []Binding) error {
	seen := make(map[entryFileKey]struct{}, len(bindings))
	for _, binding := range bindings {
		key := entryFileKey{entryID: binding.EntryID, fileID: binding.EntryFileID}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate managed file %q/%q", binding.EntryID, binding.EntryFileID)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateBindingDelta(key string, delta bindingDelta) error {
	switch {
	case delta.Version != bindingSnapshotVersion:
		return fmt.Errorf("unsupported version %d", delta.Version)
	case delta.ArrName == "":
		return errors.New("arr name is required")
	case key != bindingDeltaStoreKey(delta.EntryID, delta.EntryFileID):
		return errors.New("delta key does not match the managed file")
	case delta.Deleted:
		return nil
	case delta.Binding == nil:
		return errors.New("delta binding is required")
	}
	if err := delta.Binding.validate(); err != nil {
		return err
	}
	if delta.Binding.ArrName != delta.ArrName ||
		delta.Binding.EntryID != delta.EntryID ||
		delta.Binding.EntryFileID != delta.EntryFileID {
		return errors.New("delta binding identity does not match the delta")
	}
	return nil
}

func openArrStore(path string, indexedFields []string) (*appendstore.Store, error) {
	if path == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	return appendstore.Open(path, appendstore.Options{
		CacheSize:           1000,
		SyncInterval:        time.Second,
		CompactionThreshold: 0.5,
		AutoCompact:         true,
		IndexedFields:       indexedFields,
	})
}

func bindingStoreKey(entryID, fileID string) string {
	hash := sha256.New()
	hash.Write([]byte(entryID))
	hash.Write([]byte{0})
	hash.Write([]byte(fileID))
	return hex.EncodeToString(hash.Sum(nil))
}

func bindingDeltaStoreKey(entryID, fileID string) string {
	return bindingDeltaKeyPrefix + bindingStoreKey(entryID, fileID)
}

func bindingSnapshotStoreKey(arrName string) string {
	return bindingSnapshotKeyPrefix + arrNameHash(arrName)
}

func bindingManifestStoreKey(arrName string) string {
	return bindingManifestKeyPrefix + arrNameHash(arrName)
}

func bindingPageStoreKey(arrName string, generation uint64, page int) string {
	return fmt.Sprintf("%s%s:%d:%d", bindingPageKeyPrefix, arrNameHash(arrName), generation, page)
}

func arrNameHash(arrName string) string {
	hash := sha256.Sum256([]byte(arrName))
	return hex.EncodeToString(hash[:])
}

func deleteStoreKey(store *appendstore.Store, key string) error {
	if err := store.Delete(key); err != nil && !errors.Is(err, appendstore.ErrKeyNotFound) {
		return err
	}
	return nil
}
