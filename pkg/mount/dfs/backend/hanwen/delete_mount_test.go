//go:build linux

package hanwen

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	gofs "github.com/hanwen/go-fuse/v2/fs"
	"github.com/rs/zerolog"
	internalconfig "github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	mountconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Deletes through a real mount must stick: a deleted file must not stay
// listed, and a deleted folder must not come back with another entry's files.
func TestDeletesStickOnRealMount(t *testing.T) {
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("FUSE unavailable: %v", err)
	}
	internalconfig.SetConfigPath(t.TempDir())
	internalconfig.Reset()
	t.Cleanup(internalconfig.Reset)
	mgr := manager.New()
	t.Cleanup(func() { _ = mgr.Stop() })

	add := func(hash, folder string, names ...string) {
		entry := &storage.Entry{InfoHash: hash, Name: folder, Protocol: internalconfig.ProtocolNZB, Files: map[string]*storage.File{}}
		for _, name := range names {
			entry.Files[name] = &storage.File{Name: name, InfoHash: hash, Size: 1}
		}
		if err := mgr.Storage().AddOrUpdate(entry); err != nil {
			t.Fatal(err)
		}
	}
	add("movie", "Example.Movie", "movie.mkv", "sample.mkv")
	add("files-old", "Example.Show.S01", "e01.mkv", "e02.mkv")
	add("files-new", "Example.Show.S01", "e01.mkv", "e02.mkv")
	add("dir-old", "Example.Show.S02", "e01.mkv")
	add("dir-new", "Example.Show.S02", "e01.mkv")
	add("empty-old", "Example.Show.S03", "e01.mkv")
	add("empty-new", "Example.Show.S03", "e01.mkv")
	// Every file deleted but the entries left behind, as older builds could.
	item, err := mgr.Storage().GetEntryItem("Example.Show.S03")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range item.Files {
		f.Deleted = true
	}
	if err := mgr.Storage().UpdateItem(item); err != nil {
		t.Fatal(err)
	}

	fuseConfig := &mountconfig.FuseConfig{CacheDir: t.TempDir(), CacheCleanupInterval: time.Hour}
	vfsManager, err := vfs.NewManager(context.Background(), mgr, fuseConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vfsManager.Close() })
	root := NewDir(vfsManager, "", LevelRoot, 0, fuseConfig, zerolog.Nop(), logger.NewRateLimitedLogger())
	mountPoint := t.TempDir()
	server, err := gofs.Mount(mountPoint, root, &gofs.Options{})
	if err != nil {
		t.Skipf("cannot mount FUSE here: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })
	all := filepath.Join(mountPoint, manager.EntryAllFolder)

	t.Run("partial delete", func(t *testing.T) {
		dir := filepath.Join(all, "Example.Movie")
		if _, err := os.ReadDir(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "sample.mkv")); err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.Name() == "sample.mkv" {
				t.Fatal("deleted file is still listed")
			}
		}
		// A stat right after a listing is answered from that listing.
		if _, err := os.Lstat(filepath.Join(dir, "sample.mkv")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("lstat after listing = %v, want not exist", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "movie.mkv")); err != nil {
			t.Fatalf("remaining file: %v", err)
		}
	})

	gone := func(t *testing.T, folder string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			_, statErr := os.Lstat(filepath.Join(all, folder))
			entries, err := os.ReadDir(all)
			if err != nil {
				t.Fatal(err)
			}
			listed := false
			for _, entry := range entries {
				listed = listed || entry.Name() == folder
			}
			if errors.Is(statErr, fs.ErrNotExist) && !listed {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s came back: listed=%v, lstat=%v", folder, listed, statErr)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	t.Run("delete every file of a shared folder", func(t *testing.T) {
		dir := filepath.Join(all, "Example.Show.S01")
		for _, name := range []string{"e01.mkv", "e02.mkv"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		gone(t, "Example.Show.S01")
	})

	t.Run("rmdir refuses a folder with files", func(t *testing.T) {
		dir := filepath.Join(all, "Example.Show.S02")
		if err := syscall.Rmdir(dir); !errors.Is(err, syscall.ENOTEMPTY) {
			t.Fatalf("rmdir = %v, want ENOTEMPTY", err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "e01.mkv")); err != nil {
			t.Fatalf("file after refused rmdir: %v", err)
		}
	})

	t.Run("rmdir clears a folder whose files are deleted", func(t *testing.T) {
		dir := filepath.Join(all, "Example.Show.S03")
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("listing = %d entries, %v; want an empty folder", len(entries), err)
		}
		if err := syscall.Rmdir(dir); err != nil {
			t.Fatal(err)
		}
		gone(t, "Example.Show.S03")
	})
}
