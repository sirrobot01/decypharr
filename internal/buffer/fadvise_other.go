//go:build !linux

package buffer

import "os"

// Non-Linux platforms: posix_fadvise isn't portable enough to be worth
// per-OS implementations. On Darwin/Windows we just rely on the kernel's
// default heuristics; the hot paths are unaffected.

func adviseSequential(_ *os.File)           {}
func adviseDontNeed(_ *os.File, _, _ int64) {}
func adviseDontNeedAll(_ *os.File)          {}
func adviseWillNeed(_ *os.File, _, _ int64) {}
