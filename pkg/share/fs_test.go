package share

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func testManager(t *testing.T) *manager.Manager {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	config.Reset()
	cfg := config.Get()
	cfg.FolderNaming = config.WebDavUseFileName
	mgr := manager.New()
	t.Cleanup(func() {
		if err := mgr.Stop(); err != nil {
			t.Error(err)
		}
		config.Reset()
	})
	return mgr
}

func addEntry(t *testing.T, mgr *manager.Manager, name string, files map[string]int64) {
	t.Helper()
	now := time.Now()
	entry := &storage.Entry{
		Protocol:         config.ProtocolTorrent,
		InfoHash:         "hash-" + name,
		Name:             name,
		OriginalFilename: name,
		Files:            map[string]*storage.File{},
		AddedOn:          now,
	}
	for file, size := range files {
		entry.Size += size
		entry.Files[file] = &storage.File{
			Name: file, Size: size, InfoHash: entry.InfoHash, AddedOn: now,
		}
	}
	if err := mgr.Storage().AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	mgr.RefreshEntries(false)
}

func readdir(t *testing.T, fsys *filesystem, name string) []fs.FileInfo {
	t.Helper()
	f, err := fsys.OpenFile(context.Background(), name, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	entries, err := f.Readdir(-1)
	if err != nil {
		t.Fatalf("readdir %s: %v", name, err)
	}
	return entries
}

func TestFilesystemBuildsNestedFileTree(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{
		"Season 01/Episode 01.mkv": 100,
		"Season 01/Episode 02.mkv": 200,
	})

	fsys := newFilesystem(mgr, nil)
	root := readdir(t, fsys, "/__all__/Example Show")
	if len(root) != 1 || root[0].Name() != "Season 01" || !root[0].IsDir() {
		t.Fatalf("unexpected torrent root: %#v", root)
	}

	season := readdir(t, fsys, "/__all__/Example Show/Season 01")
	names := []string{season[0].Name(), season[1].Name()}
	slices.Sort(names)
	if !slices.Equal(names, []string{"Episode 01.mkv", "Episode 02.mkv"}) {
		t.Fatalf("unexpected season contents: %v", names)
	}

	info, err := fsys.Stat(context.Background(), "/__all__/Example Show/Season 01/Episode 02.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Size() != 200 {
		t.Fatalf("unexpected file info: dir=%v size=%d", info.IsDir(), info.Size())
	}
}

func TestFilesystemIsReadOnly(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{"a.mkv": 1})
	fsys := newFilesystem(mgr, nil)
	ctx := context.Background()

	for _, flag := range []int{os.O_WRONLY, os.O_RDWR, os.O_RDONLY | os.O_CREATE, os.O_RDONLY | os.O_TRUNC} {
		if _, err := fsys.OpenFile(ctx, "/__all__/Example Show/a.mkv", flag, 0); !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("open with flag %#x: err = %v, want ErrPermission", flag, err)
		}
	}
	if err := fsys.Mkdir(ctx, "/new", 0o755); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("mkdir: %v", err)
	}
	if err := fsys.Rename(ctx, "/__all__", "/other"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("rename: %v", err)
	}
	if err := fsys.RemoveAll(ctx, "/__all__"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("removeall: %v", err)
	}
}

func TestFilesystemErrorSentinels(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{"a.mkv": 1})
	fsys := newFilesystem(mgr, nil)
	ctx := context.Background()

	if _, err := fsys.Stat(ctx, "/no/such/thing/here"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat missing: err = %v, want ErrNotExist", err)
	}
	if _, err := fsys.Stat(ctx, "../escape"); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("stat escape: err = %v, want ErrPermission", err)
	}
}

func TestDirFileReaddirPaging(t *testing.T) {
	mgr := testManager(t)
	addEntry(t, mgr, "Example Show", map[string]int64{
		"e1.mkv": 1, "e2.mkv": 2, "e3.mkv": 3,
	})
	fsys := newFilesystem(mgr, nil)

	f, err := fsys.OpenFile(context.Background(), "/__all__/Example Show", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	first, err := f.Readdir(2)
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %d entries, err %v", len(first), err)
	}
	second, err := f.Readdir(2)
	if err != nil || len(second) != 1 {
		t.Fatalf("second page: %d entries, err %v", len(second), err)
	}
	if _, err := f.Readdir(2); err != io.EOF {
		t.Fatalf("exhausted dir: err = %v, want io.EOF", err)
	}
}
