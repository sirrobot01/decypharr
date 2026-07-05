package rclone

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/rclone"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"gopkg.in/natefinch/lumberjack.v2"
)

const (
	FSName     = "decypharr:"
	ConfigName = "decypharr"
)

// Manager handles the rclone RC server and provides mount operations
type Manager struct {
	cmd           *exec.Cmd
	configDir     string
	logger        zerolog.Logger
	ctx           context.Context
	cancel        context.CancelFunc
	serverReady   chan struct{}
	serverStarted atomic.Bool
	info          atomic.Pointer[MountInfo]
	manager       *manager.Manager
	webdavURL     string

	client *rclone.Client
}

type MountInfo struct {
	LocalPath  string `json:"local_path"`
	WebDAVURL  string `json:"webdav_url"`
	Mounted    bool   `json:"mounted"`
	MountedAt  string `json:"mounted_at,omitempty"`
	ConfigName string `json:"config_name"`
	Error      string `json:"error,omitempty"`
}

type RCRequest struct {
	Command string         `json:"command"`
	Args    map[string]any `json:"args,omitempty"`
}

type RCResponse struct {
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewManager creates a new rclone RC manager
func NewManager(manager *manager.Manager) *Manager {

	mainCfg := config.Get()
	cfg := mainCfg.Mount
	configDir := filepath.Join(config.GetMainPath(), "rclone")
	_logger := logger.New("rclone")

	if mainCfg.DisableWebDav {
		_logger.Info().Msg("WebDAV support is disabled by configuration, can't use rclone with WebDAV features")
		return nil
	}

	// Ensure config directory exists
	if err := os.MkdirAll(configDir, 0755); err != nil {
		_logger.Error().Err(err).Msg("Failed to create rclone config directory")
	}

	bindAddress := mainCfg.BindAddress
	if bindAddress == "" {
		bindAddress = "localhost"
	}

	baseUrl := fmt.Sprintf("http://%s:%s", bindAddress, mainCfg.Port)
	webdavUrl, err := url.JoinPath(baseUrl, mainCfg.URLBase, "webdav")
	if err != nil {
		return nil
	}

	if !strings.HasSuffix(webdavUrl, "/") {
		webdavUrl += "/"
	}

	ctx, cancel := context.WithCancel(context.Background())
	rcServer := fmt.Sprintf("http://localhost:%s", cfg.Rclone.Port)
	rcloneClient := rclone.NewClient(rcServer, "", "", _logger)

	m := &Manager{
		configDir:   configDir,
		logger:      _logger,
		ctx:         ctx,
		cancel:      cancel,
		client:      rcloneClient,
		serverReady: make(chan struct{}),
		webdavURL:   webdavUrl,
		manager:     manager,
	}
	return m
}

// Start starts the rclone RC server
func (m *Manager) Start(ctx context.Context) error {
	cfg := config.Get().Mount
	if m.serverStarted.Load() {
		return nil
	}
	// Use lumberjack for log rotation instead of rclone's --log-file
	rotatingLog := &lumberjack.Logger{
		Filename:   filepath.Join(logger.GetLogPath(), "rclone.log"),
		MaxSize:    10, // 10 MB
		MaxAge:     15, // 15 days
		MaxBackups: 5,  // Keep max 5 backup files
		Compress:   true,
	}

	args := []string{
		"rcd",
		"--rc-addr", ":" + cfg.Rclone.Port,
		"--rc-no-auth", // We'll handle auth at the application level
		"--config", filepath.Join(config.GetMainPath(), "rclone", "rclone.conf"),
		// No --log-file, we capture output directly
	}

	logLevel := cfg.Rclone.LogLevel
	if logLevel != "" {
		if !slices.Contains([]string{"DEBUG", "INFO", "NOTICE", "ERROR"}, logLevel) {
			logLevel = "INFO"
		}
		args = append(args, "--log-level", logLevel)
	}

	if cfg.Rclone.CacheDir != "" {
		if err := os.MkdirAll(cfg.Rclone.CacheDir, 0755); err == nil {
			args = append(args, "--cache-dir", cfg.Rclone.CacheDir)
		}
	}
	m.cmd = exec.CommandContext(ctx, "rclone", args...)

	// Route rclone output through lumberjack for rotation
	m.cmd.Stdout = rotatingLog
	m.cmd.Stderr = rotatingLog

	if err := m.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start rclone: %w", err)
	}
	m.serverStarted.Store(true)

	// Wait for server to be ready in a goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error().Interface("panic", r).Msg("Panic in rclone RC server monitor")
			}
		}()

		m.waitForServer()
		close(m.serverReady)

		// Start mounting here now

		if err := m.waitForReady(30 * time.Second); err != nil {
			m.logger.Error().Err(err).Msg("Client RC server did not become ready in time")
			return
		}

		// Start mount
		if err := m.startMount(m.ctx); err != nil {
			m.logger.Error().Err(err).Msgf("Failed to mount rclone filesystem")
		} else {
			m.logger.Info().Msgf("Successfully mounted rclone filesystem")
		}

		// Wait for command to finish and log output
		err := m.cmd.Wait()
		switch {
		case err == nil:
			m.logger.Info().Msg("Client RC server exited normally")

		case errors.Is(err, context.Canceled):
			m.logger.Info().Msg("Client RC server terminated: context canceled")

		case WasHardTerminated(err): // SIGKILL on *nix; non-zero exit on Windows
			m.logger.Info().Msg("Client RC server hard-terminated")

		default:
		}
	}()
	return nil
}

