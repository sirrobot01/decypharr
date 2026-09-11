//go:build darwin

package buffer

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fpunchhole is struct fpunchhole from <sys/fcntl.h>; x/sys/unix has no binding.
type fpunchhole struct {
	flags    uint32
	reserved uint32
	offset   int64
	length   int64
}

// prepareSparse is a no-op: APFS and HFS+ files are sparse by default.
func prepareSparse(*os.File) error { return nil }

// punchHole deallocates [offset, offset+length) via fcntl(F_PUNCHHOLE), which
// requires block-aligned ranges. The range is trimmed inward so the unaligned
// edges are left on disk rather than taking neighbouring live data with them.
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
		var st unix.Statfs_t
		if opErr = unix.Fstatfs(int(fd), &st); opErr != nil {
			return
		}
		bs := int64(st.Bsize)
		if bs <= 0 {
			opErr = errPunchUnsupported
			return
		}
		start := (offset + bs - 1) &^ (bs - 1)
		end := (offset + length) &^ (bs - 1)
		if end <= start {
			return
		}
		arg := fpunchhole{offset: start, length: end - start}
		if _, _, errno := unix.Syscall(unix.SYS_FCNTL, fd,
			uintptr(unix.F_PUNCHHOLE), uintptr(unsafe.Pointer(&arg))); errno != 0 {
			opErr = errno
		}
	}); err != nil {
		return err
	}
	if errors.Is(opErr, unix.ENOTSUP) || errors.Is(opErr, unix.ENOTTY) || errors.Is(opErr, unix.EINVAL) {
		return errPunchUnsupported
	}
	return opErr
}
