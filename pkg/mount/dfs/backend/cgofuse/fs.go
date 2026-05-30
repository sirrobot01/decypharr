package cgofuse

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs"
	"github.com/winfsp/cgofuse/fuse"
)

var subtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true,
	".sub": true, ".vtt": true, ".idx": true,
}

func isSubtitleFile(name string) bool {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return false
	}
	return subtitleExts[strings.ToLower(name[dot:])]
}

// FS implements the cgofuse FileSystemInterface
type FS struct {
	fuse.FileSystemBase // Embed base for default implementations

	vfs    *vfs.Manager
	config *config.FuseConfig
	logger zerolog.Logger

	// Handle management
	handles *HandleManager
}

// NewFS creates a new cgofuse filesystem
func NewFS(vfsManager *vfs.Manager, config *config.FuseConfig, logger zerolog.Logger) *FS {
	return &FS{
		vfs:     vfsManager,
		config:  config,
		logger:  logger,
		handles: NewHandleManager(),
	}
}

// Init is called when the filesystem is mounted
func (f *FS) Init() {
}

// Destroy is called when the filesystem is unmounted
func (f *FS) Destroy() {
	f.handles.CloseAll(func(handle *FileHandle) {
		f.releaseHandleResources(handle)
	})
}

// Statfs returns filesystem statistics
func (f *FS) Statfs(path string, stat *fuse.Statfs_t) int {
	stat.Bsize = 4096
	stat.Frsize = 4096
	stat.Blocks = 1024 * 1024 * 1024 // 4TB virtual size
	stat.Bfree = stat.Blocks / 2
	stat.Bavail = stat.Bfree
	stat.Files = 1000000
	stat.Ffree = 500000
	stat.Namemax = 255
	return 0
}

// Getattr returns file/directory attributes
func (f *FS) Getattr(path string, stat *fuse.Stat_t, fh uint64) int {
	// Root directory
	if path == "/" {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Nlink = 2
		stat.Uid = f.config.UID
		stat.Gid = f.config.GID
		now := time.Now()
		t := fuse.NewTimespec(now)
		stat.Atim = t
		stat.Mtim = t
		stat.Ctim = t
		stat.Birthtim = t
		return 0
	}

	// Parse path to get torrent/file info
	info, err := f.getFileInfo(path)
	if err != nil {
		return -fuse.ENOENT
	}

	if info.IsDir() {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Nlink = 2
	} else {
		stat.Mode = fuse.S_IFREG | 0644
		stat.Nlink = 1
		stat.Size = info.Size()
	}

	stat.Uid = f.config.UID
	stat.Gid = f.config.GID
	modTime := info.ModTime()
	t := fuse.NewTimespec(modTime)
	stat.Atim = t
	stat.Mtim = t
	stat.Ctim = t
	stat.Birthtim = t
	stat.Blksize = 512
	stat.Blocks = (stat.Size + 511) / 512

	return 0
}

// Readdir reads directory contents
func (f *FS) Readdir(path string, fill func(name string, stat *fuse.Stat_t, ofst int64) bool, ofst int64, fh uint64) int {
	// Always add . and ..
	fill(".", nil, 0)
	fill("..", nil, 0)

	if path == "/" {
		// Root directory - list all torrents/entries
		entries := f.vfs.GetManager().GetEntries()
		for _, entry := range entries {
			fill(entry.Name(), f.entryStat(&entry), 0)
		}
		return 0
	}

	// Parse path to determine level
	parts := splitPath(path)
	if len(parts) == 0 {
		return -fuse.ENOENT
	}

	groupOrTorrent := parts[0]

	if len(parts) == 1 {
		// First level directory (e.g., /__all__, /__bad__, /torrents, /nzbs, or a direct torrent)
		_, children := f.vfs.GetManager().GetEntryChildren(groupOrTorrent)
		if children == nil {
			// Try as a direct torrent directory
			_, children = f.vfs.GetManager().GetTorrentChildren(groupOrTorrent)
		}
		for _, child := range children {
			fill(child.Name(), f.entryStat(&child), 0)
		}
		return 0
	}

	// Depth 2+: e.g., /__all__/torrentname or /__all__/torrentname/subfolder
	// The second part is the torrent name
	torrentName := parts[1]

	// get the torrent's children (files)
	_, children := f.vfs.GetManager().GetTorrentChildren(torrentName)
	for _, child := range children {
		fill(child.Name(), f.entryStat(&child), 0)
	}

	return 0
}

