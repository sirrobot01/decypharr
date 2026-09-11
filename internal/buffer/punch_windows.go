//go:build windows

package buffer

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type zeroDataInformation struct {
	FileOffset      int64
	BeyondFinalZero int64
}

// prepareSparse marks f sparse. Required before punchHole is safe, and before
// truncating to the stream's logical size: NTFS reserves every cluster of a
// non-sparse file of that length.
func prepareSparse(f *os.File) error {
	if f == nil {
		return nil
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var opErr error
	if err := sc.Control(func(fd uintptr) {
		var n uint32
		opErr = windows.DeviceIoControl(windows.Handle(fd), windows.FSCTL_SET_SPARSE, nil, 0, nil, 0, &n, nil)
	}); err != nil {
		return err
	}
	if errors.Is(opErr, windows.ERROR_INVALID_FUNCTION) || errors.Is(opErr, windows.ERROR_NOT_SUPPORTED) {
		return errPunchUnsupported
	}
	return opErr
}

// punchHole deallocates [offset, offset+length) via FSCTL_SET_ZERO_DATA. Only
// safe on a sparse file — on a plain one it physically writes zeros. Buffer
// gates this on prepareSparse having succeeded.
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
		info := zeroDataInformation{FileOffset: offset, BeyondFinalZero: offset + length}
		var n uint32
		opErr = windows.DeviceIoControl(windows.Handle(fd), windows.FSCTL_SET_ZERO_DATA,
			(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil, 0, &n, nil)
	}); err != nil {
		return err
	}
	if errors.Is(opErr, windows.ERROR_INVALID_FUNCTION) || errors.Is(opErr, windows.ERROR_NOT_SUPPORTED) {
		return errPunchUnsupported
	}
	return opErr
}
