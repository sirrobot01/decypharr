//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"path"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
)

type DirLevel int

const (
	LevelRoot DirLevel = iota // This is __all__, version.txt, torrents, __bad__ and custom dirs
	LevelTorrent
	LevelFile
)

// Dir implements a FUSE directory following
type Dir struct {
	fs.Inode
	vfs   *vfs.Manager
	level DirLevel
	name  string
	// virtualPath is the canonical path of this directory within the mount.
	// It is used to give children stable, namespace-unique inode numbers.
	virtualPath string
	config      *config.FuseConfig
	logger      zerolog.Logger
	rlLogger    *logger.RateLimitedLogger
	// modTime is atomic because Lookup refreshes it on a retained inode while
	// Getattr may be reading it concurrently.
	modTime atomic.Uint64
}

var _ = (fs.NodeLookuper)((*Dir)(nil))
var _ = (fs.NodeReaddirer)((*Dir)(nil))
var _ = (fs.NodeGetattrer)((*Dir)(nil))
var _ = (fs.NodeUnlinker)((*Dir)(nil))
var _ = (fs.NodeRmdirer)((*Dir)(nil))

// NewDir creates a new directory
func NewDir(vfsManager *vfs.Manager, name string, level DirLevel, modTime uint64, config *config.FuseConfig, log zerolog.Logger, rl *logger.RateLimitedLogger) *Dir {
	return newDir(vfsManager, name, path.Join("/", name), level, modTime, config, log, rl)
}

func newDir(vfsManager *vfs.Manager, name, virtualPath string, level DirLevel, modTime uint64, config *config.FuseConfig, log zerolog.Logger, rl *logger.RateLimitedLogger) *Dir {
	d := &Dir{
		vfs:         vfsManager,
		name:        name,
		virtualPath: virtualPath,
		level:       level,
		config:      config,
		logger:      log.With().Str("dir", name).Logger(),
		rlLogger:    rl,
	}
	d.modTime.Store(modTime)
	return d
}

func (d *Dir) childPath(name string) string {
	return path.Join(d.virtualPath, name)
}

func (d *Dir) childStableAttr(name string, mode uint32) fs.StableAttr {
	return fs.StableAttr{
		Mode: mode,
		Ino:  hashPath(d.childPath(name)),
	}
}

// newNode creates a new fuse node from a FileInfo, caching it on the FileInfo
func (d *Dir) newNode(info *manager.FileInfo) fs.InodeEmbedder {
	// Check if we have a cached node
	if cached := info.Sys(); cached != nil {
		return cached.(fs.InodeEmbedder)
	}

	var node fs.InodeEmbedder
	if info.IsDir() {
		modTime := info.ModTime()
		if modTime.IsZero() {
			modTime = time.Now()
		}
		node = newDir(d.vfs, info.Name(), d.childPath(info.Name()), d.level+1, uint64(modTime.Unix()), d.config, d.logger, d.rlLogger)
	} else {
		node = NewFile(d.vfs, d.config, info, d.rlLogger)
	}

	// Cache the node for later
	info.SetSys(node)
	return node
}

// Getattr returns directory attributes
func (d *Dir) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = 0755 | fuse.S_IFDIR
	out.Size = 4096 // Standard directory size
	out.Nlink = 2   // Directories have 2 links (itself + "." entry)
	out.Uid = d.config.UID
	out.Gid = d.config.GID
	modTime := d.modTime.Load()
	out.Atime = modTime
	out.Mtime = modTime
	out.Ctime = modTime
	out.AttrValid = uint64(AttrTimeout.Seconds())
	return 0
}

