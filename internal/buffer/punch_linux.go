//go:build linux

package buffer

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// prepareSparse is a no-op: Linux files are sparse by default.
func prepareSparse(*os.File) error { return nil }

// punchHole deallocates [offset, offset+length). KEEP_SIZE preserves the
// logical size so fixed per-offset write addresses stay valid.
func punchHole(f *os.File, offset, length int64) error {
	if f == nil || length <= 0 {
		return nil
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := sc.Control(func(fd uintptr) {
		opErr = unix.Fallocate(int(fd),
			unix.FALLOC_FL_PUNCH_HOLE|unix.FALLOC_FL_KEEP_SIZE, offset, length)
	}); err != nil {
		return err
	}
	if errors.Is(opErr, unix.EOPNOTSUPP) || errors.Is(opErr, unix.ENOTSUP) {
		return errPunchUnsupported
	}
	return opErr
}
