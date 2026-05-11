package nntp

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/retry"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// ProviderPool manages connections for a single provider using a LIFO stack
type ProviderPool struct {
	conns       []*connectionEntry // Stack: Push/Pop from end
	mu          sync.Mutex         // Protects conns slice only
	slots       chan struct{}      // Semaphore: capacity = max connections
	max         int
	config      config.UsenetProvider
	activeConns sync.Map // *Connection → struct{}; tracks checked-out connections for force-close on shutdown
}

// Client manages a pool of NNTP connections.
type Client struct {
	pools     map[string]*ProviderPool // Map Key: Provider Host
	providers []config.UsenetProvider
	logger    zerolog.Logger

	retries int // Number of retries per provider for transient errors

	closed atomic.Bool
	// Speed test results storage
	speedTestResults *xsync.Map[string, SpeedTestResult]

	// repairBank gates repair-mode availability checks so they can't starve
	// streaming. Sized at construction from cfg.Repair.NNTPConnectionPercent;
	// nil when repair has no NNTP budget.
	repairBank *RepairBank
}

// SpeedTestResult holds the result of a provider speed test
type SpeedTestResult struct {
	Provider  string    `json:"provider"`
	SpeedMBps float64   `json:"speed_mbps"`
	LatencyMs int64     `json:"latency_ms"`
	BytesRead int64     `json:"bytes_read"`
	TestedAt  time.Time `json:"tested_at"`
	Error     string    `json:"error,omitempty"`
}

// connectionEntry tracks a connection and its provider
type connectionEntry struct {
	conn     *Connection
	provider config.UsenetProvider
	lastUsed time.Time
}

// TimeoutConfig holds all NNTP timeout settings in one place.
// This provides a single location to tune timeout behavior.
type TimeoutConfig struct {
	// Connection establishment timeout
	DialTimeout time.Duration
	// TCP keepalive interval
	KeepAlive time.Duration
	// Auth/handshake deadline after connection
	HandshakeTimeout time.Duration
	// Read deadline for streaming segment data
	StreamBodyTimeout time.Duration
	// Deadline for lightweight health checks (DATE)
	PingTimeout time.Duration
	// Health check connections idle longer than this
	StaleThreshold time.Duration
	// Close connections idle longer than this
	IdleTimeout time.Duration
	// How often to check for idle connections
	ReaperInterval time.Duration
}

// DefaultTimeouts returns production-tuned timeout values.
// These are aggressive to prevent "connection reset by peer" errors
// from long-idle connections.
var DefaultTimeouts = TimeoutConfig{
	DialTimeout:       10 * time.Second,
	KeepAlive:         30 * time.Second,
	HandshakeTimeout:  10 * time.Second,
	StreamBodyTimeout: 60 * time.Second,
	PingTimeout:       1500 * time.Millisecond,
	StaleThreshold:    10 * time.Second,
	IdleTimeout:       20 * time.Second,
	ReaperInterval:    5 * time.Second,
}

// Package-level timeouts used by all clients
var timeouts = normalizeTimeouts(DefaultTimeouts)

func normalizeTimeouts(in TimeoutConfig) TimeoutConfig {
	if in.DialTimeout <= 0 {
		in.DialTimeout = 10 * time.Second
	}
	if in.KeepAlive <= 0 {
		in.KeepAlive = 30 * time.Second
	}
	if in.HandshakeTimeout <= 0 {
		in.HandshakeTimeout = 10 * time.Second
	}
	if in.StreamBodyTimeout <= 0 {
		in.StreamBodyTimeout = 60 * time.Second
	}
	if in.PingTimeout <= 0 {
		in.PingTimeout = 1500 * time.Millisecond
	}
	if in.IdleTimeout <= 0 {
		in.IdleTimeout = 20 * time.Second
	}
	// Keep stale checks meaningful: stale must be >0 and below idle timeout.
	if in.StaleThreshold <= 0 || in.StaleThreshold >= in.IdleTimeout {
		in.StaleThreshold = in.IdleTimeout / 2
		if in.StaleThreshold <= 0 {
			in.StaleThreshold = 10 * time.Second
		}
	}
	if in.ReaperInterval <= 0 {
		in.ReaperInterval = 5 * time.Second
	}
	// Sweep frequently enough to avoid long idle overhang.
	maxReaperInterval := in.IdleTimeout / 4
	if maxReaperInterval < time.Second {
		maxReaperInterval = time.Second
	}
	if in.ReaperInterval > maxReaperInterval {
		in.ReaperInterval = maxReaperInterval
	}
	return in
}

