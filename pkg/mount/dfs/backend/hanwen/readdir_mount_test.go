//go:build linux

package hanwen

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/rs/zerolog"
	internalconfig "github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	mountconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// A real mount is the only way to see this bug: the kernel, not this code,
// rejects a bad directory entry and fails the whole listing with EIO.
func TestListingSurvivesUnlistableNamesOnRealMount(t *testing.T) {
	longButListable := strings.Repeat("M", 300)
	if _, err := os.Stat("/dev/fuse"); err != nil {
		t.Skipf("FUSE unavailable: %v", err)
	}
	internalconfig.SetConfigPath(t.TempDir())
	internalconfig.Reset()
	t.Cleanup(internalconfig.Reset)
	mgr := manager.New()
	t.Cleanup(func() { _ = mgr.Stop() })

	if err := mgr.Storage().SaveMigrationStatus(&storage.SystemMigrationStatus{}); err != nil {
		t.Fatal(err)
	}
	for hash, name := range map[string]string{
		"aaa": "Alpha",
		"bbb": strings.Repeat("L", 5000),
		"ccc": "With/Slash",
		"ddd": "Delta",
		"eee": longButListable,
	} {
		entry := &storage.Entry{
			InfoHash: hash, Name: name, Protocol: internalconfig.ProtocolNZB,
			Files: map[string]*storage.File{"video.mkv": {Name: "video.mkv", InfoHash: hash, Size: 1}},
		}
		if err := mgr.Storage().AddOrUpdate(entry); err != nil {
			t.Fatal(err)
		}
	}

	fuseConfig := &mountconfig.FuseConfig{CacheDir: t.TempDir(), CacheCleanupInterval: time.Hour}
	vfsManager, err := vfs.NewManager(context.Background(), mgr, fuseConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vfsManager.Close() })
	root := NewDir(vfsManager, "", LevelRoot, 0, fuseConfig, zerolog.Nop(), logger.NewRateLimitedLogger())
	mountPoint := t.TempDir()
	server, err := fs.Mount(mountPoint, root, &fs.Options{})
	if err != nil {
		t.Skipf("cannot mount FUSE here: %v", err)
	}
	t.Cleanup(func() { _ = server.Unmount() })

	for _, group := range []string{manager.EntryAllFolder, manager.EntryNZBFolder} {
		entries, err := os.ReadDir(filepath.Join(mountPoint, group))
		if err != nil {
			t.Fatalf("listing %s: %v (got %d entries first)", group, err, len(entries))
		}
		var got []string
		for _, entry := range entries {
			got = append(got, entry.Name())
		}
		sort.Strings(got)
		if strings.Join(got, ",") != "Alpha,Delta,"+longButListable {
			t.Fatalf("listing %s = %.80q, want Alpha, Delta and the 300-byte name", group, got)
		}
	}
}
