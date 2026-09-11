//go:build !windows

package buffer

import (
	"syscall"
	"testing"
)

// allocatedBlocks reports the 512-byte blocks the filesystem has actually
// allocated to path, which is how a punch is observed from outside.
func allocatedBlocks(t *testing.T, path string) int64 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	return int64(st.Blocks)
}
