//go:build linux

package reader

import (
	"os"

	"golang.org/x/sys/unix"
)

// punchHole deallocates [offset, offset+length) in f, returning the storage to
// the filesystem. On tmpfs/ramdisk this frees the backing RAM pages; on a
// regular filesystem it makes the region sparse again. The file size is
// preserved (KEEP_SIZE) so the fixed per-segment offsets stay valid and a
// re-fetch can rewrite in place.
func punchHole(f *os.File, offset, length int64) error {
	if f == nil || length <= 0 {
		return nil
	}
	return unix.Fallocate(int(f.Fd()),
		unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, offset, length)
}