// entryStat creates a fuse.Stat_t for a FileInfo entry
// This is used by Readdir to provide file type information to clients like Samba
func (f *FS) entryStat(info *manager.FileInfo) *fuse.Stat_t {
	stat := &fuse.Stat_t{
		Uid:     f.config.UID,
		Gid:     f.config.GID,
		Blksize: 512,
	}

	modTime := info.ModTime()
	t := fuse.NewTimespec(modTime)
	stat.Atim = t
	stat.Mtim = t
	stat.Ctim = t
	stat.Birthtim = t

	if info.IsDir() {
		stat.Mode = fuse.S_IFDIR | 0755
		stat.Nlink = 2
	} else {
		stat.Mode = fuse.S_IFREG | 0644
		stat.Nlink = 1
		stat.Size = info.Size()
		stat.Blocks = (stat.Size + 511) / 512
	}

	return stat
}

// CreateEx intercepts subtitle file creation and routes to sidecar injection.
func (f *FS) CreateEx(path string, mode uint32, fi *fuse.FileInfo_t) int {
	parts := splitPath(path)
	// Only allow subtitle writes at depth 2+ (inside a torrent folder)
	if len(parts) >= 2 && isSubtitleFile(parts[len(parts)-1]) {
		torrentName := parts[len(parts)-2]
		filename := parts[len(parts)-1]
		fh := f.handles.CreateSidecar(torrentName, filename, f.vfs.GetManager())
		fi.Fh = fh
		fi.DirectIo = true
		return 0
	}
	return -fuse.EACCES
}

// OpenEx opens a file with extended info (implements fuse.FileSystemOpenEx)
// This allows setting DirectIO which is critical for media playback on Windows
func (f *FS) OpenEx(path string, fi *fuse.FileInfo_t) int {
	fi.Fh = ^uint64(0)

	// Check if read-only access
	if fi.Flags&(os.O_WRONLY|os.O_RDWR|os.O_APPEND|os.O_CREATE|os.O_TRUNC) != 0 {
		return -fuse.EACCES
	}

	info, err := f.getFileInfo(path)
	if err != nil {
		return -fuse.ENOENT
	}

	if info.IsDir() {
		return -fuse.EISDIR
	}

	var reader *vfs.StreamingFile

	// get reader/stream for remote files
	if info.IsRemote() {
		stream, err := f.vfs.GetFile(info)
		if err != nil {
			f.logger.Error().Err(err).Str("path", path).Msg("Failed to get DFS stream file")
			return -fuse.EIO
		}
		reader = stream
	}

	// Enable DirectIO to bypass the Windows kernel cache manager.
	// Without this, WinFsp routes reads through cached I/O which expects
	// aligned reads and pre-fetches data in patterns that break streaming.
	fi.DirectIo = true

	// Create handle (reader/stream may be nil for non-remote files)
	fi.Fh = f.handles.Create(info, reader)
	return 0
}

// Open opens a file (fallback for non-OpenEx path)
func (f *FS) Open(path string, flags int) (int, uint64) {
	fi := fuse.FileInfo_t{Flags: flags}
	errc := f.OpenEx(path, &fi)
	return errc, fi.Fh
}

// Read reads from a file
func (f *FS) Read(path string, buff []byte, off int64, fh uint64) int {

	handle := f.handles.Get(fh)
	if handle == nil {
		return -fuse.EBADF
	}

	// Check bounds
	if off >= handle.info.Size() {
		return 0 // EOF
	}

	// Adjust size if reading beyond EOF
	size := len(buff)
	if off+int64(size) > handle.info.Size() {
		size = int(handle.info.Size() - off)
	}

	// Static content (e.g. version.txt): serve from in-memory buffer.
	if content := handle.info.Content(); len(content) > 0 {
		if off >= int64(len(content)) {
			return 0
		}
		end := off + int64(size)
		if end > int64(len(content)) {
			end = int64(len(content))
		}
		return copy(buff, content[off:end])
	}

	// Sidecar file (e.g. subtitle): serve from disk.
	if handle.info.IsSidecar() {
		f, err := os.Open(handle.info.SidecarPath())
		if err != nil {
			return -fuse.ENOENT
		}
		defer f.Close()
		n, _ := f.ReadAt(buff[:size], off)
		return n
	}

	if handle.reader == nil {
		return -fuse.EIO
	}

	readCtx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	n, err := handle.reader.ReadAtContext(readCtx, buff[:size], off)
	if err != nil && n == 0 {
		switch {
		case errors.Is(err, syscall.EBADF):
			return -fuse.EBADF
		case errors.Is(err, io.EOF):
			// EOF is not an error for FUSE Read
			return 0
		case errors.Is(err, context.DeadlineExceeded):
			return -fuse.ETIMEDOUT
		case errors.Is(err, context.Canceled):
			return -fuse.EINTR
		default:
			return -fuse.EIO
		}
	}

	return n
}

