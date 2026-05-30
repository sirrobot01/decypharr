//go:build linux || (darwin && amd64)

package hanwen

import (
	"context"
	"strings"
	"sync"
	"syscall"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
	"github.com/sirrobot01/decypharr/pkg/manager"
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

// SidecarFile is a writable FUSE node for subtitle files being injected.
type SidecarFile struct {
	fs.Inode
	torrentName string
	filename    string
	mgr         *manager.Manager
}

var _ = (fs.NodeCreater)((*Dir)(nil))

// Create handles file creation in a torrent folder — only allows subtitle extensions.
func (d *Dir) Create(ctx context.Context, name string, flags uint32, mode uint32, out *fuse.EntryOut) (node *fs.Inode, fh fs.FileHandle, fuseFlags uint32, errno syscall.Errno) {
	if d.level != LevelFile {
		return nil, nil, 0, syscall.EPERM
	}
	if !isSubtitleFile(name) {
		return nil, nil, 0, syscall.EACCES
	}

	sf := &SidecarFile{
		torrentName: d.name,
		filename:    name,
		mgr:         d.vfs.GetManager(),
	}

	out.Attr.Mode = fuse.S_IFREG | 0644
	out.Attr.Uid = d.config.UID
	out.Attr.Gid = d.config.GID
	out.AttrValid = uint64(AttrTimeout.Seconds())
	out.EntryValid = uint64(EntryTimeout.Seconds())

	inode := d.NewInode(ctx, sf, fs.StableAttr{Mode: fuse.S_IFREG | 0644})
	handle := &SidecarWriteHandle{sf: sf}
	return inode, handle, fuse.FOPEN_DIRECT_IO, 0
}

// SidecarWriteHandle buffers writes and injects on release.
type SidecarWriteHandle struct {
	sf  *SidecarFile
	mu  sync.Mutex
	buf []byte
}

var (
	_ = (fs.FileWriter)((*SidecarWriteHandle)(nil))
	_ = (fs.FileReleaser)((*SidecarWriteHandle)(nil))
	_ = (fs.FileFlusher)((*SidecarWriteHandle)(nil))
)

func (h *SidecarWriteHandle) Write(ctx context.Context, data []byte, off int64) (uint32, syscall.Errno) {
	h.mu.Lock()
	defer h.mu.Unlock()
	end := int(off) + len(data)
	if end > len(h.buf) {
		newBuf := make([]byte, end)
		copy(newBuf, h.buf)
		h.buf = newBuf
	}
	copy(h.buf[off:], data)
	return uint32(len(data)), 0
}

func (h *SidecarWriteHandle) Flush(ctx context.Context) syscall.Errno {
	return 0
}

func (h *SidecarWriteHandle) Release(ctx context.Context) syscall.Errno {
	h.mu.Lock()
	content := make([]byte, len(h.buf))
	copy(content, h.buf)
	h.mu.Unlock()

	if len(content) == 0 {
		return 0
	}
	if err := h.sf.mgr.InjectSidecarFile(h.sf.torrentName, h.sf.filename, content); err != nil {
		return syscall.EIO
	}
	return 0
}

// Open for SidecarFile — returns a fresh write handle so the kernel can write after create.
func (sf *SidecarFile) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	return &SidecarWriteHandle{sf: sf}, fuse.FOPEN_DIRECT_IO, 0
}

// Getattr for SidecarFile
func (sf *SidecarFile) Getattr(ctx context.Context, fh fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	out.Mode = fuse.S_IFREG | 0644
	out.Nlink = 1
	return 0
}
