//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"testing"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rs/zerolog"
	internalconfig "github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	mountconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestChildStableAttrIsDeterministic(t *testing.T) {
	dir := &Dir{virtualPath: "/__all__/example"}

	first := dir.childStableAttr("video.mkv", fuse.S_IFREG|0644)
	second := dir.childStableAttr("video.mkv", fuse.S_IFREG|0644)

	if first != second {
		t.Fatalf("same child returned different stable attributes: first=%+v second=%+v", first, second)
	}
	if first.Ino <= 1 {
		t.Fatalf("child inode must not use a reserved value: %d", first.Ino)
	}
}

func TestChildStableAttrIncludesFullParentPath(t *testing.T) {
	firstParent := &Dir{virtualPath: "/__all__/example"}
	secondParent := &Dir{virtualPath: "/provider/example"}

	first := firstParent.childStableAttr("video.mkv", fuse.S_IFREG|0644)
	second := secondParent.childStableAttr("video.mkv", fuse.S_IFREG|0644)

	if first.Ino == second.Ino {
		t.Fatalf("same child name under different parents returned inode %d", first.Ino)
	}
}

func TestNewDirTracksCanonicalVirtualPath(t *testing.T) {
	dir := NewDir(nil, "", LevelRoot, 0, nil, zerolog.Nop(), nil)

	if got := dir.childPath("__all__"); got != "/__all__" {
		t.Fatalf("root child path = %q, want %q", got, "/__all__")
	}

	group := newDir(nil, "__all__", dir.childPath("__all__"), LevelTorrent, 0, nil, zerolog.Nop(), nil)
	child := newDir(nil, "example", group.childPath("example"), LevelFile, 0, nil, zerolog.Nop(), nil)
	if got := child.childPath("video.mkv"); got != "/__all__/example/video.mkv" {
		t.Fatalf("nested child path = %q, want %q", got, "/__all__/example/video.mkv")
	}
}

func TestRefreshExistingChildUpdatesRetainedInode(t *testing.T) {
	initial := testRemoteFileInfo(t, 128, time.Now().Add(-time.Hour))
	replacement := testRemoteFileInfo(t, 256, time.Now())
	root := NewDir(nil, "", LevelRoot, 0, &mountconfig.FuseConfig{}, zerolog.Nop(), logger.NewRateLimitedLogger())
	file := NewFile(nil, &mountconfig.FuseConfig{}, initial, logger.NewRateLimitedLogger())

	fs.NewNodeFS(root, &fs.Options{})
	inode := root.NewInode(context.Background(), file, root.childStableAttr("video.mkv", fuse.S_IFREG|0644))
	if !root.AddChild("video.mkv", inode, false) {
		t.Fatal("failed to add test child")
	}

	retained := root.refreshExistingChild("video.mkv", replacement)
	if retained != inode {
		t.Fatal("lookup did not return the retained inode")
	}
	if got := file.info.Load(); got != replacement {
		t.Fatal("retained file metadata was not refreshed")
	}

	var current fuse.AttrOut
	if errno := file.Getattr(context.Background(), nil, &current); errno != 0 {
		t.Fatalf("Getattr failed: %v", errno)
	}
	if current.Size != uint64(replacement.Size()) {
		t.Fatalf("current inode size = %d, want replacement size %d", current.Size, replacement.Size())
	}

	var openSnapshot fuse.AttrOut
	handle := &Handle{file: file, info: initial, content: initial.Content()}
	if errno := file.Getattr(context.Background(), handle, &openSnapshot); errno != 0 {
		t.Fatalf("handle Getattr failed: %v", errno)
	}
	if openSnapshot.Size != uint64(initial.Size()) {
		t.Fatalf("open handle size = %d, want original size %d", openSnapshot.Size, initial.Size())
	}
}

