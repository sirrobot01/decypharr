//go:build !windows

package rclone

import (
	"errors"
	"os/exec"
	"syscall"
)

// WasHardTerminated reports true iff the process was ended by SIGKILL or SIGTERM.
func WasHardTerminated(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok || !ws.Signaled() {
		return false
	}
	sig := ws.Signal()
	return sig == syscall.SIGKILL || sig == syscall.SIGTERM
}

// ExitCode returns the numeric exit code when available.
func ExitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, false
	}
	if ws.Exited() {
		return ws.ExitStatus(), true
	}
	// Conventional shell “killed by signal” code is 128 + signal.
	if ws.Signaled() {
		return 128 + int(ws.Signal()), true
	}
	return 0, false
}
