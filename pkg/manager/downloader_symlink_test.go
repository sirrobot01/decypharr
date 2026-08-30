package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestCreateSymlinksSkipsMatchingDirectoryName(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	mountPath := t.TempDir()
	symlinkDir := t.TempDir()
	fileName := "release.mkv"

	nestedDir := filepath.Join(mountPath, fileName)
	if err := os.Mkdir(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	wantTarget := filepath.Join(nestedDir, fileName)
	if err := os.WriteFile(wantTarget, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &Downloader{logger: zerolog.Nop()}
	entry := &storage.Entry{Name: "release"}
	files := []*storage.File{{Name: fileName}}

	paths, err := d.createSymlinksWhenMountFilesAppear(entry, files, mountPath, symlinkDir)
	if err != nil {
		t.Fatalf("create symlinks: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("created %d symlinks, want 1", len(paths))
	}

	gotTarget, err := os.Readlink(paths[0])
	if err != nil {
		t.Fatalf("read symlink: %v", err)
	}
	if gotTarget != wantTarget {
		t.Fatalf("symlink target = %q, want nested file %q", gotTarget, wantTarget)
	}
	info, err := os.Stat(paths[0])
	if err != nil {
		t.Fatalf("stat symlink target: %v", err)
	}
	if info.IsDir() {
		t.Fatal("symlink target is a directory, want file")
	}
}
