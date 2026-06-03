//go:build !linux

package buffer

import "os"

// punchHole is a no-op on platforms without fallocate(PUNCH_HOLE) support.
// The sparse file is reclaimed on Close (the file is closed and, if it was
// a temp file, removed); during runtime, evicted slots are simply overwritten
// in place on re-write, so correctness is preserved. Only the eager RAM/disk
// reclamation optimization is unavailable here.
func punchHole(_ *os.File, _, _ int64) error { return nil }