// Write handles writes for sidecar (subtitle) files
func (f *FS) Write(path string, buff []byte, off int64, fh uint64) int {
	handle := f.handles.Get(fh)
	if handle == nil || handle.sidecar == nil {
		return -fuse.EBADF
	}
	sc := handle.sidecar
	sc.mu.Lock()
	defer sc.mu.Unlock()
	end := int(off) + len(buff)
	if end > len(sc.buf) {
		newBuf := make([]byte, end)
		copy(newBuf, sc.buf)
		sc.buf = newBuf
	}
	copy(sc.buf[off:], buff)
	return len(buff)
}

// Release closes a file handle
func (f *FS) Release(path string, fh uint64) int {
	handle := f.handles.Get(fh)
	if handle != nil {
		if handle.sidecar != nil {
			sc := handle.sidecar
			sc.mu.Lock()
			content := make([]byte, len(sc.buf))
			copy(content, sc.buf)
			sc.mu.Unlock()
			if len(content) > 0 {
				if err := sc.mgr.InjectSidecarFile(sc.torrentName, sc.filename, content); err != nil {
					f.logger.Error().Err(err).Str("torrent", sc.torrentName).Str("file", sc.filename).Msg("Failed to inject sidecar")
				}
			}
		} else {
			f.releaseHandleResources(handle)
		}
	}
	f.handles.Delete(fh)
	return 0
}

// Opendir opens a directory
func (f *FS) Opendir(path string) (int, uint64) {

	if path == "/" {
		return 0, 0
	}

	info, err := f.getFileInfo(path)
	if err != nil {
		return -fuse.ENOENT, ^uint64(0)
	}

	if !info.IsDir() {
		return -fuse.ENOTDIR, ^uint64(0)
	}

	return 0, 0
}

// Releasedir closes a directory
func (f *FS) Releasedir(path string, fh uint64) int {
	return 0
}

// Flush is called when a file descriptor is closed
func (f *FS) Flush(path string, fh uint64) int {
	return 0
}

// Fsync synchronizes file contents
func (f *FS) Fsync(path string, datasync bool, fh uint64) int {
	return 0
}

// Unlink removes a file
func (f *FS) Unlink(path string) int {
	parts := splitPath(path)
	if len(parts) < 2 {
		return -fuse.EPERM
	}

	info, err := f.getFileInfo(path)
	if err != nil {
		return -fuse.ENOENT
	}

	if info.IsDir() {
		return -fuse.EISDIR
	}

	if err := f.vfs.GetManager().RemoveEntry(info); err != nil {
		f.logger.Error().Err(err).Str("file", info.Name()).Msg("Failed to remove file")
		return -fuse.EIO
	}

	return 0
}

// Rmdir removes a directory
func (f *FS) Rmdir(path string) int {
	parts := splitPath(path)
	if len(parts) < 1 {
		return -fuse.EPERM
	}

	info, err := f.getFileInfo(path)
	if err != nil {
		return -fuse.ENOENT
	}

	if !info.IsDir() {
		return -fuse.ENOTDIR
	}

	if err := f.vfs.GetManager().RemoveEntry(info); err != nil {
		f.logger.Error().Err(err).Str("dir", info.Name()).Msg("Failed to remove directory")
		return -fuse.EIO
	}

	return 0
}

// Access checks file access permissions
// This is a no-op - returning EACCES for write checks causes Windows media
// players to refuse to open files even for reading
func (f *FS) Access(path string, mask uint32) int {
	return 0
}

