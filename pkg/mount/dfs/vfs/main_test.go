package vfs

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

// TestMain pins the global config to a temp dir: several dependencies
// (logger.Default via the rate-limited logger, the buffer pools) lazily call
// config.Get, which os.Exit(1)s when no config path is set.
func TestMain(m *testing.M) {
	configDir, err := os.MkdirTemp("", "decypharr-vfs-test-")
	if err != nil {
		panic(err)
	}

	config.SetConfigPath(configDir)
	code := m.Run()
	_ = os.RemoveAll(configDir)
	os.Exit(code)
}
