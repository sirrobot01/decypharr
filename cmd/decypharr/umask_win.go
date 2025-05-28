//go:build windows
// +build windows

package decypharr

func SetUmask(umask int) {
	// No-op on Windows
}
