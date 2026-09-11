package repair

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func TestCollectArrFilesUsesUniqueManagedSizeFallback(t *testing.T) {
	media := arr.Content{Files: []arr.ContentFile{{
		Path: "/sonarr/library/renamed.mkv",
		Size: 12345,
	}}}
	managed := map[int64][]managedArrFile{
		12345: {{entryName: "Release.Name", fileName: "original.mkv"}},
	}

	grouped, stats := collectArrFiles(media, managed)
	files := grouped["Release.Name"]
	if len(files) != 1 || files[0].TargetPath != "original.mkv" || files[0].IsSymlink {
		t.Fatalf("fallback files = %#v", files)
	}
	if stats.total != 1 || stats.fallback != 1 || stats.unmapped != 0 || stats.ambiguous != 0 {
		t.Fatalf("fallback stats = %#v", stats)
	}
}

func TestCollectArrFilesRejectsAmbiguousSizeFallback(t *testing.T) {
	media := arr.Content{Files: []arr.ContentFile{{
		Path: "/sonarr/library/renamed.mkv",
		Size: 12345,
	}}}
	managed := map[int64][]managedArrFile{
		12345: {
			{entryName: "Release.One", fileName: "first.mkv"},
			{entryName: "Release.Two", fileName: "second.mkv"},
		},
	}

	grouped, stats := collectArrFiles(media, managed)
	if len(grouped) != 0 {
		t.Fatalf("ambiguous fallback mapped files = %#v", grouped)
	}
	if stats.total != 1 || stats.ambiguous != 1 || stats.fallback != 0 {
		t.Fatalf("ambiguous stats = %#v", stats)
	}
}

func TestCollectArrFilesPrefersReadableSymlink(t *testing.T) {
	root := t.TempDir()
	entryDir := filepath.Join(root, "managed", "Release.Name")
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatalf("create managed entry: %v", err)
	}
	target := filepath.Join(entryDir, "original.mkv")
	if err := os.WriteFile(target, []byte("video"), 0o644); err != nil {
		t.Fatalf("create managed file: %v", err)
	}
	libraryDir := filepath.Join(root, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatalf("create library: %v", err)
	}
	libraryPath := filepath.Join(libraryDir, "renamed.mkv")
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatalf("create library symlink: %v", err)
	}

	media := arr.Content{Files: []arr.ContentFile{{Path: libraryPath, Size: 5}}}
	managed := map[int64][]managedArrFile{
		5: {{entryName: "Wrong.Release", fileName: "wrong.mkv"}},
	}
	grouped, stats := collectArrFiles(media, managed)
	files := grouped[entryDir]
	if len(files) != 1 || files[0].TargetPath != "original.mkv" || !files[0].IsSymlink {
		t.Fatalf("symlink files = %#v", files)
	}
	if stats.resolved != 1 || stats.fallback != 0 {
		t.Fatalf("symlink stats = %#v", stats)
	}
}