// NewClient creates a new connection manager
func NewClient(cfg *config.Config) (*Client, error) {
	providers := cfg.Usenet.Providers
	if len(providers) == 0 {
		return nil, errors.New("no NNTP providers configured")
	}

	// Sort providers by priority (lower number = higher priority)
	sort.Slice(providers, func(i, j int) bool {
		return providers[i].Priority < providers[j].Priority
	})

	pools := make(map[string]*ProviderPool)
	for _, p := range providers {
		pp := &ProviderPool{
			conns:  make([]*connectionEntry, 0, p.MaxConnections),
			slots:  make(chan struct{}, p.MaxConnections),
			max:    p.MaxConnections,
			config: p,
		}
		pools[p.Host] = pp
	}

	cm := &Client{
		pools:            pools,
		providers:        providers,
		retries:          cfg.Retries,
		logger:           logger.New("nntp-client"),
		speedTestResults: xsync.NewMap[string, SpeedTestResult](),
	}
	cm.repairBank = cm.newRepairBank(cfg.Repair.NNTPConnectionPercent)

	// Start background reaper
	go cm.reaper()
	return cm, nil
}

// put returns a connection to the pool and releases the slot.
func (c *Client) put(conn *Connection, provider config.UsenetProvider) {
	if conn == nil {
		return
	}

	pp, ok := c.pools[provider.Host]
	if !ok {
		_ = conn.Close()
		return
	}

	pp.activeConns.Delete(conn) // Deregister from active tracking

	// Don't return closed connections to pool
	if conn.IsClosed() {
		_ = conn.Close()
		<-pp.slots // Release slot
		return
	}

	if c.closed.Load() {
		_ = conn.Close()
		<-pp.slots // Release slot
		return
	}

	entry := &connectionEntry{
		conn:     conn,
		provider: provider,
		lastUsed: utils.Now(),
	}

	pp.mu.Lock()
	// Cap stack size (shouldn't happen with semaphore, but be safe)
	if len(pp.conns) >= pp.max {
		pp.mu.Unlock()
		_ = conn.Close()
		<-pp.slots // Release slot
		return
	}
	pp.conns = append(pp.conns, entry) // Push to stack
	pp.mu.Unlock()

	<-pp.slots // Release slot - connection is now available for reuse
}

// release closes a connection without returning it (for error cases)
func (c *Client) release(conn *Connection) {
	if conn != nil {
		_ = conn.Close()
		if pp, ok := c.pools[conn.address]; ok {
			pp.activeConns.Delete(conn) // Deregister from active tracking
			<-pp.slots                  // Release slot
		}
	}
}

// isHealthy checks if a connection entry is still usable
func (c *Client) isHealthy(entry *connectionEntry) bool {
	if entry == nil || entry.conn == nil {
		return false
	}
	// Check if explicitly closed
	if entry.conn.IsClosed() {
		return false
	}
	// Check if already closed/expired (though normally caught by reaper)
	// Or check stale threshold
	if time.Since(entry.lastUsed) > timeouts.StaleThreshold {
		if err := entry.conn.ping(); err != nil {
			return false
		}
	}
	return true
}

func isIdleExpired(lastUsed time.Time, now time.Time) bool {
	if lastUsed.IsZero() {
		return false
	}
	return now.Sub(lastUsed) > timeouts.IdleTimeout
}

