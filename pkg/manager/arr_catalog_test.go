package manager

import (
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Entries synced from a provider carry no category. They are still symlinked
// into an Arr library, so the catalog must return their files.
func TestListManagedFilesIgnoresEntryCategory(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	store, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	entries := []*storage.Entry{
		{
			InfoHash: "aabbccddeeff00112233445566778899aabbccdd",
			Name:     "Movie.2023",
			Category: "radarr",
			Files:    map[string]*storage.File{"movie.mkv": {Name: "movie.mkv", Size: 100}},
		},
		{
			InfoHash: "00112233445566778899aabbccddeeff00112233",
			Name:     "Synced.2023",
			Files:    map[string]*storage.File{"synced.mkv": {Name: "synced.mkv", Size: 200}},
		},
	}
	for _, entry := range entries {
		if err := store.AddOrUpdate(entry); err != nil {
			t.Fatal(err)
		}
	}

	catalog := managedArrCatalog{storage: store, logger: zerolog.Nop()}
	files, err := catalog.ListManagedFiles(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2: %#v", len(files), files)
	}

	one, err := catalog.ListManagedFiles(t.Context(), entries[1].InfoHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != 1 || one[0].FileName != "synced.mkv" || one[0].FileSize != 200 {
		t.Fatalf("targeted files = %#v", one)
	}
}

// A file with no ID cannot be indexed or reacquired, so the scan must count it
// rather than drop it silently.
func TestEntryManagedFilesCountsSkips(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	entry := &storage.Entry{
		InfoHash: "aabbccddeeff00112233445566778899aabbccdd",
		Name:     "Show.S01",
		Files: map[string]*storage.File{
			"kept.mkv":    {Name: "kept.mkv", Size: 100, ID: "id-1"},
			"gone.mkv":    {Name: "gone.mkv", Size: 200, ID: "id-2", Deleted: true},
			"no-id.mkv":   {Name: "no-id.mkv", Size: 300},
			"missing.mkv": nil,
		},
	}

	var skips catalogSkips
	files := entryManagedFiles(entry, &skips)
	if len(files) != 1 || files[0].FileName != "kept.mkv" {
		t.Fatalf("files = %#v", files)
	}
	if skips.deleted != 1 || skips.noID != 1 {
		t.Fatalf("skips = %#v", skips)
	}
}
