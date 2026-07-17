//go:build !windows

package nntp

import "golang.org/x/sys/unix"

func setSocketBuffers(fd uintptr, readBuffer, writeBuffer int) {
	if readBuffer > 0 {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_RCVBUF, readBuffer)
	}
	if writeBuffer > 0 {
		_ = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_SNDBUF, writeBuffer)
	}
}