// ExecuteWithFailover executes an operation with automatic provider failover and retry logic.
// Uses exclusion-based connection acquisition: gets ANY available connection,
// and on retryable errors, retries with exponential backoff before excluding the provider.
// Uses avast/retry-go for retry handling.
func (c *Client) ExecuteWithFailover(ctx context.Context, fn func(conn *Connection) error) error {
	var lastErr error
	excludedProviders := make(map[string]bool)

	for providerAttempts := 0; providerAttempts < len(c.providers); providerAttempts++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		conn, connProvider, err := c.getAnyAvailableConnection(ctx, excludedProviders)
		if err != nil {
			lastErr = err
			continue
		}

		// Use retry-go for retry logic with exponential backoff.
		// currentProvider tracks which pool currentConn actually belongs to so
		// returnOrReleaseConn always releases the right semaphore slot.
		var currentConn = conn
		var currentProvider = connProvider
		err = retry.Do(
			func() error {
				execErr := c.safeExecute(currentConn, fn)
				if execErr == nil {
					return nil
				}

				var nntpErr *Error
				if errors.As(execErr, &nntpErr) {
					switch nntpErr.Type {
					case ErrorTypeConnection, ErrorTypeTimeout, ErrorTypeServerBusy:
						// Retriable error - release the potentially dead connection.
						// Nil currentConn first to prevent double slot-release if new connection acquisition fails.
						releasedConn := currentConn
						currentConn = nil
						c.release(releasedConn)
						// Get a fresh connection for retry
						newConn, newProvider, connErr := c.getAnyAvailableConnection(ctx, map[string]bool{})
						if connErr != nil {
							return retry.Unrecoverable(connErr)
						}
						// Prefer same provider for retry if possible.
						// If we landed on a different provider, try to swap for the original.
						// On failure, keep the connection we already have (best-effort).
						if newConn.address != connProvider.Host {
							if preferredConn, _, preferredErr := c.getConnectionFromProvider(ctx, connProvider); preferredErr == nil {
								// Return the mismatched connection to its own pool, not connProvider's.
								c.returnOrReleaseConn(newConn, newProvider)
								newConn = preferredConn
								newProvider = connProvider
							}
							// else: fall back to the connection from whichever provider had capacity
						}
						currentConn = newConn
						currentProvider = newProvider
						return execErr // Retriable

					case ErrorTypeArticleNotFound:
						// Article not found - not retriable, try next provider
						return retry.Unrecoverable(execErr)

					default:
						// Non-retriable error
						return retry.Unrecoverable(execErr)
					}
				} else if customerror.IsPanicError(execErr) {
					// Panic error - release connection.
					// Nil currentConn first to prevent double slot-release after retry loop.
					releasedConn := currentConn
					currentConn = nil
					c.release(releasedConn)
					return retry.Unrecoverable(execErr)
				}
				// Unknown error type - don't retry
				return retry.Unrecoverable(execErr)
			},
			retry.Context(ctx),
			retry.Attempts(uint(c.retries)+1),
			retry.Delay(config.DefaultRetryDelay),
			retry.MaxDelay(config.DefaultRetryDelayMax),
			retry.DelayType(retry.BackOffDelay),
			retry.LastErrorOnly(true),
		)

		// Success
		if err == nil {
			c.returnOrReleaseConn(currentConn, currentProvider)
			return nil
		}

		// Handle failure
		c.returnOrReleaseConn(currentConn, currentProvider)
		lastErr = err

		// Check if we should exclude this provider
		var nntpErr *Error
		if errors.As(err, &nntpErr) {
			if nntpErr.Type == ErrorTypeArticleNotFound ||
				nntpErr.Type == ErrorTypeConnection ||
				nntpErr.Type == ErrorTypeTimeout ||
				nntpErr.Type == ErrorTypeServerBusy {
				excludedProviders[connProvider.Host] = true
			} else {
				// Non-retriable error, return immediately
				return err
			}
		} else if customerror.IsPanicError(err) {
			excludedProviders[connProvider.Host] = true
		} else {
			// Unknown error type - return immediately
			return err
		}
	}

	if lastErr != nil {
		return lastErr
	}
	return errors.New("all providers failed")
}

// returnOrReleaseConn returns a connection to the pool or releases it if closed
func (c *Client) returnOrReleaseConn(conn *Connection, provider config.UsenetProvider) {
	if conn == nil {
		return
	}
	if conn.IsClosed() {
		c.release(conn)
	} else {
		c.put(conn, provider)
	}
}

// getConnectionFromProvider tries to get a connection from a specific provider
func (c *Client) getConnectionFromProvider(ctx context.Context, provider config.UsenetProvider) (*Connection, config.UsenetProvider, error) {
	pp, ok := c.pools[provider.Host]
	if !ok {
		return nil, provider, fmt.Errorf("provider pool not found: %s", provider.Host)
	}

	select {
	case pp.slots <- struct{}{}:
		conn, err := c.getOrCreateFromPool(ctx, pp, provider)
		if err != nil {
			<-pp.slots
			return nil, provider, err
		}
		return conn, provider, nil
	case <-ctx.Done():
		return nil, provider, ctx.Err()
	}
}

// safeExecute wraps fn execution with panic recovery
func (c *Client) safeExecute(conn *Connection, fn func(conn *Connection) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error().
				Interface("panic", r).
				Str("host", conn.address).
				Msg("Recovered from panic in NNTP operation")
			err = customerror.NewPanicError(r)
		}
	}()
	return fn(conn)
}

