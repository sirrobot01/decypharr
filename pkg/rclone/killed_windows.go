//go:build windows

package rclone

import (
	"errors"
	"os/exec"
	"syscall"
)

func WasHardTerminated(err error) bool {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	// No Signaled() on Windows; consider "hard terminated" if not success.
	return ws.ExitStatus() != 0 // Use the ExitStatus() method
}

// ExitCode returns the process exit code when available.
func ExitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	ws, ok := ee.Sys().(syscall.WaitStatus)
	if !ok {
		return 0, false
	}
	return ws.ExitStatus(), true // Use the ExitStatus() method
}
