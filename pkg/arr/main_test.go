package arr

import (
	"os"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestMain(m *testing.M) {
	directory, err := os.MkdirTemp("", "decypharr-arr-tests-")
	if err != nil {
		panic(err)
	}
	config.SetConfigPath(directory)
	code := m.Run()
	_ = os.RemoveAll(directory)
	os.Exit(code)
}

// testService registers one instance named "arr" and returns the service.
func testService(instance Arr) *Service {
	instance.Name = "arr"
	service := New()
	service.arrs[instance.Name] = instance
	return service
}
