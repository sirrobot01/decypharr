//go:build windows

package buffer

import "testing"

// allocatedBlocks has no cheap equivalent on Windows (it needs
// GetCompressedFileSize, which x/sys/windows does not bind). Returning 0
// makes TestPunchHoleReclaims skip the reclamation assertion there; the
// zero-read and logical-size assertions still run.
func allocatedBlocks(_ *testing.T, _ string) int64 { return 0 }
