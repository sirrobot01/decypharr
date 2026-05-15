package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
)

type UsenetProvider struct {
	Host           string `json:"host,omitempty"` // Host of the usenet server
	Port           int    `json:"port,omitempty"` // Port of the usenet server
	Username       string `json:"username,omitempty"`
	Password       string `json:"password,omitempty"`
	Backbone       string `json:"backbone,omitempty"`        // Shared article backbone identifier used for failover decisions
	MaxConnections int    `json:"max_connections,omitempty"` // Max connections for this provider (default: 10)
	SSL            bool   `json:"ssl,omitempty"`             // Use SSL/TLS for the connection
	Priority       int    `json:"priority,omitempty"`        // Priority for this provider (lower = higher priority)
}

// Usenet configuration for usenet streaming and downloading
type Usenet struct {
	Providers []UsenetProvider `json:"providers,omitempty"` // Usenet provider configurations
	// Per-stream/file configuration
	MaxConnections int `json:"max_connections,omitempty"` // Maximum concurrent connections per file for parsing and streaming (default: 10)
	// Read-ahead configuration
	ReadAhead string `json:"read_ahead,omitempty"` // Bytes to prefetch ahead of reads e.g. "16MB", "32MB" (default: 128MB)
	// Processing timeout
	ProcessingTimeout string `json:"processing_timeout,omitempty"` // Timeout for NZB processing e.g. "5m", "10m" (default: 10m). Mark as bad if exceeded.
	// Availability check sampling
	AvailabilitySamplePercent int `json:"availability_sample_percent,omitempty"` // Percentage of segments to check for availability (1-100, default: 100 = check all)
	// Max concurrent NZB processing
	MaxConcurrentNZB int `json:"max_concurrent_nzb,omitempty"` // Maximum NZBs to process in parallel (default: 2)

	DiskBufferPath string `json:"disk_buffer_path,omitempty"` // Path for disk buffer storage (empty = main_path/usenet/streams)
}

func (u Usenet) IsZero() bool {
	return len(u.Providers) == 0 && u.MaxConnections == 0 && u.ReadAhead == "" && u.ProcessingTimeout == ""
}

func (c *Config) updateUsenetConfig() {
	// Per-stream configuration defaults
	if c.Usenet.MaxConnections == 0 {
		c.Usenet.MaxConnections = 15 // Default: 15 connections per file
	}

	// Read-ahead default - bytes to prefetch ahead of reads
	if c.Usenet.ReadAhead == "" {
		c.Usenet.ReadAhead = "16MB" // Default: 16MB read-ahead buffer
	}

	// Processing timeout default
	if c.Usenet.ProcessingTimeout == "" {
		c.Usenet.ProcessingTimeout = "10m" // Default: 10 minutes for NZB processing
	}

	// CacheDir: empty = system temp folder (no default needed)

	// Availability sample percent default - clamp to valid range
	if c.Usenet.AvailabilitySamplePercent <= 0 {
		c.Usenet.AvailabilitySamplePercent = 10
	} else if c.Usenet.AvailabilitySamplePercent > 100 {
		c.Usenet.AvailabilitySamplePercent = 100
	}

	// Max concurrent NZB processing default
	if c.Usenet.MaxConcurrentNZB <= 0 {
		c.Usenet.MaxConcurrentNZB = 2 // Default: 2 NZBs processed in parallel
	}

	if c.Usenet.DiskBufferPath == "" {
		c.Usenet.DiskBufferPath = filepath.Join(GetMainPath(), "usenet", "streams")
	}

	for i, provider := range c.Usenet.Providers {
		c.Usenet.Providers[i] = c.updateUsenetProvider(i, provider)
	}
}

func (c *Config) updateUsenetProvider(index int, u UsenetProvider) UsenetProvider {
	if u.Port == 0 {
		u.Port = 119 // Default port for usenet
	}
	if u.MaxConnections == 0 {
		u.MaxConnections = 20 // Default max connections per provider
	}
	if u.Priority == 0 {
		u.Priority = index + 1 // Default priority based on order
	}
	return u
}

func validateUsenet(providers []UsenetProvider) error {
	if len(providers) == 0 {
		return nil
	}
	for _, usenet := range providers {
		// Basic field validation
		if usenet.Host == "" {
			return errors.New("usenet provider host is required")
		}
		if usenet.Username == "" {
			return errors.New("usenet provider username is required")
		}
		if usenet.Password == "" {
			return errors.New("usenet provider password is required")
		}
	}

	return nil
}

func (c *Config) applyUsenetEnvVars() {
	// Per-stream configuration
	if maxConns := getEnv("USENET__MAX_CONNECTIONS"); maxConns != "" {
		if v, err := strconv.Atoi(maxConns); err == nil {
			c.Usenet.MaxConnections = v
		}
	}

	if readAhead := getEnv("USENET__READ_AHEAD"); readAhead != "" {
		c.Usenet.ReadAhead = readAhead
	}

	if processingTimeout := getEnv("USENET__PROCESSING_TIMEOUT"); processingTimeout != "" {
		c.Usenet.ProcessingTimeout = processingTimeout
	}

	if availabilitySample := getEnv("USENET__AVAILABILITY_SAMPLE_PERCENT"); availabilitySample != "" {
		if v, err := strconv.Atoi(availabilitySample); err == nil {
			c.Usenet.AvailabilitySamplePercent = v
		}
	}

	if maxConcurrentNZB := getEnv("USENET__MAX_CONCURRENT_NZB"); maxConcurrentNZB != "" {
		if v, err := strconv.Atoi(maxConcurrentNZB); err == nil {
			c.Usenet.MaxConcurrentNZB = v
		}
	}

	// Usenet providers array
	for i := 0; i < 10; i++ { // Support up to 10 usenet providers
		prefix := fmt.Sprintf("USENET__PROVIDERS__%d__", i)
		if val := getEnv(prefix + "HOST"); val != "" {
			// Ensure array is large enough
			if i >= len(c.Usenet.Providers) {
				c.Usenet.Providers = append(c.Usenet.Providers, make([]UsenetProvider, i-len(c.Usenet.Providers)+1)...)
			}
			c.Usenet.Providers[i].Host = val

			if port := getEnv(prefix + "PORT"); port != "" {
				if v, err := strconv.Atoi(port); err == nil {
					c.Usenet.Providers[i].Port = v
				}
			}
			if username := getEnv(prefix + "USERNAME"); username != "" {
				c.Usenet.Providers[i].Username = username
			}
			if password := getEnv(prefix + "PASSWORD"); password != "" {
				c.Usenet.Providers[i].Password = password
			}
			if backbone := getEnv(prefix + "BACKBONE"); backbone != "" {
				c.Usenet.Providers[i].Backbone = backbone
			}
			if maxConnections := getEnv(prefix + "MAX_CONNECTIONS"); maxConnections != "" {
				if v, err := strconv.Atoi(maxConnections); err == nil {
					c.Usenet.Providers[i].MaxConnections = v
				}
			}
			if ssl := getEnv(prefix + "SSL"); ssl != "" {
				c.Usenet.Providers[i].SSL = parseBool(ssl)
			}

			if priority := getEnv(prefix + "PRIORITY"); priority != "" {
				if v, err := strconv.Atoi(priority); err == nil {
					c.Usenet.Providers[i].Priority = v
				}
			}
		}
	}
}