// getFileInfo resolves a path to FileInfo
func (f *FS) getFileInfo(path string) (*manager.FileInfo, error) {
	parts := splitPath(path)
	if len(parts) == 0 {
		return nil, os.ErrNotExist
	}

	name := parts[0]

	// Check if it's a top-level entry (from GetEntries)
	if len(parts) == 1 {
		// First check if it's one of the special directories or version.txt
		entries := f.vfs.GetManager().GetEntries()
		for _, entry := range entries {
			if entry.Name() == name {
				// Return a copy to avoid modifying the original
				info := entry
				return &info, nil
			}
		}
		// Not found in root entries, try as a torrent in one of the groups
		return f.vfs.GetManager().GetTorrentEntry(name)
	}

	// Depth 2: could be a torrent inside __all__, or a file inside a torrent
	if len(parts) == 2 {
		groupOrTorrent := parts[0]
		entryName := parts[1]

		// Check if first part is a special group (__all__, __bad__, torrents, nzbs)
		_, children := f.vfs.GetManager().GetEntryChildren(groupOrTorrent)
		for _, child := range children {
			if child.Name() == entryName {
				info := child
				return &info, nil
			}
		}

		// Otherwise treat first part as torrent name and second as filename
		return f.vfs.GetManager().GetTorrentFile(groupOrTorrent, entryName)
	}

	// Depth 3+: file within a torrent inside a group
	// Path: /group/torrent/file
	torrentName := parts[1]
	filename := strings.Join(parts[2:], "/")
	return f.vfs.GetManager().GetTorrentFile(torrentName, filename)
}

// splitPath splits a path into components
func splitPath(path string) []string {
	path = strings.Trim(path, "/")
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

func (f *FS) releaseHandleResources(handle *FileHandle) {
	if handle == nil {
		return
	}

	if handle.reader != nil && handle.info != nil {
		if err := handle.reader.Close(); err != nil && !customerror.IsSilentError(err) {
			f.logger.Debug().Err(err).Msg("Failed to close VFS reader")
		}
		f.vfs.ReleaseFile(handle.info)
		handle.reader = nil
	}
}

// HandleManager manages file handles
type HandleManager struct {
	handles *xsync.Map[uint64, *FileHandle]
	nextFH  atomic.Uint64
}

// FileHandle represents an open file
type FileHandle struct {
	info    *manager.FileInfo
	reader  *vfs.StreamingFile
	sidecar *SidecarHandle
}

// SidecarHandle buffers writes for subtitle injection
type SidecarHandle struct {
	torrentName string
	filename    string
	mgr         *manager.Manager
	mu          sync.Mutex
	buf         []byte
}

// NewHandleManager creates a new handle manager
func NewHandleManager() *HandleManager {
	hm := &HandleManager{
		handles: xsync.NewMap[uint64, *FileHandle](),
	}
	hm.nextFH.Store(1)
	return hm
}

// Create creates a new handle
func (h *HandleManager) Create(info *manager.FileInfo, reader *vfs.StreamingFile) uint64 {
	fh := h.nextFH.Load()
	h.nextFH.Add(1)
	h.handles.Store(fh, &FileHandle{
		info:   info,
		reader: reader,
	})
	return fh
}

// CreateSidecar creates a handle for buffered subtitle injection
func (h *HandleManager) CreateSidecar(torrentName, filename string, mgr *manager.Manager) uint64 {
	fh := h.nextFH.Load()
	h.nextFH.Add(1)
	h.handles.Store(fh, &FileHandle{
		sidecar: &SidecarHandle{
			torrentName: torrentName,
			filename:    filename,
			mgr:         mgr,
		},
	})
	return fh
}

// Get returns a handle by ID
func (h *HandleManager) Get(fh uint64) *FileHandle {
	fhi, ok := h.handles.Load(fh)
	if !ok {
		return nil
	}
	return fhi
}

// Delete removes a handle
func (h *HandleManager) Delete(fh uint64) {
	h.handles.Delete(fh)
}

// CloseAll closes all handles
func (h *HandleManager) CloseAll(cleanup func(*FileHandle)) {
	h.handles.Range(func(key uint64, handle *FileHandle) bool {
		if cleanup != nil {
			cleanup(handle)
		}
		h.handles.Delete(key)
		return true
	})
}

// Interface assertions
var (
	_ fuse.FileSystemInterface = (*FS)(nil)
	_ fuse.FileSystemOpenEx    = (*FS)(nil)
)
