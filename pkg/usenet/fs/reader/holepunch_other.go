//go:build !linux

package reader

import "os"

// punchHole is a no-op where hole-punching isn't available. The sparse file is
// still overwritten in place on re-fetch, so correctness is unaffected; only
// the tmpfs/ramdisk RAM-reclaim optimization is unavailable on these platforms.
func punchHole(_ *os.File, _, _ int64) error { return nil }