// Stop stops the rclone RC server and unmounts all mounts
func (m *Manager) Stop() error {
	if !m.serverStarted.Load() {
		return nil
	}

	m.logger.Info().Msg("Stopping rclone RC server")
	// Cancel context and stop process
	m.cancel()

	// Stopping mount
	m.stopMount()

	if m.cmd != nil && m.cmd.Process != nil {
		// Try graceful shutdown first
		if err := m.cmd.Process.Signal(os.Interrupt); err != nil {
			if killErr := m.cmd.Process.Kill(); killErr != nil {
				return killErr
			}
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- m.cmd.Wait()
		}()

		<-time.After(2 * time.Second)
		if err := m.cmd.Process.Kill(); err != nil {
			// Check if the process already finished
			if !strings.Contains(err.Error(), "process already finished") {
				return err
			}
		}

		// Still wait for the Wait() to complete to clean up the process
		select {
		case <-done:
			m.logger.Info().Msg("Client process cleanup completed")
		case <-time.After(5 * time.Second):
			m.logger.Error().Msg("Parse cleanup timeout")
		}
	}

	m.serverStarted.Store(false)
	m.logger.Info().Msg("Client RC server stopped")
	return nil
}

func (m *Manager) getMountInfo() *MountInfo {
	return m.info.Load()
}

func (m *Manager) IsMounted() bool {
	info := m.getMountInfo()
	return info != nil && info.Mounted
}

// Start creates the mount using rclone RC
func (m *Manager) startMount(ctx context.Context) error {
	// Check if already mounted
	if m.IsMounted() {
		m.logger.Info().Msg("Mount is already mounted")
		return nil
	}

	// Try to ping rcd
	if err := m.client.Ping(ctx); err != nil {
		return fmt.Errorf("rclone RC server is not reachable: %w", err)
	}

	if err := m.mountWithRetry(ctx, 3); err != nil {
		m.logger.Error().Err(err).Msg("Mount operation failed")
		return err
	}
	go m.MonitorMounts(ctx)
	return nil
}

func (m *Manager) stopMount() {
	if !m.IsMounted() {
		m.logger.Info().Msgf("Mount is not mounted, skipping unmount")
		return
	}

	m.logger.Info().Msg("Unmounting via RC")

	m.unmount(m.ctx)
	m.logger.Info().Msgf("Successfully unmounted %s", m.getMountInfo().LocalPath)
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

// Refresh refreshes directories in the VFS cache
func (m *Manager) Refresh(dirs []string) error {
	mountInfo := m.getMountInfo()
	if mountInfo == nil || !mountInfo.Mounted {
		return fmt.Errorf("mount is not mounted")
	}

	if err := m.client.Refresh(context.Background(), dirs, FSName); err != nil {
		m.logger.Error().Err(err).
			Msg("Failed to refresh directory")
		return fmt.Errorf("failed to refresh directory %s : %w", dirs, err)
	}
	return nil
}

func (m *Manager) GetLogger() zerolog.Logger {
	return m.logger
}

func (m *Manager) Type() string {
	return "rclone"
}

// waitForServer waits for the RC server to become available
func (m *Manager) waitForServer() {
	maxAttempts := 30
	for range maxAttempts {
		if m.ctx.Err() != nil {
			return
		}

		if err := m.client.Ping(m.ctx); err == nil {
			return
		}

		time.Sleep(time.Second)
	}

	m.logger.Error().Msg("Client RC server not responding - mount operations will be disabled")
}

// waitForReady waits for the RC server to be ready
func (m *Manager) waitForReady(timeout time.Duration) error {
	select {
	case <-m.serverReady:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout waiting for rclone RC server to be ready")
	case <-m.ctx.Done():
		return m.ctx.Err()
	}
}
