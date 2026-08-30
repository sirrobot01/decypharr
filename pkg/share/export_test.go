package share

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestExportServesThroughCache(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{"Season 01/Episode 01.mkv": 100})

	enabled := true
	dir := filepath.Join(t.TempDir(), "share-cache")
	export, err := NewExport(context.Background(), mgr, config.ShareCache{Enabled: &enabled, Dir: dir, MaxSize: "1GB"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := export.Close(); err != nil {
			t.Error(err)
		}
	})

	// The cache owns its directory and lays out data and metadata separately.
	for _, sub := range []string{"data", "meta"} {
		if _, err := os.Stat(filepath.Join(dir, sub)); err != nil {
			t.Fatalf("cache did not create %s: %v", sub, err)
		}
	}

	// Metadata operations pass through to the catalog unchanged.
	info, err := export.FileSystem().Stat(context.Background(), "/__all__/Example Show/Season 01/Episode 01.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 100 {
		t.Fatalf("size = %d, want 100", info.Size())
	}

	if got := export.Stats().MaxBytes; got != 1<<30 {
		t.Fatalf("budget = %d, want %d", got, int64(1)<<30)
	}
}

// The cache is opt-in, so an unset config must not touch the disk.
func TestExportWithoutCache(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{"a.mkv": 1})

	dir := filepath.Join(t.TempDir(), "share-cache")
	export, err := NewExport(context.Background(), mgr, config.ShareCache{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("disabled cache touched %s: %v", dir, err)
	}
	if _, err := export.FileSystem().Stat(context.Background(), "/__all__/Example Show/a.mkv"); err != nil {
		t.Fatal(err)
	}
	if got := export.Stats().MaxBytes; got != 0 {
		t.Fatalf("disabled cache reported a budget of %d", got)
	}
}
