package utils

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestMain(m *testing.M) {
	configDir, err := os.MkdirTemp("", "decypharr-utils-test-")
	if err != nil {
		panic(err)
	}

	config.SetConfigPath(configDir)
	code := m.Run()
	_ = os.RemoveAll(configDir)
	os.Exit(code)
}