// getAnyAvailableConnection gets a connection from ANY provider that isn't excluded.
// Phase 1: Non-blocking scan of all eligible providers (fast path)
// Phase 2: If all busy, race goroutines to get first available slot
func (c *Client) getAnyAvailableConnection(ctx context.Context, excludedProviders map[string]bool) (*Connection, config.UsenetProvider, error) {
	// Build list of eligible providers
	var eligible []config.UsenetProvider
	for _, p := range c.providers {
		if !excludedProviders[p.Host] {
			eligible = append(eligible, p)
		}
	}

	if len(eligible) == 0 {
		return nil, config.UsenetProvider{}, errors.New("no eligible providers available")
	}

	// Phase 1: Non-blocking scan - try to get a free slot from any provider
	for _, provider := range eligible {
		pp := c.pools[provider.Host]

		select {
		case pp.slots <- struct{}{}:
			// Got a slot - try to get or create connection
			conn, err := c.getOrCreateFromPool(ctx, pp, provider)
			if err != nil {
				<-pp.slots // Release slot on error
				continue   // Try next provider
			}
			return conn, provider, nil
		default:
			// Pool at capacity, try next provider
			continue
		}
	}

	// Phase 2: All providers busy - race for first available
	return c.raceForConnection(ctx, eligible)
}

// raceForConnection spawns goroutines that race to acquire a connection slot.
// Returns as soon as any provider has availability.
//
// Each goroutine reports exactly one result (success or error) via resultCh, or
// exits silently if it never acquired a slot. A WaitGroup + channel-close ensures
// the receiver loop always terminates, and any extra connections won by multiple
// goroutines are properly returned to the pool — preventing slot leaks under heavy
// concurrent import load.
func (c *Client) raceForConnection(ctx context.Context, eligible []config.UsenetProvider) (*Connection, config.UsenetProvider, error) {
	type result struct {
		conn     *Connection
		provider config.UsenetProvider
		err      error
	}

	innerCtx, cancel := context.WithCancel(ctx)

	// Buffer for all possible results — goroutines that win the slot race send here.
	resultCh := make(chan result, len(eligible))
	var wg sync.WaitGroup

	for _, provider := range eligible {
		wg.Add(1)
		go func(p config.UsenetProvider) {
			defer wg.Done()
			pp := c.pools[p.Host]

			// Block waiting for slot (respects context)
			select {
			case pp.slots <- struct{}{}:
				// Got slot
			case <-innerCtx.Done():
				return // Context cancelled before we got a slot — no send needed
			}

			// Check if context was cancelled while we were waiting
			if innerCtx.Err() != nil {
				<-pp.slots // Release slot
				return
			}

			// Try to get or create connection
			conn, err := c.getOrCreateFromPool(innerCtx, pp, p)
			if err != nil {
				<-pp.slots // Release slot on error
				select {
				case resultCh <- result{nil, p, err}:
				case <-innerCtx.Done():
				}
				return
			}

			// Send the result; if the inner context was already cancelled (another
			// goroutine won), return our connection to the pool immediately.
			select {
			case resultCh <- result{conn, p, nil}:
				// Slot is still held — the receiver will call returnOrReleaseConn.
			case <-innerCtx.Done():
				c.put(conn, p) // releases slot
			}
		}(provider)
	}

	// Close resultCh once all goroutines have finished so the receiver loop below
	// can terminate without needing an explicit count.
	go func() {
		wg.Wait()
		close(resultCh)
		cancel()
	}()

	// Drain all results:
	//  - First success becomes the winner; cancel() is called to stop remaining goroutines.
	//  - Any subsequent successes (from goroutines that raced to completion before cancel
	//    reached them) have their connections returned to the pool to prevent slot leaks.
	//  - Errors are collected so we can return a meaningful error if there is no winner.
	var winConn *Connection
	var winProvider config.UsenetProvider
	var lastErr error

	for {
		select {
		case r, ok := <-resultCh:
			if !ok {
				// Channel closed — all goroutines have finished.
				if winConn != nil {
					return winConn, winProvider, nil
				}
				if lastErr != nil {
					return nil, config.UsenetProvider{}, lastErr
				}
				return nil, config.UsenetProvider{}, errors.New("failed to get connection from any provider")
			}
			if r.err == nil && r.conn != nil {
				if winConn == nil {
					winConn = r.conn
					winProvider = r.provider
					cancel() // Tell losing goroutines to stop ASAP.
				} else {
					// Extra winner arrived before cancel propagated — release it.
					c.returnOrReleaseConn(r.conn, r.provider)
				}
			} else if r.err != nil {
				lastErr = r.err
			}
		case <-ctx.Done():
			// Parent context cancelled — cancel inner, drain remaining connections
			// in background so we don't block the caller.
			cancel()
			go func() {
				for r := range resultCh {
					if r.conn != nil {
						c.returnOrReleaseConn(r.conn, r.provider)
					}
				}
			}()
			return nil, config.UsenetProvider{}, ctx.Err()
		}
	}
}