func (d *Dir) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	// Always query fresh data from manager (no caching)
	info, errno := d.lookupChild(name)
	if errno != 0 {
		return nil, errno
	}

	// Stable attributes make go-fuse retain an existing inode. Refresh the
	// retained child before returning it so replacements do not continue
	// serving stale metadata.
	if child := d.refreshExistingChild(name, info); child != nil {
		d.setEntryOut(info, out, d.nodeModTime(info, child.Operations()))
		return child, 0
	}

	// get or create fuse node (cached on FileInfo)
	node := d.newNode(info)

	// Set attributes
	d.setEntryOut(info, out, d.nodeModTime(info, node))

	// Supplying a stable inode lets go-fuse deduplicate repeated lookups.
	// Leaving Ino unset makes it allocate a new sequential inode each time.
	attr := d.childStableAttr(name, out.Mode)
	if !info.IsDir() {
		// Gen is part of go-fuse's node identity. A stale inode can outlive an
		// unlink through an open handle; without a generation, re-creating the
		// same path would silently resolve to it in the bridge (invisible to
		// this method). A replacement carries a new ModTime and so misses it.
		// Directories keep Gen 0: root listings stamp their ModTime with the
		// current time, which would churn a new inode per lookup.
		if mt := info.ModTime(); !mt.IsZero() {
			attr.Gen = uint64(mt.Unix())
		}
	}
	return d.NewInode(ctx, node, attr), 0
}

// refreshExistingChild returns the already-known child inode for name after
// refreshing its metadata from a fresh manager lookup, or nil if there is no
// retained child of the matching kind.
func (d *Dir) refreshExistingChild(name string, info *manager.FileInfo) *fs.Inode {
	child := d.GetChild(name)
	if child == nil {
		return nil
	}

	switch ops := child.Operations().(type) {
	case *File:
		if info.IsDir() {
			return nil
		}
		ops.updateInfo(info)
		info.SetSys(ops)
	case *Dir:
		if !info.IsDir() {
			return nil
		}
		if mt := info.ModTime(); !mt.IsZero() {
			ops.modTime.Store(uint64(mt.Unix()))
		}
	default:
		return nil
	}
	return child
}

func (d *Dir) nodeModTime(info *manager.FileInfo, node fs.InodeEmbedder) uint64 {
	if modTime := info.ModTime(); !modTime.IsZero() {
		return uint64(modTime.Unix())
	}

	switch node := node.(type) {
	case *File:
		return uint64(node.createdAt.Unix())
	case *Dir:
		return node.modTime.Load()
	default:
		return 0
	}
}

// lookupChild looks up a child by name using O(1) lookups where possible
func (d *Dir) lookupChild(name string) (*manager.FileInfo, syscall.Errno) {
	switch d.level {
	case LevelRoot:
		// Root level: small static list (~6 entries), O(n) is acceptable
		// These are __all__, __bad__, torrents, nzbs, custom folders, version.txt
		entries := d.vfs.GetManager().GetEntries()
		for i := range entries {
			if entries[i].Name() == name {
				return &entries[i], 0
			}
		}
		return nil, syscall.ENOENT

	case LevelTorrent:
		// Torrent level: O(1) lookup by name
		info, err := d.vfs.GetManager().GetEntryInfo(name)
		if err != nil {
			return nil, syscall.ENOENT
		}
		return info, 0

	case LevelFile:
		// File level: O(1) lookup specific file in torrent
		info, err := d.vfs.GetManager().GetTorrentFile(d.name, name)
		if err != nil {
			return nil, syscall.ENOENT
		}
		return info, 0

	default:
		return nil, syscall.ENOENT
	}
}

// setEntryOut sets the attributes for an entry
func (d *Dir) setEntryOut(info *manager.FileInfo, out *fuse.EntryOut, modTime uint64) {
	if info.IsDir() {
		out.Attr.Mode = fuse.S_IFDIR | 0755
		out.Attr.Nlink = 2
	} else {
		out.Attr.Mode = fuse.S_IFREG | 0644
		out.Attr.Size = uint64(info.Size())
		out.Attr.Nlink = 1
	}

	out.Attr.Uid = d.config.UID
	out.Attr.Gid = d.config.GID
	out.Attr.Atime = modTime
	out.Attr.Mtime = modTime
	out.Attr.Ctime = modTime
	out.AttrValid = uint64(AttrTimeout.Seconds())
	out.EntryValid = uint64(EntryTimeout.Seconds())
}

