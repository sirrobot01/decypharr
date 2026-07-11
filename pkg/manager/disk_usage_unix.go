//go:build !windows

package manager

import (
	"os"
	"syscall"
)

// fileDiskUsage returns the actual disk space info's file occupies (Blocks *
// 512), not its logical length - the two diverge for sparse files. The DFS
// cache stores files preallocated to their full logical length with only
// downloaded chunks actually written (the ffprobe repair sweep's header/moov reads
// create one such sparse file for nearly every entry), so a plain
// info.Size() badly over-reports what deleting a cache dir would actually
// reclaim. Falls back to info.Size() if Sys() doesn't yield a *syscall.Stat_t
// - should be unreachable given the !windows build tag, but a fallback is
// cheap and keeps this safe against an exotic unix variant with a different
// Sys() type.
func fileDiskUsage(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Blocks * 512
	}
	return info.Size()
}