// getOrCreateFromPool tries to get an existing connection from pool, or creates a new one.
// Caller must have already acquired a slot from pp.slots.
func (c *Client) getOrCreateFromPool(ctx context.Context, pp *ProviderPool, provider config.UsenetProvider) (*Connection, error) {
	// Try to get existing connection from pool (quick lock)
	for {
		pp.mu.Lock()
		if len(pp.conns) > 0 {
			// Pop from end (LIFO)
			n := len(pp.conns)
			entry := pp.conns[n-1]
			pp.conns[n-1] = nil // Avoid memory leak
			pp.conns = pp.conns[:n-1]
			pp.mu.Unlock()

			now := utils.Now()
			if isIdleExpired(entry.lastUsed, now) {
				_ = entry.conn.Close()
				continue
			}

			// Health check outside lock
			if c.isHealthy(entry) {
				pp.activeConns.Store(entry.conn, struct{}{}) // Register as active (checked-out)
				return entry.conn, nil
			}
			// Unhealthy - close and try next pooled connection
			_ = entry.conn.Close()
			continue
		}
		pp.mu.Unlock()
		break
	}

	// No pooled connection available, create new one
	conn, err := c.createConnection(ctx, provider)
	if err != nil {
		return nil, err
	}
	pp.activeConns.Store(conn, struct{}{}) // Register as active (checked-out)
	return conn, nil
}

// createConnection creates a new NNTP connection to a provider
func (c *Client) createConnection(ctx context.Context, provider config.UsenetProvider) (*Connection, error) {
	address := fmt.Sprintf("%s:%d", provider.Host, provider.Port)

	var netConn net.Conn
	var err error

	dialer := &net.Dialer{
		Timeout:   timeouts.DialTimeout,
		KeepAlive: timeouts.KeepAlive,
	}

	// TLS if enabled
	if provider.SSL {
		// Dial with TLS directly if possible, or Dial then Wrap
		tlsConfig := &tls.Config{
			ServerName:         provider.Host,
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		}
		// Use tls.Dialer for simpler timeout handling
		tlsDialer := &tls.Dialer{
			NetDialer: dialer,
			Config:    tlsConfig,
		}
		netConn, err = tlsDialer.DialContext(ctx, "tcp", address)
	} else {
		netConn, err = dialer.DialContext(ctx, "tcp", address)
	}

	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("dial %s: %w", address, err))
	}

	// Optimize TCP socket
	if tcpConn, ok := netConn.(*net.TCPConn); ok {
		_ = tcpConn.SetNoDelay(true)
		_ = tcpConn.SetReadBuffer(1024 * 1024)
		_ = tcpConn.SetWriteBuffer(256 * 1024)
	}
	// For TLS, get underlying conn
	if tlsConn, ok := netConn.(*tls.Conn); ok {
		if tcpConn, ok := tlsConn.NetConn().(*net.TCPConn); ok {
			_ = tcpConn.SetNoDelay(true)
			_ = tcpConn.SetReadBuffer(1024 * 1024)
			_ = tcpConn.SetWriteBuffer(256 * 1024)
		}
	}

	reader := bufio.NewReaderSize(netConn, 512*1024)
	writer := bufio.NewWriterSize(netConn, 64*1024)

	conn := &Connection{
		conn:     netConn,
		reader:   reader,
		text:     textproto.NewReader(reader),
		writer:   writer,
		address:  provider.Host,
		port:     provider.Port,
		username: provider.Username,
		password: provider.Password,
		logger:   c.logger.With().Str("host", provider.Host).Logger(),
	}

	// Set deadline for handshake (greeting + auth)
	// If the server doesn't respond quickly during setup, we should abort.
	_ = netConn.SetDeadline(utils.Now().Add(timeouts.HandshakeTimeout))

	// Read greeting
	line, err := reader.ReadString('\n')
	if err != nil {
		_ = netConn.Close()
		return nil, NewConnectionError(fmt.Errorf("read greeting: %w", err))
	}
	if !strings.HasPrefix(line, "200") && !strings.HasPrefix(line, "201") {
		_ = netConn.Close()
		return nil, NewConnectionError(fmt.Errorf("unexpected greeting: %s", line))
	}

	// Authenticate
	if provider.Username != "" {
		if err := conn.authenticate(); err != nil {
			_ = netConn.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}

	// Clear deadline for normal operation
	_ = netConn.SetDeadline(time.Time{})

	return conn, nil
}

// reaper periodically closes idle connections
func (c *Client) reaper() {
	ticker := time.NewTicker(timeouts.ReaperInterval)
	defer ticker.Stop()

	for range ticker.C {
		if c.closed.Load() {
			return
		}
		c.reapIdleConnections()
	}
}