func (d *Dir) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	// Always query fresh data from manager (no caching)
	entries, errno := d.listChildren()
	if errno != 0 {
		return nil, errno
	}

	fuseEntries := make([]fuse.DirEntry, 0, len(entries))
	for _, info := range entries {
		mode := uint32(fuse.S_IFREG | 0644)
		if info.IsDir() {
			mode = fuse.S_IFDIR | 0755
		}
		fuseEntries = append(fuseEntries, fuse.DirEntry{
			Mode: mode,
			Name: info.Name(),
			Ino:  d.childStableAttr(info.Name(), mode).Ino,
		})
	}

	return fs.NewListDirStream(fuseEntries), 0
}

// listChildren returns the children of this directory
func (d *Dir) listChildren() ([]manager.FileInfo, syscall.Errno) {
	switch d.level {
	case LevelRoot:
		return d.vfs.GetManager().GetEntries(), 0

	case LevelTorrent:
		_, children := d.vfs.GetManager().GetEntryChildren(d.name)
		if children == nil {
			return nil, 0
		}
		return children, 0

	case LevelFile:
		_, children := d.vfs.GetManager().GetTorrentChildren(d.name)
		if children == nil {
			return nil, 0
		}
		return children, 0

	default:
		return nil, syscall.ENOENT
	}
}

// Refresh clears any cached nodes so next lookup gets fresh data
func (d *Dir) Refresh() {
	// For kernel cache invalidation, we can still notify if we're mounted
	if d.EmbeddedInode().StableAttr().Ino != 0 {
		// Invalidate directory content cache to force Readdir
		_ = d.NotifyContent(0, 0)
	}
}

// RefreshChild invalidates a specific child directory's cache
func (d *Dir) RefreshChild(name string) {
	if d.EmbeddedInode().StableAttr().Ino == 0 {
		return
	}

	// Notify kernel that this entry may have changed (force re-lookup)
	_ = d.NotifyEntry(name)

	// get the child inode from the kernel's cache
	// If it exists, also invalidate its content cache so Readdir is re-called
	if child := d.GetChild(name); child != nil {
		// get the Dir node from the child inode
		if childDir, ok := child.Operations().(*Dir); ok {
			// Invalidate the child directory's content cache
			_ = childDir.NotifyContent(0, 0)
		}
		// Where the kernel supports NOTIFY_PRUNE, also drop the subtree's
		// unused dentries and inodes so the next lookup rebuilds fresh nodes
		// instead of retaining stale ones. Busy nodes survive; ENOSYS on older
		// kernels leaves the invalidation above as the only effect.
		_ = d.NotifyPrune([]*fs.Inode{child})
	}
}

// Unlink removes a child from this directory
func (d *Dir) Unlink(ctx context.Context, name string) syscall.Errno {
	if d.level != LevelFile {
		return syscall.EPERM
	}

	info, err := d.vfs.GetManager().GetTorrentFile(d.name, name)
	if err != nil {
		return syscall.ENOENT
	}

	if err := d.vfs.GetManager().RemoveEntry(info); err != nil {
		d.logger.Error().Err(err).Str("file", info.Name()).Msg("Failed to remove file from source")
		return syscall.EIO
	}

	return 0
}

// Rmdir removes a directory from this directory
func (d *Dir) Rmdir(ctx context.Context, name string) syscall.Errno {
	if d.level != LevelTorrent {
		return syscall.EPERM
	}

	info, err := d.vfs.GetManager().GetTorrentEntry(name)
	if err != nil {
		return syscall.ENOENT
	}

	if err := d.vfs.GetManager().RemoveEntry(info); err != nil {
		d.logger.Error().Err(err).Str("torrent", name).Msg("Failed to remove torrent from source")
		return syscall.EIO
	}

	return 0
}
