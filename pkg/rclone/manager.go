package rclone

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
)

// Manager handles the rclone RC server and provides mount operations
type Manager struct {
	cmd           *exec.Cmd
	rcPort        string
	rcUser        string
	rcPass        string
	configDir     string
	mounts        map[string]*MountInfo
	mountsMutex   sync.RWMutex
	logger        zerolog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	httpClient    *http.Client
	serverReady   chan struct{}
	serverStarted bool
	mu            sync.RWMutex
}

type MountInfo struct {
	Provider   string `json:"provider"`
	LocalPath  string `json:"local_path"`
	WebDAVURL  string `json:"webdav_url"`
	Mounted    bool   `json:"mounted"`
	MountedAt  string `json:"mounted_at,omitempty"`
	ConfigName string `json:"config_name"`
	Error      string `json:"error,omitempty"`
}

type RCRequest struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args,omitempty"`
}

type RCResponse struct {
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

type CoreStatsResponse struct {
	TransferStats map[string]interface{} `json:"transferStats"`
	CoreStats     map[string]interface{} `json:"coreStats"`
}

// NewManager creates a new rclone RC manager
func NewManager() *Manager {
	cfg := config.Get()

	rcPort := "5572"
	configDir := filepath.Join(cfg.Path, "rclone")

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		_logger := logger.New("rclone")
		_logger.Error().Err(err).Msg("Failed to create rclone config directory")
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Manager{
		rcPort:      rcPort,
		configDir:   configDir,
		mounts:      make(map[string]*MountInfo),
		logger:      logger.New("rclone"),
		ctx:         ctx,
		cancel:      cancel,
		httpClient:  &http.Client{Timeout: 30 * time.Second},
		serverReady: make(chan struct{}),
	}
}

// Start starts the rclone RC server
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.serverStarted {
		return nil
	}

	cfg := config.Get()
	if !cfg.Rclone.Enabled {
		m.logger.Info().Msg("Rclone is disabled, skipping RC server startup")
		return nil
	}

	args := []string{
		"rcd",
		"--rc-addr", ":" + m.rcPort,
		"--rc-no-auth", // We'll handle auth at the application level
		"--config", filepath.Join(m.configDir, "rclone.conf"),
		"--log-level", "INFO",
	}
	m.cmd = exec.CommandContext(ctx, "rclone", args...)
	m.cmd.Dir = m.configDir

	// Capture output for debugging
	var stdout, stderr bytes.Buffer
	m.cmd.Stdout = &stdout
	m.cmd.Stderr = &stderr

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start rclone RC server: %w", err)
	}

	m.serverStarted = true

	// Wait for server to be ready in a goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error().Interface("panic", r).Msg("Panic in rclone RC server monitor")
			}
		}()

		m.waitForServer()
		close(m.serverReady)

		// Start mount monitoring once server is ready
		go func() {
			defer func() {
				if r := recover(); r != nil {
					m.logger.Error().Interface("panic", r).Msg("Panic in mount monitor")
				}
			}()
			m.MonitorMounts(ctx)
		}()

		// Wait for command to finish and log output
		err := m.cmd.Wait()
		switch {
		case err == nil:
			m.logger.Info().Msg("Rclone RC server exited normally")

		case errors.Is(err, context.Canceled):
			m.logger.Info().Msg("Rclone RC server terminated: context canceled")

		case WasHardTerminated(err): // SIGKILL on *nix; non-zero exit on Windows
			m.logger.Info().Msg("Rclone RC server hard-terminated")

		default:
			if code, ok := ExitCode(err); ok {
				m.logger.Debug().Int("exit_code", code).Err(err).
					Msg("Rclone RC server error")
			} else {
				m.logger.Debug().Err(err).Msg("Rclone RC server error (no exit code)")
			}
		}
	}()
	return nil
}