func (c *Client) reapIdleConnections() {
	now := utils.Now()
	for _, pp := range c.pools {
		pp.mu.Lock()

		// LIFO Stack: Oldest (least recently used) items are at index 0.
		// Find first non-expired connection; all after it are newer and valid.
		expiredCount := 0
		for _, entry := range pp.conns {
			if isIdleExpired(entry.lastUsed, now) {
				_ = entry.conn.Close()
				expiredCount++
			} else {
				// Found a valid one - stop here
				break
			}
		}

		// Remove expired connections from the front of the slice
		if expiredCount > 0 {
			// Shift remaining items to front
			remaining := len(pp.conns) - expiredCount
			copy(pp.conns, pp.conns[expiredCount:])
			// Nil out trailing pointers to help GC
			for i := remaining; i < len(pp.conns); i++ {
				pp.conns[i] = nil
			}
			pp.conns = pp.conns[:remaining]
		}

		pp.mu.Unlock()
	}
}

// Stats returns current pool statistics
func (c *Client) Stats() map[string]interface{} {
	if c.closed.Load() {
		return nil
	}

	stats := make(map[string]interface{})
	providers := make([]map[string]interface{}, 0, len(c.providers))

	totalActive := 0
	totalIdle := 0
	totalMax := 0

	for _, p := range c.providers {
		pp, ok := c.pools[p.Host]
		if !ok {
			continue
		}

		pp.mu.Lock()
		idle := len(pp.conns)
		pp.mu.Unlock()

		// Active = slots in use (tokens in the semaphore channel)
		active := len(pp.slots)
		maxC := pp.max

		totalActive += active
		totalIdle += idle
		totalMax += maxC

		providerInfo := map[string]interface{}{
			"host":            p.Host,
			"port":            p.Port,
			"max_connections": maxC,
			"active":          active,
			"idle":            idle,
			"ssl":             p.SSL,
		}

		// Add speed test result if available
		if result, ok := c.speedTestResults.Load(p.Host); ok {
			providerInfo["speed_test"] = map[string]interface{}{
				"latency_ms": result.LatencyMs,
				"speed_mbps": result.SpeedMBps,
				"bytes_read": result.BytesRead,
				"tested_at":  result.TestedAt.Format("2006-01-02T15:04:05Z07:00"),
				"error":      result.Error,
			}
		}

		providers = append(providers, providerInfo)
	}

	poolStats := map[string]interface{}{
		"max_connections": totalMax,
		"total_created":   totalActive + totalIdle,
		"active":          totalActive,
		"idle":            totalIdle,
	}

	stats["pool"] = poolStats
	stats["providers"] = providers

	return stats
}

func (c *Client) Stat(ctx context.Context, messageID string) (int, string, error) {
	if c.closed.Load() {
		return 0, "", errors.New("nntp client is closed")
	}

	var num int
	var id string
	err := c.ExecuteWithFailover(ctx, func(conn *Connection) error {
		n, echoed, statErr := conn.Stat(messageID)
		if statErr != nil {
			return statErr
		}
		num = n
		id = echoed
		return nil
	})
	return num, id, err
}

// chunkResult holds the results from a single chunk of PipelinedStat
type chunkResult struct {
	startIdx int          // Index into original messageIDs slice
	results  []StatResult // Per-segment results for this chunk
	connErr  error        // Connection-level error (if any)
}

type batchStatState struct {
	sawNotFound bool
	sawOtherErr bool
	lastErr     error
}

