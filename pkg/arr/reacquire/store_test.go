package reacquire

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sirrobot01/appendstore"
)

var errBindingSnapshotPut = errors.New("binding snapshot put failed")

type observedBindingStore struct {
	bindingRepositoryStore
	failNextPut bool
	forEach     int
	syncs       int
	puts        []string
	trace       []string
	largestPut  int
}

func (s *observedBindingStore) ForEach(yield func(string, []byte) error) error {
	s.forEach++
	return s.bindingRepositoryStore.ForEach(yield)
}

func (s *observedBindingStore) Put(key string, value []byte, options *appendstore.PutOptions) error {
	if s.failNextPut {
		s.failNextPut = false
		return errBindingSnapshotPut
	}
	s.puts = append(s.puts, key)
	s.trace = append(s.trace, "put "+key)
	s.largestPut = max(s.largestPut, len(value))
	return s.bindingRepositoryStore.Put(key, value, options)
}

func (s *observedBindingStore) Sync() error {
	s.syncs++
	s.trace = append(s.trace, "sync")
	return s.bindingRepositoryStore.Sync()
}

func TestBindingRepositoryFailedSnapshotPutKeepsCommittedGeneration(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	original := repositoryTestBinding("radarr", "old-entry", "old-file", 1)
	if err := repository.ReplaceArrGeneration("radarr", 1, []Binding{original}); err != nil {
		t.Fatal(err)
	}

	store := &observedBindingStore{bindingRepositoryStore: repository.store, failNextPut: true}
	repository.store = store
	replacement := repositoryTestBinding("radarr", "new-entry", "new-file", 2)
	if err := repository.ReplaceArrGeneration("radarr", 2, []Binding{replacement}); !errors.Is(err, errBindingSnapshotPut) {
		t.Fatalf("ReplaceArrGeneration error = %v, want %v", err, errBindingSnapshotPut)
	}
	assertSingleRepositoryBinding(t, repository, original)
	if store.syncs != 0 {
		t.Fatalf("Sync calls after failed Put = %d, want 0", store.syncs)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository = openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	assertSingleRepositoryBinding(t, repository, original)
}

func TestBindingRepositoryCommitsPagesBeforeTheManifest(t *testing.T) {
	repository := openTestBindingRepository(t, t.TempDir()+"/bindings.db")
	t.Cleanup(func() { _ = repository.Close() })
	store := &observedBindingStore{bindingRepositoryStore: repository.store}
	repository.store = store

	bindings := make([]Binding, bindingPageSize+1)
	for i := range bindings {
		bindings[i] = repositoryTestBinding("sonarr", fmt.Sprintf("entry-%d", i), fmt.Sprintf("file-%d", i), 4)
	}
	if err := repository.ReplaceArrGeneration("sonarr", 4, bindings); err != nil {
		t.Fatal(err)
	}

	manifest := "put " + bindingManifestStoreKey("sonarr")
	manifestAt := slices.Index(store.trace, manifest)
	if manifestAt < 0 {
		t.Fatalf("trace = %v, want a manifest write", store.trace)
	}
	pages := 0
	for _, event := range store.trace[:manifestAt] {
		if strings.HasPrefix(event, "put "+bindingPageKeyPrefix) {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("pages written before the manifest = %d, want 2", pages)
	}
	// Every page must be durable before the manifest names it.
	if store.trace[manifestAt-1] != "sync" {
		t.Fatalf("trace = %v, want a sync before the manifest", store.trace)
	}
	if store.trace[len(store.trace)-1] != "sync" {
		t.Fatalf("trace = %v, want the manifest synced", store.trace)
	}
}

// A page is one row, so a large generation must not be one large write.
func TestBindingRepositoryPagesLargeGenerations(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	bindings := make([]Binding, bindingPageSize*2+3)
	for i := range bindings {
		bindings[i] = repositoryTestBinding("radarr", fmt.Sprintf("entry-%d", i), fmt.Sprintf("file-%d", i), 1)
	}
	if err := repository.ReplaceArrGeneration("radarr", 1, bindings); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository = openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	loaded, err := repository.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != len(bindings) {
		t.Fatalf("loaded %d bindings, want %d", len(loaded), len(bindings))
	}
	if pages := len(repository.state.stored["radarr"]); pages != 3 {
		t.Fatalf("stored pages = %d, want 3", pages)
	}
}

// An interrupted write leaves pages with no manifest; the committed generation
// must survive it.
func TestBindingRepositoryIgnoresUncommittedPages(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	committed := repositoryTestBinding("sonarr", "entry-1", "file-1", 1)
	if err := repository.ReplaceArrGeneration("sonarr", 1, []Binding{committed}); err != nil {
		t.Fatal(err)
	}

	orphan := bindingPage{
		Version:    bindingSnapshotVersion,
		ArrName:    "sonarr",
		Generation: 2,
		Bindings:   []Binding{repositoryTestBinding("sonarr", "entry-2", "file-2", 2)},
	}
	data, err := json.Marshal(orphan)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.store.Put(bindingPageStoreKey("sonarr", 2, 0), data, nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository = openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	assertSingleRepositoryBinding(t, repository, committed)
}

func TestBindingRepositoryRejectsCorruptAuthoritativeSnapshot(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	legacy := repositoryTestBinding("radarr", "entry", "file", 1)
	putLegacyBinding(t, repository.store, legacy)
	if err := repository.store.Put(bindingSnapshotStoreKey("radarr"), []byte("{"), nil); err != nil {
		t.Fatal(err)
	}
	if err := repository.store.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	repository = openTestBindingRepository(t, path)

	if _, err := repository.LoadAll(); err == nil {
		t.Fatal("LoadAll accepted a corrupt authoritative snapshot")
	}
}

func TestBindingRepositoryMigratesLegacyRowsOnMutation(t *testing.T) {
	t.Run("save", func(t *testing.T) {
		path := t.TempDir() + "/bindings.db"
		repository := openTestBindingRepository(t, path)
		first := repositoryTestBinding("sonarr", "entry-1", "file-1", 1)
		second := repositoryTestBinding("sonarr", "entry-2", "file-2", 1)
		putLegacyBinding(t, repository.store, first)
		putLegacyBinding(t, repository.store, second)

		bindings, err := repository.LoadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 2 {
			t.Fatalf("legacy binding count = %d, want 2", len(bindings))
		}
		first.EntryFileName = "updated.mkv"
		first.Generation = 2
		if err := repository.Save(first); err != nil {
			t.Fatal(err)
		}

		poisoned := first
		poisoned.EntryFileName = "legacy-row-should-be-ignored.mkv"
		poisoned.Generation = 99
		putLegacyBinding(t, repository.store, poisoned)
		if err := repository.Close(); err != nil {
			t.Fatal(err)
		}

		repository = openTestBindingRepository(t, path)
		t.Cleanup(func() { _ = repository.Close() })
		bindings, err = repository.LoadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 2 {
			t.Fatalf("migrated binding count = %d, want 2", len(bindings))
		}
		if bindings[0].EntryFileName != "updated.mkv" || bindings[0].Generation != 2 {
			t.Fatalf("migrated binding = %#v", bindings[0])
		}
	})

	t.Run("delete", func(t *testing.T) {
		path := t.TempDir() + "/bindings.db"
		repository := openTestBindingRepository(t, path)
		binding := repositoryTestBinding("radarr", "entry", "file", 1)
		putLegacyBinding(t, repository.store, binding)
		if err := repository.Delete(binding.EntryID, binding.EntryFileID); err != nil {
			t.Fatal(err)
		}
		if err := repository.Close(); err != nil {
			t.Fatal(err)
		}

		repository = openTestBindingRepository(t, path)
		t.Cleanup(func() { _ = repository.Close() })
		bindings, err := repository.LoadAll()
		if err != nil {
			t.Fatal(err)
		}
		if len(bindings) != 0 {
			t.Fatalf("bindings after migrated delete = %#v", bindings)
		}
	})
}

func TestBindingRepositoryCachesStateAcrossTargetedSaves(t *testing.T) {
	repository := openTestBindingRepository(t, t.TempDir()+"/bindings.db")
	t.Cleanup(func() { _ = repository.Close() })
	store := &observedBindingStore{bindingRepositoryStore: repository.store}
	repository.store = store

	if _, err := repository.LoadAll(); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(repositoryTestBinding("radarr", "entry-1", "file-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(repositoryTestBinding("radarr", "entry-2", "file-2", 1)); err != nil {
		t.Fatal(err)
	}
	if store.forEach != 1 {
		t.Fatalf("ForEach calls = %d, want 1", store.forEach)
	}
	bindings, err := repository.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("binding count = %d, want 2", len(bindings))
	}
}

func openTestBindingRepository(t *testing.T, path string) *BindingRepository {
	t.Helper()
	repository, err := OpenBindingRepository(path)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func putLegacyBinding(t *testing.T, store bindingRepositoryStore, binding Binding) {
	t.Helper()
	data, err := json.Marshal(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(bindingStoreKey(binding.EntryID, binding.EntryFileID), data, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.Sync(); err != nil {
		t.Fatal(err)
	}
}

func repositoryTestBinding(arrName, entryID, fileID string, generation uint64) Binding {
	return Binding{
		ArrName:     arrName,
		EntryID:     entryID,
		EntryFileID: fileID,
		Generation:  generation,
	}
}

func assertSingleRepositoryBinding(t *testing.T, repository *BindingRepository, want Binding) {
	t.Helper()
	bindings, err := repository.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 1 || bindings[0].EntryID != want.EntryID ||
		bindings[0].EntryFileID != want.EntryFileID || bindings[0].Generation != want.Generation {
		t.Fatalf("bindings = %#v, want %#v", bindings, want)
	}
}

func TestBindingRepositorySaveWritesOneRow(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	baseline := []Binding{
		repositoryTestBinding("sonarr", "entry-1", "file-1", 1),
		repositoryTestBinding("sonarr", "entry-2", "file-2", 1),
	}
	if err := repository.ReplaceArrGeneration("sonarr", 1, baseline); err != nil {
		t.Fatal(err)
	}

	store := &observedBindingStore{bindingRepositoryStore: repository.store}
	repository.store = store
	updated := repositoryTestBinding("sonarr", "entry-1", "file-1", 2)
	updated.EntryFileName = "updated.mkv"
	if err := repository.Save(updated); err != nil {
		t.Fatal(err)
	}

	want := bindingDeltaStoreKey("entry-1", "file-1")
	if len(store.puts) != 1 || store.puts[0] != want {
		t.Fatalf("Save wrote %v, want the single delta row %q", store.puts, want)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository = openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	bindings, err := repository.LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(bindings) != 2 {
		t.Fatalf("binding count = %d, want 2", len(bindings))
	}
	if bindings[0].EntryFileName != "updated.mkv" || bindings[0].Generation != 2 {
		t.Fatalf("delta did not win over the snapshot: %#v", bindings[0])
	}
}

func TestBindingRepositoryGenerationReplacesDeltas(t *testing.T) {
	path := t.TempDir() + "/bindings.db"
	repository := openTestBindingRepository(t, path)
	if err := repository.ReplaceArrGeneration("radarr", 1, []Binding{
		repositoryTestBinding("radarr", "entry-1", "file-1", 1),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Save(repositoryTestBinding("radarr", "entry-2", "file-2", 2)); err != nil {
		t.Fatal(err)
	}
	if err := repository.Delete("entry-1", "file-1"); err != nil {
		t.Fatal(err)
	}
	// A later full scan is authoritative: it must drop both the delta and the
	// tombstone the targeted writes left behind.
	if err := repository.ReplaceArrGeneration("radarr", 3, []Binding{
		repositoryTestBinding("radarr", "entry-1", "file-1", 3),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	repository = openTestBindingRepository(t, path)
	t.Cleanup(func() { _ = repository.Close() })
	assertSingleRepositoryBinding(t, repository, repositoryTestBinding("radarr", "entry-1", "file-1", 3))
}
