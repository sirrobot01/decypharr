// Package share exposes the manager's library catalog as a facetfs.FileSystem
// so it can be served over the facetfs protocol packages (NFSv4 today; SFTP
// and SMB share the same adapter later). The export is read-only: every
// mutating call fails with fs.ErrPermission.
package share

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/facetfs"
)

type catalog interface {
	RootInfo() *manager.FileInfo
	GetEntries() []manager.FileInfo
	GetEntryChildren(string) (*manager.FileInfo, []manager.FileInfo)
	GetTorrentChildren(string) (*manager.FileInfo, []manager.FileInfo)
}

// lookupCacheTTL bounds staleness of memoized path resolutions. Matches the
// scale of kernel NFS attribute caching; imports/deletes surface within it.
const lookupCacheTTL = 2 * time.Second

type lookupCacheEntry struct {
	n      *node
	err    error
	expiry int64 // unix nanos
}

type node struct {
	info    *manager.FileInfo
	name    string
	size    int64
	modTime time.Time
	isDir   bool
}

type filesystem struct {
	catalog catalog
	open    func(*manager.FileInfo, fs.FileInfo) (facetfs.File, error)

	// lookups memoizes lookupAbsolute results for a short TTL: resolution
	// linear-scans catalog listings and one protocol request can resolve the
	// same path several times.
	lookups sync.Map // path key -> *lookupCacheEntry
}

func newFilesystem(c catalog, open func(*manager.FileInfo, fs.FileInfo) (facetfs.File, error)) *filesystem {
	return &filesystem{catalog: c, open: open}
}

func (f *filesystem) Mkdir(_ context.Context, name string, _ fs.FileMode) error {
	return &fs.PathError{Op: "mkdir", Path: name, Err: fs.ErrPermission}
}

func (f *filesystem) RemoveAll(_ context.Context, name string) error {
	return &fs.PathError{Op: "removeall", Path: name, Err: fs.ErrPermission}
}

func (f *filesystem) Rename(_ context.Context, oldName, _ string) error {
	return &fs.PathError{Op: "rename", Path: oldName, Err: fs.ErrPermission}
}

func (f *filesystem) Stat(_ context.Context, name string) (fs.FileInfo, error) {
	segments, err := absolute(name)
	if err != nil {
		return nil, err
	}
	entry, err := f.lookupAbsolute(segments)
	if err != nil {
		return nil, err
	}
	return fileInfo(entry), nil
}

func (f *filesystem) OpenFile(_ context.Context, name string, flag int, _ fs.FileMode) (facetfs.File, error) {
	if flag&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	segments, err := absolute(name)
	if err != nil {
		return nil, err
	}
	entry, err := f.lookupAbsolute(segments)
	if err != nil {
		return nil, err
	}
	info := fileInfo(entry)
	if entry.isDir {
		return &dirFile{info: info, list: func() ([]fs.FileInfo, error) { return f.readDir(segments) }}, nil
	}
	if !entry.info.IsRemote() {
		content := entry.info.Content()
		return newFile(info, bytes.NewReader(content), nil), nil
	}
	if f.open == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	return f.open(entry.info, info)
}

func (f *filesystem) readDir(segments []string) ([]fs.FileInfo, error) {
	var children []node
	switch len(segments) {
	case 0:
		children = nodes(f.catalog.GetEntries())
	case 1:
		_, entries := f.catalog.GetEntryChildren(segments[0])
		children = nodes(entries)
	default:
		children = f.torrentChildren(segments[1], strings.Join(segments[2:], "/"))
	}

	infos := make([]fs.FileInfo, 0, len(children))
	for i := range children {
		infos = append(infos, fileInfo(&children[i]))
	}
	slices.SortFunc(infos, func(a, b fs.FileInfo) int { return strings.Compare(a.Name(), b.Name()) })
	return infos, nil
}

func (f *filesystem) lookupAbsolute(segments []string) (*node, error) {
	key := strings.Join(segments, "\x00")
	now := time.Now().UnixNano()
	if v, ok := f.lookups.Load(key); ok {
		e := v.(*lookupCacheEntry)
		if now < e.expiry {
			return e.n, e.err
		}
		f.lookups.Delete(key)
	}
	n, err := f.lookupAbsoluteUncached(segments)
	f.lookups.Store(key, &lookupCacheEntry{n: n, err: err, expiry: now + int64(lookupCacheTTL)})
	return n, err
}