// BatchStat checks the availability of many message IDs using NNTP STAT,
// pipelined across multiple connections. Each worker holds one repair-bank
// token for its lifetime, so the total number of concurrent NNTP connections
// used by all in-flight BatchStat calls never exceeds the bank's capacity.
// When the client has no bank configured, a small default worker count is
// used. Does NOT fail-fast: every chunk is processed so the caller sees
// complete per-segment visibility.
func (c *Client) BatchStat(ctx context.Context, messageIDs []string) (*BatchStatResult, error) {
	if c.closed.Load() {
		return nil, errors.New("nntp client is closed")
	}
	if len(messageIDs) == 0 {
		return &BatchStatResult{}, nil
	}

	// Pipeline depth per STAT round-trip. Balances RTT amortization against
	// memory, cancellation latency, and connection-drop blast radius.
	const pipelineBatchSize = 50
	// Worker count when no bank is configured (no repair budget). Keeps
	// non-repair callers cheap.
	const defaultWorkers = 2

	type chunk struct {
		startIdx   int
		messageIDs []string
	}
	chunks := make([]chunk, 0, (len(messageIDs)+pipelineBatchSize-1)/pipelineBatchSize)
	for i := 0; i < len(messageIDs); i += pipelineBatchSize {
		end := min(i+pipelineBatchSize, len(messageIDs))
		chunks = append(chunks, chunk{startIdx: i, messageIDs: messageIDs[i:end]})
	}

	workers := defaultWorkers
	if c.repairBank != nil {
		workers = c.repairBank.Capacity()
	}
	workers = min(workers, len(chunks))
	if workers < 1 {
		workers = 1
	}

	chunksCh := make(chan chunk, len(chunks))
	for _, ch := range chunks {
		chunksCh <- ch
	}
	close(chunksCh)

	resultsChan := make(chan chunkResult, len(chunks))
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Hold one bank token for this worker's lifetime. With many
			// in-flight callers, late workers block here until a token frees
			// up — capping total concurrent connections at bank.Capacity().
			release, err := c.repairBank.acquire(ctx)
			if err != nil {
				for ch := range chunksCh {
					resultsChan <- chunkResult{startIdx: ch.startIdx, connErr: err}
				}
				return
			}
			defer release()

			for ch := range chunksCh {
				if ctx.Err() != nil {
					resultsChan <- chunkResult{startIdx: ch.startIdx, connErr: ctx.Err()}
					continue
				}
				chunkResults, err := c.batchStatAcrossProviders(ctx, ch.messageIDs)
				resultsChan <- chunkResult{
					startIdx: ch.startIdx,
					results:  chunkResults,
					connErr:  err,
				}
			}
		}()
	}

	go func() {
		wg.Wait()
		close(resultsChan)
	}()

	allResults := make([]StatResult, len(messageIDs))
	for i, msgID := range messageIDs {
		allResults[i].MessageID = msgID
	}

	for cr := range resultsChan {
		if cr.connErr != nil {
			// Mark every message in this chunk as errored.
			endIdx := cr.startIdx + pipelineBatchSize
			if endIdx > len(messageIDs) {
				endIdx = len(messageIDs)
			}
			for i := cr.startIdx; i < endIdx; i++ {
				allResults[i].Available = false
				allResults[i].Error = cr.connErr
			}
			continue
		}
		for i, res := range cr.results {
			idx := cr.startIdx + i
			if idx < len(allResults) {
				allResults[idx] = res
			}
		}
	}

	result := &BatchStatResult{
		Results:    allResults,
		TotalCount: len(messageIDs),
	}
	for _, r := range allResults {
		if r.Available {
			result.FoundCount++
			continue
		}
		if r.Error == nil {
			continue
		}
		// Article-not-found doesn't count as an error for the caller's
		// availability decision; only true connection/protocol failures do.
		var nntpErr *Error
		if errors.As(r.Error, &nntpErr) && nntpErr.Type == ErrorTypeArticleNotFound {
			continue
		}
		result.ErrorCount++
	}
	return result, nil
}

func (c *Client) batchStatAcrossProviders(ctx context.Context, messageIDs []string) ([]StatResult, error) {
	results := make([]StatResult, len(messageIDs))
	states := make([]batchStatState, len(messageIDs))
	unresolved := make([]int, len(messageIDs))
	for i, msgID := range messageIDs {
		results[i].MessageID = msgID
		unresolved[i] = i
	}

	for _, provider := range c.providers {
		if len(unresolved) == 0 {
			break
		}
		if ctx.Err() != nil {
			return results, ctx.Err()
		}

		chunkIDs := make([]string, len(unresolved))
		for i, idx := range unresolved {
			chunkIDs[i] = messageIDs[idx]
		}

		providerResults, err := c.batchStatOnProvider(ctx, provider, chunkIDs)
		if err != nil && len(providerResults) == 0 {
			for _, idx := range unresolved {
				states[idx].sawOtherErr = true
				states[idx].lastErr = err
			}
			continue
		}

		nextUnresolved := make([]int, 0, len(unresolved))
		for i, idx := range unresolved {
			if i >= len(providerResults) {
				states[idx].sawOtherErr = true
				if err != nil {
					states[idx].lastErr = err
				} else {
					states[idx].lastErr = NewConnectionError(fmt.Errorf("provider %s returned incomplete batch results", provider.Host))
				}
				nextUnresolved = append(nextUnresolved, idx)
				continue
			}

			res := providerResults[i]
			if res.Available {
				results[idx] = res
				continue
			}

			var nntpErr *Error
			if res.Error != nil && errors.As(res.Error, &nntpErr) && nntpErr.Type == ErrorTypeArticleNotFound {
				states[idx].sawNotFound = true
			} else {
				states[idx].sawOtherErr = true
				if res.Error != nil {
					states[idx].lastErr = res.Error
				} else if err != nil {
					states[idx].lastErr = err
				} else {
					states[idx].lastErr = NewConnectionError(fmt.Errorf("provider %s returned an empty STAT result for %s", provider.Host, res.MessageID))
				}
			}
			nextUnresolved = append(nextUnresolved, idx)
		}
		unresolved = nextUnresolved
	}

	for _, idx := range unresolved {
		switch {
		case states[idx].sawNotFound && !states[idx].sawOtherErr:
			results[idx].Available = false
			results[idx].Error = classifyNNTPError(430, fmt.Sprintf("segment %s not found on any provider", results[idx].MessageID))
		case states[idx].lastErr != nil:
			results[idx].Available = false
			results[idx].Error = states[idx].lastErr
		case states[idx].sawNotFound:
			results[idx].Available = false
			results[idx].Error = NewConnectionError(fmt.Errorf("segment %s not found on some providers but could not be verified on others", results[idx].MessageID))
		default:
			results[idx].Available = false
			results[idx].Error = NewConnectionError(fmt.Errorf("segment %s could not be verified on any provider", results[idx].MessageID))
		}
	}

	return results, nil
}

