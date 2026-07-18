//go:build windows

package manager

import "os"

// fileDiskUsage falls back to the logical file length on Windows: there's no
// portable *syscall.Stat_t/Blocks equivalent via os.FileInfo here, and
// decypharr's DFS cache backend isn't built for Windows anyway (see
// pkg/mount/dfs/vfs/sparse_windows.go), so the sparse-file undercounting
// this exists to fix doesn't apply on this platform.
func fileDiskUsage(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}
