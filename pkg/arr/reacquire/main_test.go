package reacquire

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "decypharr-reacquire-tests-")
	if err != nil {
		panic(err)
	}
	config.SetConfigPath(directory)
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}
