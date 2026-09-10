package config

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	json "github.com/bytedance/sonic"
)

var (
	instance   atomic.Pointer[Config]
	configMu   sync.Mutex
	configPath atomic.Value
)

func SetConfigPath(path string) { configPath.Store(path) }

func GetMainPath() string {
	value, _ := configPath.Load().(string)
	return value
}

// Get returns the current read-only snapshot. Use Update to change it.
func Get() *Config {
	if current := instance.Load(); current != nil {
		return current
	}
	configMu.Lock()
	defer configMu.Unlock()
	if current := instance.Load(); current != nil {
		return current
	}
	current := &Config{}
	if err := current.loadConfig(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "configuration error: %v\n", err)
		os.Exit(1)
	}
	instance.Store(current)
	return current
}

// Clone returns an independent copy for editing.
func (c *Config) Clone() (*Config, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	var snapshot Config
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if c.Auth != nil {
		snapshot.Auth = new(*c.Auth)
	}
	return &snapshot, nil
}

// Update saves and publishes a new snapshot. The callback edits a private copy.
// Do not call Get or Update from the callback.
func Update(edit func(*Config) error) (*Config, error) {
	Get()
	configMu.Lock()
	defer configMu.Unlock()
	current := instance.Load()
	if current == nil {
		return nil, errors.New("configuration was reset during update; retry the update")
	}
	next, err := current.Clone()
	if err != nil {
		return nil, fmt.Errorf("copy configuration: %w", err)
	}
	if err := edit(next); err != nil {
		return nil, err
	}
	if err := next.Save(); err != nil {
		return nil, err
	}
	published, err := next.Clone()
	if err != nil {
		return nil, fmt.Errorf("copy saved configuration: %w", err)
	}
	instance.Store(published)
	return published, nil
}

func Reset() {
	configMu.Lock()
	defer configMu.Unlock()
	instance.Store(nil)
}