func (c *Client) batchStatOnProvider(ctx context.Context, provider config.UsenetProvider, messageIDs []string) ([]StatResult, error) {
	conn, providerCfg, err := c.getConnectionFromProvider(ctx, provider)
	if err != nil {
		return nil, err
	}

	results, statErr := conn.PipelinedStat(messageIDs)
	if statErr != nil {
		c.release(conn)
		return results, statErr
	}

	c.returnOrReleaseConn(conn, providerCfg)
	return results, nil
}

// Close shuts down the connection manager
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}

	var totalClosed int
	for _, pp := range c.pools {
		pp.mu.Lock()
		// Close idle connections
		for _, entry := range pp.conns {
			_ = entry.conn.Close()
			totalClosed++
		}
		pp.conns = nil
		pp.mu.Unlock()

		// Force-close active (checked-out) connections to unblock any in-flight operations.
		// This causes StreamBody/sendCommand reads to fail immediately, allowing prefetch
		// workers to exit and SegmentFetcher.Close() to complete without hanging.
		pp.activeConns.Range(func(key, _ any) bool {
			_ = key.(*Connection).Close()
			totalClosed++
			return true
		})
	}

	c.logger.Info().
		Int("total_closed", totalClosed).
		Msg("Connection manager closed")

	return nil
}

// SpeedTest runs a speed test for a specific provider.
func (c *Client) SpeedTest(ctx context.Context, providerHost string, messageID string) SpeedTestResult {
	result := SpeedTestResult{
		Provider: providerHost,
		TestedAt: utils.Now(),
	}

	// Find the provider by host
	var targetProvider *config.UsenetProvider
	for i := range c.providers {
		if c.providers[i].Host == providerHost {
			targetProvider = &c.providers[i]
			break
		}
	}

	if targetProvider == nil {
		result.Error = "provider not found"
		c.speedTestResults.Store(providerHost, result)
		return result
	}

	// Create connection
	conn, err := c.createConnection(ctx, *targetProvider)
	if err != nil {
		result.Error = fmt.Sprintf("connection failed: %v", err)
		c.speedTestResults.Store(providerHost, result)
		return result
	}
	defer func(conn *Connection) {
		_ = conn.Close()
	}(conn)

	// Measure latency using ping (true network RTT)
	pingStart := utils.Now()
	if err := conn.ping(); err != nil {
		result.Error = fmt.Sprintf("ping failed: %v", err)
		c.speedTestResults.Store(providerHost, result)
		return result
	}
	result.LatencyMs = time.Since(pingStart).Milliseconds()

	// If no messageID provided, just return latency
	if messageID == "" {
		c.speedTestResults.Store(providerHost, result)
		return result
	}

	// Download the segment to measure actual speed
	downloadStart := utils.Now()
	data, err := conn.GetBody(messageID)
	downloadDuration := time.Since(downloadStart)

	if err != nil {
		result.Error = fmt.Sprintf("download failed: %v", err)
		c.speedTestResults.Store(providerHost, result)
		return result
	}

	result.BytesRead = int64(len(data))

	// Calculate speed in MB/s
	if downloadDuration.Seconds() > 0 {
		result.SpeedMBps = float64(result.BytesRead) / downloadDuration.Seconds() / (1024 * 1024)
	}

	c.speedTestResults.Store(providerHost, result)
	return result
}

// GetSpeedTestResults returns all stored speed test results
func (c *Client) GetSpeedTestResults() map[string]SpeedTestResult {
	results := make(map[string]SpeedTestResult)
	c.speedTestResults.Range(func(host string, result SpeedTestResult) bool {
		results[host] = result
		return true
	})
	return results
}

// GetSpeedTestResult returns the speed test result for a specific provider
func (c *Client) GetSpeedTestResult(providerHost string) (SpeedTestResult, bool) {
	return c.speedTestResults.Load(providerHost)
}
