package nntp

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

func TestMain(m *testing.M) {
	configDir, err := os.MkdirTemp("", "decypharr-nntp-test-")
	if err != nil {
		panic(err)
	}

	config.SetConfigPath(configDir)
	// NNTP deadlines come from utils.Now(), which stays frozen at process
	// start until the global cached clock updater runs (production starts it
	// in main.go). Without it, a test process older than the handshake
	// timeout fails every NNTP dial with an instant i/o timeout.
	utils.StartGlobalCachedTime()
	code := m.Run()
	_ = os.RemoveAll(configDir)
	os.Exit(code)
}
