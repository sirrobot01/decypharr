//go:build linux

package buffer

import (
	"os"

	"golang.org/x/sys/unix"
)

// punchHole deallocates [offset, offset+length) in f via
// fallocate(FALLOC_FL_PUNCH_HOLE | FALLOC_FL_KEEP_SIZE).
//
// On ext4/xfs/btrfs this returns the data blocks to the filesystem; on
// tmpfs it directly returns RAM pages — which is the whole point of
// supporting punch on streaming caches mounted on /dev/shm or similar.
//
// FALLOC_FL_KEEP_SIZE preserves the logical file size so the fixed
// per-offset write addresses remain valid for re-fetches that overwrite
// the same slot.
func punchHole(f *os.File, offset, length int64) error {
	if f == nil || length <= 0 {
		return nil
	}
	return unix.Fallocate(int(f.Fd()),
		unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE,
		offset, length)
}