func TestRefreshExistingChildUpdatesRetainedDirModTime(t *testing.T) {
	root := NewDir(nil, "", LevelRoot, 0, &mountconfig.FuseConfig{}, zerolog.Nop(), logger.NewRateLimitedLogger())
	child := newDir(nil, "shows", "/shows", LevelTorrent, 100, &mountconfig.FuseConfig{}, zerolog.Nop(), logger.NewRateLimitedLogger())

	fs.NewNodeFS(root, &fs.Options{})
	inode := root.NewInode(context.Background(), child, root.childStableAttr("shows", fuse.S_IFDIR|0755))
	if !root.AddChild("shows", inode, false) {
		t.Fatal("failed to add test child")
	}

	var managerInstance manager.Manager
	info := managerInstance.RootInfo()

	retained := root.refreshExistingChild("shows", info)
	if retained != inode {
		t.Fatal("expected the retained directory inode")
	}
	if got, want := child.modTime.Load(), uint64(info.ModTime().Unix()); got != want {
		t.Fatalf("retained dir modTime = %d, want %d", got, want)
	}
}

func TestRefreshExistingChildRejectsKindMismatch(t *testing.T) {
	initial := testRemoteFileInfo(t, 128, time.Now())
	root := NewDir(nil, "", LevelRoot, 0, &mountconfig.FuseConfig{}, zerolog.Nop(), logger.NewRateLimitedLogger())
	file := NewFile(nil, &mountconfig.FuseConfig{}, initial, logger.NewRateLimitedLogger())

	fs.NewNodeFS(root, &fs.Options{})
	inode := root.NewInode(context.Background(), file, root.childStableAttr("video.mkv", fuse.S_IFREG|0644))
	if !root.AddChild("video.mkv", inode, false) {
		t.Fatal("failed to add test child")
	}

	var managerInstance manager.Manager
	if root.refreshExistingChild("video.mkv", managerInstance.RootInfo()) != nil {
		t.Fatal("directory info must not refresh a retained file node")
	}
}

func TestSetEntryOutUsesFileTimestampFallback(t *testing.T) {
	info := &manager.FileInfo{}
	mountConfig := &mountconfig.FuseConfig{}
	dir := &Dir{config: mountConfig}
	file := NewFile(nil, mountConfig, info, logger.NewRateLimitedLogger())

	var entry fuse.EntryOut
	dir.setEntryOut(info, &entry, dir.nodeModTime(info, file))

	var attr fuse.AttrOut
	if errno := file.Getattr(context.Background(), nil, &attr); errno != 0 {
		t.Fatalf("Getattr failed: %v", errno)
	}
	if entry.Mtime != attr.Mtime || entry.Ctime != attr.Ctime || entry.Atime != attr.Atime {
		t.Fatalf("entry timestamps (%d, %d, %d) differ from Getattr (%d, %d, %d)", entry.Atime, entry.Mtime, entry.Ctime, attr.Atime, attr.Mtime, attr.Ctime)
	}
	if entry.Mtime > uint64(time.Now().Add(time.Minute).Unix()) {
		t.Fatalf("zero ModTime underflowed to %d", entry.Mtime)
	}
}

func testRemoteFileInfo(t *testing.T, size int64, addedOn time.Time) *manager.FileInfo {
	t.Helper()
	internalconfig.SetConfigPath(t.TempDir())
	internalconfig.Reset()
	managerInstance := manager.New()
	t.Cleanup(func() {
		if err := managerInstance.Stop(); err != nil {
			t.Errorf("stop manager: %v", err)
		}
		internalconfig.Reset()
	})

	entry := &storage.Entry{
		Protocol: internalconfig.ProtocolTorrent,
		InfoHash: "test-hash",
		Name:     "test-entry",
		Size:     size,
		Files: map[string]*storage.File{
			"video.mkv": {
				Name:     "video.mkv",
				Size:     size,
				InfoHash: "test-hash",
				AddedOn:  addedOn,
			},
		},
	}
	if err := managerInstance.Storage().AddOrUpdate(entry); err != nil {
		t.Fatalf("add test entry: %v", err)
	}
	info, err := managerInstance.GetTorrentFile(entry.Name, "video.mkv")
	if err != nil {
		t.Fatalf("get test file metadata: %v", err)
	}
	return info
}
