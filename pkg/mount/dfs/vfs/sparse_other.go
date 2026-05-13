//go:build !windows

package vfs

import "os"

func markSparseFile(_ *os.File) error {
	return nil
}
