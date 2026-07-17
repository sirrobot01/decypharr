//go:build windows

package nntp

import "syscall"

func setSocketBuffers(fd uintptr, readBuffer, writeBuffer int) {
	handle := syscall.Handle(fd)
	if readBuffer > 0 {
		_ = syscall.SetsockoptInt(handle, syscall.SOL_SOCKET, syscall.SO_RCVBUF, readBuffer)
	}
	if writeBuffer > 0 {
		_ = syscall.SetsockoptInt(handle, syscall.SOL_SOCKET, syscall.SO_SNDBUF, writeBuffer)
	}
}
