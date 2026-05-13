//go:build windows

package vfs

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func markSparseFile(f *os.File) error {
	if f == nil {
		return fmt.Errorf("nil file")
	}

	var bytesReturned uint32
	if err := windows.DeviceIoControl(
		windows.Handle(f.Fd()),
		windows.FSCTL_SET_SPARSE,
		nil,
		0,
		nil,
		0,
		&bytesReturned,
		nil,
	); err != nil {
		return err
	}

	return nil
}