// Stop stops the rclone RC server and unmounts all mounts
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.serverStarted {
		return nil
	}

	m.logger.Info().Msg("Stopping rclone RC server")

	// Unmount all mounts first
	m.mountsMutex.RLock()
	mountList := make([]*MountInfo, 0, len(m.mounts))
	for _, mount := range m.mounts {
		if mount.Mounted {
			mountList = append(mountList, mount)
		}
	}
	m.mountsMutex.RUnlock()

	// Unmount in parallel
	var wg sync.WaitGroup
	for _, mount := range mountList {
		wg.Add(1)
		go func(mount *MountInfo) {
			defer wg.Done()
			if err := m.unmount(mount.Provider); err != nil {
				m.logger.Error().Err(err).Str("provider", mount.Provider).Msg("Failed to unmount during shutdown")
			}
		}(mount)
	}

	// Wait for unmounts with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		m.logger.Info().Msg("All mounts unmounted successfully")
	case <-time.After(30 * time.Second):
		m.logger.Warn().Msg("Timeout waiting for mounts to unmount, proceeding with shutdown")
	}

	// Cancel context and stop process
	m.cancel()

	if m.cmd != nil && m.cmd.Process != nil {
		// Try graceful shutdown first
		if err := m.cmd.Process.Signal(os.Interrupt); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to send interrupt signal, using kill")
			if killErr := m.cmd.Process.Kill(); killErr != nil {
				m.logger.Error().Err(killErr).Msg("Failed to kill rclone process")
				return killErr
			}
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- m.cmd.Wait()
		}()

		select {
		case err := <-done:
			if err != nil && !errors.Is(err, context.Canceled) && !WasHardTerminated(err) {
				m.logger.Warn().Err(err).Msg("Rclone process exited with error")
			}
		case <-time.After(10 * time.Second):
			m.logger.Warn().Msg("Timeout waiting for rclone to exit, force killing")
			if err := m.cmd.Process.Kill(); err != nil {
				m.logger.Error().Err(err).Msg("Failed to force kill rclone process")
				return err
			}
			// Wait a bit more for the kill to take effect
			select {
			case <-done:
				m.logger.Info().Msg("Rclone process killed successfully")
			case <-time.After(5 * time.Second):
				m.logger.Error().Msg("Process may still be running after kill")
			}
		}
	}

	// Clean up any remaining mount directories
	cfg := config.Get()
	if cfg.Rclone.MountPath != "" {
		m.cleanupMountDirectories(cfg.Rclone.MountPath)
	}

	m.serverStarted = false
	m.logger.Info().Msg("Rclone RC server stopped")
	return nil
}

// cleanupMountDirectories removes empty mount directories
func (m *Manager) cleanupMountDirectories(_ string) {
	m.mountsMutex.RLock()
	defer m.mountsMutex.RUnlock()

	for _, mount := range m.mounts {
		if mount.LocalPath != "" {
			// Try to remove the directory if it's empty
			if err := os.Remove(mount.LocalPath); err == nil {
				m.logger.Debug().Str("path", mount.LocalPath).Msg("Removed empty mount directory")
			}
			// Don't log errors here as the directory might not be empty, which is fine
		}
	}
}

// waitForServer waits for the RC server to become available
func (m *Manager) waitForServer() {
	maxAttempts := 30
	for i := 0; i < maxAttempts; i++ {
		if m.ctx.Err() != nil {
			return
		}

		if m.pingServer() {
			m.logger.Info().Msg("Rclone RC server is ready")
			return
		}

		time.Sleep(time.Second)
	}

	m.logger.Error().Msg("Rclone RC server not responding - mount operations will be disabled")
}

// pingServer checks if the RC server is responding
func (m *Manager) pingServer() bool {
	req := RCRequest{Command: "core/version"}
	_, err := m.makeRequest(req)
	return err == nil
}

// makeRequest makes a request to the rclone RC server
func (m *Manager) makeRequest(req RCRequest) (*RCResponse, error) {
	reqBody, err := json.Marshal(req.Args)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := fmt.Sprintf("http://localhost:%s/%s", m.rcPort, req.Command)
	httpReq, err := http.NewRequestWithContext(m.ctx, "POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := m.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			m.logger.Debug().Err(err).Msg("Failed to close response body")
		}
	}()

	var rcResp RCResponse
	if err := json.NewDecoder(resp.Body).Decode(&rcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	if rcResp.Error != "" {
		return nil, fmt.Errorf("rclone error: %s", rcResp.Error)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d - %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	return &rcResp, nil
}

// IsReady returns true if the RC server is ready
func (m *Manager) IsReady() bool {
	select {
	case <-m.serverReady:
		return true
	default:
		return false
	}
}

// WaitForReady waits for the RC server to be ready
func (m *Manager) WaitForReady(timeout time.Duration) error {
	select {
	case <-m.serverReady:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for rclone RC server to be ready")
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}

func (m *Manager) GetLogger() zerolog.Logger {
	return m.logger
}