func (f *filesystem) lookupAbsoluteUncached(segments []string) (*node, error) {
	switch len(segments) {
	case 0:
		return makeNode(f.catalog.RootInfo()), nil
	case 1:
		entries := f.catalog.GetEntries()
		for i := range entries {
			if entries[i].Name() == segments[0] {
				return makeNode(&entries[i]), nil
			}
		}
	case 2:
		_, entries := f.catalog.GetEntryChildren(segments[0])
		for i := range entries {
			if entries[i].Name() == segments[1] {
				return makeNode(&entries[i]), nil
			}
		}
	default:
		if _, err := f.lookupAbsolute(segments[:2]); err != nil {
			return nil, err
		}
		fileName := strings.Join(segments[2:], "/")
		_, children := f.catalog.GetTorrentChildren(segments[1])
		for i := range children {
			if children[i].Name() == fileName {
				return makeNode(&children[i]), nil
			}
		}
		prefix := fileName + "/"
		for i := range children {
			if strings.HasPrefix(children[i].Name(), prefix) {
				return &node{
					name:    segments[len(segments)-1],
					size:    4096,
					modTime: children[i].ModTime(),
					isDir:   true,
				}, nil
			}
		}
	}
	return nil, &fs.PathError{Op: "stat", Path: "/" + path.Join(segments...), Err: fs.ErrNotExist}
}

func absolute(name string) ([]string, error) {
	clean := path.Clean(name)
	if clean == "." || clean == "/" || clean == "" {
		return nil, nil
	}
	clean = strings.TrimPrefix(clean, "/")
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return nil, &fs.PathError{Op: "resolve", Path: name, Err: fs.ErrPermission}
	}
	return strings.Split(clean, "/"), nil
}

func (f *filesystem) torrentChildren(torrent, prefix string) []node {
	_, entries := f.catalog.GetTorrentChildren(torrent)
	if prefix != "" {
		prefix += "/"
	}

	children := make(map[string]node)
	for i := range entries {
		remainder, ok := strings.CutPrefix(entries[i].Name(), prefix)
		if !ok || remainder == "" {
			continue
		}
		name, _, nested := strings.Cut(remainder, "/")
		if nested {
			if existing, ok := children[name]; !ok || !existing.isDir {
				children[name] = node{name: name, size: 4096, modTime: entries[i].ModTime(), isDir: true}
			}
			continue
		}
		if existing, ok := children[name]; ok && existing.isDir {
			continue
		}
		entry := makeNode(&entries[i])
		entry.name = name
		children[name] = *entry
	}

	result := make([]node, 0, len(children))
	for _, child := range children {
		result = append(result, child)
	}
	return result
}

func nodes(entries []manager.FileInfo) []node {
	result := make([]node, len(entries))
	for i := range entries {
		result[i] = *makeNode(&entries[i])
	}
	return result
}

func makeNode(info *manager.FileInfo) *node {
	return &node{
		info:    info,
		name:    info.Name(),
		size:    info.Size(),
		modTime: info.ModTime(),
		isDir:   info.IsDir(),
	}
}

func fileInfo(entry *node) fs.FileInfo {
	mode := fs.FileMode(0o444)
	size := entry.size
	if entry.isDir {
		mode = fs.ModeDir | 0o555
		size = 4096
	}
	return nodeInfo{
		name:    entry.name,
		modTime: entry.modTime,
		isDir:   entry.isDir,
		mode:    mode,
		size:    size,
	}
}

type nodeInfo struct {
	name    string
	modTime time.Time
	isDir   bool
	mode    fs.FileMode
	size    int64
}

func (i nodeInfo) Name() string       { return i.name }
func (i nodeInfo) Mode() fs.FileMode  { return i.mode }
func (i nodeInfo) Size() int64        { return i.size }
func (i nodeInfo) ModTime() time.Time { return i.modTime }
func (i nodeInfo) IsDir() bool        { return i.isDir }
func (i nodeInfo) Sys() any           { return nil }

var _ facetfs.FileSystem = (*filesystem)(nil)
