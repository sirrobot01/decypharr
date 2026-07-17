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
	// Backup marks this provider as a fallback tier. Backups are only
	// consulted when every non-backup ("primary") provider is excluded
	// — e.g. all primaries returned article-not-found or had connection
	// errors. They are NOT used just because a primary's pool is busy;
	// the request waits for a primary slot instead. This matches the
	// "unlimited primary + block backup for completion" model that most
	// other Usenet clients implement, and prevents block providers from
	// being billed for articles the unlimited could have served.
	Backup bool `json:"backup,omitempty"`
}

// Usenet configuration for usenet streaming and downloading
type Usenet struct {
	Providers []UsenetProvider `json:"providers,omitempty"` // Usenet provider configurations
	// Per-stream/file configuration
	MaxConnections           int `json:"max_connections,omitempty"`            // Maximum concurrent connections per streaming file (default: 15)
	ProcessingMaxConnections int `json:"processing_max_connections,omitempty"` // Maximum concurrent connections per file for parsing and NZB downloads (default: max_connections)
	// Read-ahead configuration
	ReadAhead string `json:"read_ahead,omitempty"` // Bytes to prefetch ahead of streaming reads e.g. "16MB", "32MB" (default: 16MB)
	// SocketReadBuffer / SocketWriteBuffer set the per-connection TCP
	// SO_RCVBUF / SO_SNDBUF (e.g. "4MB"). At high RTT a single connection's
	// throughput is capped at roughly buffer ÷ RTT, so the receive buffer must
	// cover the bandwidth-delay product (BDP = link_speed × RTT). "0" leaves
	// OS autotuning in charge. Note: the OS still caps these
	// (Linux net.core.rmem_max/wmem_max, macOS kern.ipc.maxsockbuf) — raise
	// those sysctls too to actually get large windows. Defaults: 4MB / 1MB.
	SocketReadBuffer  string `json:"socket_read_buffer,omitempty"`
	SocketWriteBuffer string `json:"socket_write_buffer,omitempty"`
	// Processing timeout
	ProcessingTimeout string `json:"processing_timeout,omitempty"` // Timeout for NZB processing e.g. "5m", "10m" (default: 10m). Mark as bad if exceeded.
	// Availability check sampling
	AvailabilitySamplePercent       int    `json:"availability_sample_percent,omitempty"`        // Percentage of segments to check during repair (1-100, default: 10)
	ImportAvailabilitySamplePercent int    `json:"import_availability_sample_percent,omitempty"` // Percentage of segments to check when adding an NZB (1-100, default: 1)
	DiskBufferPath                  string `json:"disk_buffer_path,omitempty"`                   // Path for disk buffer storage (empty = main_path/usenet/streams)

	// BufferMemory caps the total RAM the usenet streaming buffers hold across
	// all open streams, e.g. "512MB". Per-stream buffers stay generous for
	// smooth playback; this bounds the aggregate so many concurrent streams
	// can't OOM. Empty = default (512MB); "0" disables the cap.
	BufferMemory string `json:"buffer_memory,omitempty"`
}

// BufferMemoryBytes resolves the usenet streaming-buffer RAM cap. Empty ->
// 512MB default; "0" -> disabled (0).
func (u Usenet) BufferMemoryBytes() int64 {
	if u.BufferMemory == "" {
		return 512 << 20
	}
	n, err := ParseSize(u.BufferMemory)
	if err != nil {
		return 512 << 20
	}
	return n
}

func (u Usenet) IsZero() bool {
	return len(u.Providers) == 0 && u.MaxConnections == 0 && u.ProcessingMaxConnections == 0 && u.ReadAhead == "" && u.ProcessingTimeout == ""
}

func (c *Config) updateUsenetConfig() {
	// Per-stream configuration defaults
	if c.Usenet.MaxConnections == 0 {
		c.Usenet.MaxConnections = 15 // Default: 15 connections per file
	}
	if c.Usenet.ProcessingMaxConnections <= 0 {
		c.Usenet.ProcessingMaxConnections = c.Usenet.MaxConnections
	}

	// Read-ahead default - bytes to prefetch ahead of reads
	if c.Usenet.ReadAhead == "" {
		c.Usenet.ReadAhead = "16MB" // Default: 16MB read-ahead buffer
	}

	// TCP socket buffer defaults sized for high-RTT BDP. "0" (explicit) opts
	// into OS autotuning, so only fill when unset.
	if c.Usenet.SocketReadBuffer == "" {
		c.Usenet.SocketReadBuffer = "4MB"
	}
	if c.Usenet.SocketWriteBuffer == "" {
		c.Usenet.SocketWriteBuffer = "1MB"
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
	if c.Usenet.ImportAvailabilitySamplePercent <= 0 {
		c.Usenet.ImportAvailabilitySamplePercent = 1
	} else if c.Usenet.ImportAvailabilitySamplePercent > 100 {
		c.Usenet.ImportAvailabilitySamplePercent = 100
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
	// Auto-enable TLS for ports that only speak implicit TLS.
	// Users who set port 563 (NNTPS) or 443 without ssl:true get a
	// plain-TCP connection; the server waits for a TLS ClientHello and
	// never sends the greeting, causing a 10-second i/o timeout.
	if !u.SSL && (u.Port == 563 || u.Port == 443) {
		u.SSL = true
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
	processingMaxConns := getEnv("USENET__PROCESSING_MAX_CONNECTIONS")
	if maxConns := getEnv("USENET__MAX_CONNECTIONS"); maxConns != "" {
		if v, err := strconv.Atoi(maxConns); err == nil {
			c.Usenet.MaxConnections = v
			if processingMaxConns == "" {
				c.Usenet.ProcessingMaxConnections = v
			}
		}
	}
	if processingMaxConns != "" {
		if v, err := strconv.Atoi(processingMaxConns); err == nil {
			c.Usenet.ProcessingMaxConnections = v
		}
	}

	if readAhead := getEnv("USENET__READ_AHEAD"); readAhead != "" {
		c.Usenet.ReadAhead = readAhead
	}

	if v := getEnv("USENET__SOCKET_READ_BUFFER"); v != "" {
		c.Usenet.SocketReadBuffer = v
	}

	if v := getEnv("USENET__SOCKET_WRITE_BUFFER"); v != "" {
		c.Usenet.SocketWriteBuffer = v
	}

	if processingTimeout := getEnv("USENET__PROCESSING_TIMEOUT"); processingTimeout != "" {
		c.Usenet.ProcessingTimeout = processingTimeout
	}

	if availabilitySample := getEnv("USENET__AVAILABILITY_SAMPLE_PERCENT"); availabilitySample != "" {
		if v, err := strconv.Atoi(availabilitySample); err == nil {
			c.Usenet.AvailabilitySamplePercent = v
		}
	}
	if availabilitySample := getEnv("USENET__IMPORT_AVAILABILITY_SAMPLE_PERCENT"); availabilitySample != "" {
		if v, err := strconv.Atoi(availabilitySample); err == nil {
			c.Usenet.ImportAvailabilitySamplePercent = v
		}
	}

	// Usenet providers array
	for i := range 10 { // Support up to 10 usenet providers
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

			if backup := getEnv(prefix + "BACKUP"); backup != "" {
				c.Usenet.Providers[i].Backup = parseBool(backup)
			}
		}
	}
}
