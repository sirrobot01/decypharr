//go:build !windows

package decypharr

import "syscall"

func SetUmask(umask int) {
	syscall.Umask(umask)
}
