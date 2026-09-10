package nntp

import (
	"bufio"
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/textproto"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/logger"
	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// ProviderPool manages connections for a single provider using a LIFO stack
type ProviderPool struct {
	conns       []*connectionEntry // Stack: Push/Pop from end
	mu          sync.Mutex         // Protects conns slice only
	slots       chan struct{}      // Semaphore: capacity = max connections
	waiting     [workloadCount]int // Protected by Client.waitMu
	waitingMask atomic.Uint32
	max         int
	config      config.UsenetProvider
	activeConns sync.Map // *Connection → struct{}; tracks checked-out connections for force-close on shutdown

	// Dial cooldown: consecutive createConnection failures arm a backoff
	// window during which the any-provider scan routes around this provider
	// instead of paying a full dial/handshake timeout per acquisition. A
	// TCP-blackholed primary previously added the whole timeout to EVERY
	// checkout while a healthy secondary sat warm with pooled connections
	// (see BenchmarkAcquireDeadPrimary). The cooldown only ever reroutes:
	// when no eligible provider is warm, dials proceed regardless, so
	// single-provider setups keep their fail-fast behavior.
	dialFailStreak    atomic.Int32
	dialCooldownUntil atomic.Int64 // nanotimeNow deadline; 0 = no cooldown
}

// maxDialCooldown caps the exponential dial backoff. Kept short relative to
// the DFS no-progress windows so a recovered provider is retried promptly.
const maxDialCooldown = 15 * time.Second

func (pp *ProviderPool) inDialCooldown() bool {
	until := pp.dialCooldownUntil.Load()
	return until != 0 && nanotimeNow() < until
}

func (pp *ProviderPool) noteDialFailure() {
	streak := pp.dialFailStreak.Add(1)
	backoff := min(time.Second<<min(streak-1, 4), maxDialCooldown)
	pp.dialCooldownUntil.Store(nanotimeNow() + int64(backoff))
}

func (pp *ProviderPool) noteDialSuccess() {
	if pp.dialFailStreak.Load() != 0 {
		pp.dialFailStreak.Store(0)
		pp.dialCooldownUntil.Store(0)
	}
}

func (pp *ProviderPool) hasIdle() bool {
	pp.mu.Lock()
	n := len(pp.conns)
	pp.mu.Unlock()
	return n > 0
}

// isWarm reports whether this pool can plausibly produce a connection
// without dialing through an active cooldown: it isn't cooling down at all,
// has idle pooled connections, or has checked-out connections whose return
// will restock the pool.
func (pp *ProviderPool) isWarm() bool {
	if !pp.inDialCooldown() {
		return true
	}
	if pp.hasIdle() {
		return true
	}
	return len(pp.slots) > 0
}

// Client manages a pool of NNTP connections.
type Client struct {
	pools map[string]*ProviderPool // Map Key: config.UsenetProvider.ID()
	// orderedPools is index-aligned with providers so the per-acquisition
	// scan loops reach a provider's pool by index instead of building an
	// ID string (fmt.Sprintf + allocation) for a map lookup on every pass.
	orderedPools []*ProviderPool
	providers    []config.UsenetProvider
	logger       zerolog.Logger

	retries int // Number of retries per provider for transient errors

	// waitMu guards the priority queues of parked acquirers. releaseSlot hands
	// a freed slot directly to the oldest compatible waiter in the highest
	// priority class under this lock.
	waitMu  sync.Mutex
	waiters [workloadCount]waiterQueue
	// admission holds lock-free cumulative telemetry for contended connection
	// acquisitions. The uncontended path never touches these counters.
	admission [workloadCount]admissionMetrics
	// streamBackupWait is an opt-in latency budget for urgent playback. After
	// this long queued on the primary tier, demand may spill to a configured
	// backup provider. Zero preserves completion-only backup semantics.
	streamBackupWait       time.Duration
	streamBackupSpillovers atomic.Uint64

	closed atomic.Bool
	// Speed test results storage
	speedTestResults *xsync.Map[string, SpeedTestResult]

	// repairPool is the shared worker pool that processes BatchStat
	// chunks. Sized at construction from cfg.Repair.NNTPConnectionPercent.
	// Replaces the previous RepairBank counting semaphore + per-call
	// conc.Pool design — that pattern produced N × bank.Capacity
	// goroutines under N concurrent BatchStat calls because each call
	// sized its own pool to the entire bank capacity. The shared pool
	// caps total worker goroutines to exactly pool.Capacity().
	repairPool *RepairPool

	// TCP socket buffer sizes (bytes) applied to every new connection. 0 means
	// "leave OS autotuning untouched". Sized from cfg.Usenet.Socket*Buffer.
	// At high RTT the receive buffer is the single-connection throughput cap
	// (≈ buffer ÷ RTT), so it must cover the bandwidth-delay product.
	sockReadBuf  int
	sockWriteBuf int

	// Pool lifecycle tuning, resolved at construction from DefaultTimeouts
	// with an optional cfg.Usenet.ConnIdleTimeout override. Kept per-client
	// (rather than on the package-level timeouts var) so config reloads that
	// rebuild the client can't race connections still using the old one.
	idleTimeout    time.Duration // close pooled conns unused for this long
	staleThreshold time.Duration // verify-ping on checkout after this much inactivity
	pingInterval   time.Duration // reaper keepalive-ping cadence for idle conns
	pingTimeout    time.Duration // budget for a checkout verify-ping
	keepalivePing  time.Duration // budget for a reaper keepalive ping
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
	// lastPing is when the reaper last keepalive-pinged this idle entry.
	// Kept separate from lastUsed so pings keep the session alive without
	// counting as use — idle expiry stays keyed to real work only.
	lastPing time.Time
}

// lastActivity returns the most recent proof the connection was alive:
// either real use or a successful keepalive ping.
func (e *connectionEntry) lastActivity() time.Time {
	if e.lastPing.After(e.lastUsed) {
		return e.lastPing
	}
	return e.lastUsed
}

var connectionEntryPool = sync.Pool{
	New: func() any {
		return &connectionEntry{}
	},
}

func acquireConnectionEntry(conn *Connection, provider config.UsenetProvider, lastUsed time.Time) *connectionEntry {
	entry := connectionEntryPool.Get().(*connectionEntry)
	entry.conn = conn
	entry.provider = provider
	entry.lastUsed = lastUsed
	return entry
}

func releaseConnectionEntry(entry *connectionEntry) {
	if entry == nil {
		return
	}
	*entry = connectionEntry{}
	connectionEntryPool.Put(entry)
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
	// Deadline for the verify-ping (DATE) on checkout. This one is on the
	// critical path — a reader is already waiting — so it stays tight and
	// fails over to a fresh dial rather than waiting out a slow answer.
	PingTimeout time.Duration
	// Deadline for the reaper's background keepalive ping (DATE). Nobody is
	// waiting on it, so it gets a wider budget: pings share the link with
	// bulk BODY transfers, and under a saturated downlink a round trip can
	// take seconds. A tight budget there kills warm connections that are
	// merely queued behind a download, forcing a needless TCP+TLS+AUTH.
	KeepalivePingTimeout time.Duration
	// Health check connections idle longer than this
	StaleThreshold time.Duration
	// Close connections idle longer than this
	IdleTimeout time.Duration
	// Keepalive-ping idle pooled connections whose last activity (use or
	// ping) is older than this, instead of letting them go stale
	PingInterval time.Duration
	// How often to check for idle connections
	ReaperInterval time.Duration
}

// DefaultTimeouts returns production-tuned timeout values.
//
// IdleTimeout is deliberately long: players read in bursts (fill their
// buffer, go quiet for tens of seconds, read again), and closing warm
// connections between bursts forces a TCP+TLS+AUTH reconnect storm on
// every resume — measured at ~38k reconnects/week in production with the
// old 20s value. Stale sessions are handled by keepalive DATE pings
// (PingInterval, in the reaper) plus a verify-ping on checkout
// (StaleThreshold), not by closing early.
var DefaultTimeouts = TimeoutConfig{
	DialTimeout:       10 * time.Second,
	KeepAlive:         30 * time.Second,
	HandshakeTimeout:  10 * time.Second,
	StreamBodyTimeout: 60 * time.Second,
	PingTimeout:       1500 * time.Millisecond,
	// Wide enough to ride out queueing delay on a saturated link. A dead
	// path still costs only one of these per sweep: the first timeout
	// flushes the pool instead of pinging the rest of the batch.
	KeepalivePingTimeout: 5 * time.Second,
	StaleThreshold:       60 * time.Second,
	IdleTimeout:          5 * time.Minute,
	PingInterval:         30 * time.Second,
	ReaperInterval:       5 * time.Second,
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
	if in.KeepalivePingTimeout <= 0 {
		in.KeepalivePingTimeout = 5 * time.Second
	}
	if in.IdleTimeout <= 0 {
		in.IdleTimeout = 5 * time.Minute
	}
	// Keep stale checks meaningful: stale must be >0 and below idle timeout.
	if in.StaleThreshold <= 0 || in.StaleThreshold >= in.IdleTimeout {
		in.StaleThreshold = in.IdleTimeout / 2
		if in.StaleThreshold <= 0 {
			in.StaleThreshold = 10 * time.Second
		}
	}
	// Keepalive pings must fire well inside the idle window to be useful.
	if in.PingInterval <= 0 || in.PingInterval >= in.IdleTimeout {
		in.PingInterval = min(30*time.Second, in.IdleTimeout/2)
	}
	// A keepalive ping holds a pool slot while it runs. Keep that below the
	// cadence at which pings are issued so a sweep cannot still be waiting
	// when the next one is due.
	if in.KeepalivePingTimeout > in.PingInterval {
		in.KeepalivePingTimeout = in.PingInterval
	}
	if in.ReaperInterval <= 0 {
		in.ReaperInterval = 5 * time.Second
	}
	// Sweep frequently enough to avoid long idle overhang.
	maxReaperInterval := max(in.IdleTimeout/4, time.Second)
	if in.ReaperInterval > maxReaperInterval {
		in.ReaperInterval = maxReaperInterval
	}
	return in
}

// buildPools creates one ProviderPool per provider. Pools are keyed by
// provider ID (host:port/username), never bare host: dual-account setups
// list the same host twice, and host keying silently merged them into one
// pool with one account's connection cap.
func buildPools(providers []config.UsenetProvider) (map[string]*ProviderPool, []*ProviderPool) {
	pools := make(map[string]*ProviderPool, len(providers))
	ordered := make([]*ProviderPool, len(providers))
	for i, p := range providers {
		pp := &ProviderPool{
			conns:  make([]*connectionEntry, 0, p.MaxConnections),
			slots:  make(chan struct{}, p.MaxConnections),
			max:    p.MaxConnections,
			config: p,
		}
		pools[p.ID()] = pp
		ordered[i] = pp
	}
	return pools, ordered
}

// NewClient creates a new connection manager
func NewClient(cfg *config.Config) (*Client, error) {
	providers := cfg.Usenet.Providers
	if len(providers) == 0 {
		return nil, errors.New("no NNTP providers configured")
	}

	// Sort providers by priority (lower number = higher priority)
	slices.SortFunc(providers, func(a, b config.UsenetProvider) int {
		return cmp.Compare(a.Priority, b.Priority)
	})

	// Pre-normalize backbones once. excludes() runs on every connection
	// acquisition (potentially hundreds of times per second under load),
	// and the previous code re-ran strings.ToLower + TrimSpace per call
	// per provider, allocating a fresh string each time. Caching it here
	// turns the hot path into pure map lookups.
	for i := range providers {
		providers[i].Backbone = normalizeBackbone(providers[i].Backbone)
	}

	pools, orderedPools := buildPools(providers)
	cm := &Client{
		pools:            pools,
		orderedPools:     orderedPools,
		providers:        providers,
		retries:          cfg.Retries,
		logger:           logger.New("nntp-client"),
		speedTestResults: xsync.NewMap[string, SpeedTestResult](),
		sockReadBuf:      parseSockBuf(cfg.Usenet.SocketReadBuffer),
		sockWriteBuf:     parseSockBuf(cfg.Usenet.SocketWriteBuffer),
		idleTimeout:      timeouts.IdleTimeout,
		staleThreshold:   timeouts.StaleThreshold,
		pingInterval:     timeouts.PingInterval,
		pingTimeout:      timeouts.PingTimeout,
		keepalivePing:    timeouts.KeepalivePingTimeout,
	}
	if cfg.Usenet.ConnIdleTimeout != "" {
		if d, err := utils.ParseDuration(cfg.Usenet.ConnIdleTimeout); err != nil || d <= 0 {
			cm.logger.Warn().Str("conn_idle_timeout", cfg.Usenet.ConnIdleTimeout).
				Msg("invalid conn_idle_timeout, using default")
		} else {
			cm.idleTimeout = d
			// Keep the derived thresholds inside the configured window.
			if cm.staleThreshold >= cm.idleTimeout {
				cm.staleThreshold = cm.idleTimeout / 2
			}
			if cm.pingInterval >= cm.idleTimeout {
				cm.pingInterval = cm.idleTimeout / 2
			}
			if cm.keepalivePing > cm.pingInterval {
				cm.keepalivePing = cm.pingInterval
			}
		}
	}
	if value := cfg.Usenet.StreamBackupWait; value != "" && value != "0" {
		if d, err := utils.ParseDuration(value); err != nil || d <= 0 {
			cm.logger.Warn().Str("stream_backup_wait", value).
				Msg("invalid stream_backup_wait, backup spillover disabled")
		} else {
			cm.streamBackupWait = d
		}
	}
	cm.repairPool = cm.newRepairPool(cfg.Repair.NNTPConnectionPercent)

	// Start background reaper
	go cm.reaper()
	return cm, nil
}

// put returns a connection to the pool and releases the slot.
func (c *Client) put(conn *Connection, provider config.UsenetProvider) {
	if conn == nil {
		return
	}

	pp := conn.pool
	if pp == nil {
		_ = conn.Close()
		return
	}

	pp.activeConns.Delete(conn) // Deregister from active tracking

	// Don't return closed connections to pool
	if conn.IsClosed() {
		_ = conn.Close()
		c.releaseSlot(pp)
		return
	}

	if c.closed.Load() {
		_ = conn.Close()
		c.releaseSlot(pp)
		return
	}

	entry := acquireConnectionEntry(conn, provider, utils.Now())

	pp.mu.Lock()
	// Cap stack size (shouldn't happen with semaphore, but be safe)
	if len(pp.conns) >= pp.max {
		pp.mu.Unlock()
		_ = conn.Close()
		c.releaseSlot(pp)
		return
	}
	pp.conns = append(pp.conns, entry) // Push to stack
	pp.mu.Unlock()

	c.releaseSlot(pp) // connection is now available for reuse
}

// release closes a connection without returning it (for error cases)
func (c *Client) release(conn *Connection) {
	if conn != nil {
		_ = conn.Close()
		if pp := conn.pool; pp != nil {
			pp.activeConns.Delete(conn) // Deregister from active tracking
			c.releaseSlot(pp)
		}
	}
}

// checkEntryHealth reports whether a pooled entry is still usable.
// pingTimedOut is true when the entry failed its staleness verify-ping by
// timing out rather than erroring immediately: a reset means the server
// dropped this one session, but a silent timeout means the network path is
// gone — and every older entry idling below it in the stack is dead too.
func (c *Client) checkEntryHealth(entry *connectionEntry) (healthy, pingTimedOut bool) {
	if entry == nil || entry.conn == nil {
		return false, false
	}
	// Check if explicitly closed
	if entry.conn.IsClosed() {
		return false, false
	}
	// Verify with a ping if we haven't heard from this connection recently.
	// A successful reaper keepalive counts as activity, so freshly-pinged
	// connections skip the extra checkout round-trip.
	if time.Since(entry.lastActivity()) > c.staleThreshold {
		if err := entry.conn.ping(c.pingTimeout); err != nil {
			return false, isTimeoutLike(err)
		}
	}
	return true, false
}

// flushIdle closes every idle pooled connection on pp. Called when a
// checkout verify-ping times out: sequentially discovering that each
// remaining pooled connection is also dead would stall the checkout for
// len(conns) × PingTimeout.
func (c *Client) flushIdle(pp *ProviderPool) {
	pp.mu.Lock()
	drained := pp.conns
	pp.conns = nil
	pp.mu.Unlock()
	for _, entry := range drained {
		conn := entry.conn
		releaseConnectionEntry(entry)
		_ = conn.Close()
	}
}

// isIdleExpired reports whether a pooled connection has gone unused (by
// real work, not keepalive pings) long enough to close.
func (c *Client) isIdleExpired(lastUsed time.Time, now time.Time) bool {
	if lastUsed.IsZero() {
		return false
	}
	return now.Sub(lastUsed) > c.idleTimeout
}

// ErrAllProvidersFailed marks an error returned after ExecuteWithFailover
// exhausted both its per-provider retries and provider failover; outer retry
// loops should not multiply attempts on it.
var ErrAllProvidersFailed = errors.New("all providers failed")

// IsAllProvidersFailed reports whether err carries ErrAllProvidersFailed.
func IsAllProvidersFailed(err error) bool {
	return errors.Is(err, ErrAllProvidersFailed)
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

// getConnectionFromProvider gets a connection from a specific provider,
// parking in the shared waiter FIFO when the pool is saturated so releases
// hand it a slot directly alongside any-provider waiters. Dial cooldowns
// are ignored (allowDial=true): targeted callers — the repair BatchStat
// sweep — need this provider's answer specifically, and a prompt connection
// error is more accurate for them than a silent reroute.
func (c *Client) getConnectionFromProvider(ctx context.Context, workload Workload, provider config.UsenetProvider) (*Connection, config.UsenetProvider, error) {
	if !workload.valid() {
		return nil, provider, fmt.Errorf("invalid NNTP workload: %s", workload)
	}
	pp, ok := c.pools[provider.ID()]
	if !ok {
		return nil, provider, fmt.Errorf("provider pool not found: %s", provider.Host)
	}

	// Fast path: free slot, no queueing.
	if c.tryAcquireSlot(pp, workload) {
		conn, err := c.getOrCreateFromPool(ctx, pp, provider, true)
		if err != nil {
			c.releaseSlot(pp)
			return nil, provider, err
		}
		return conn, provider, nil
	}

	w := c.newQueuedWaiter(workload, []*ProviderPool{pp})
	c.register(w)
	if c.tryAcquireSlot(pp, workload) {
		// Won a raced slot directly; deregister drains any concurrent
		// handoff so the second slot is re-released, not leaked.
		c.deregister(w)
	} else {
		select {
		case got := <-w.handoff:
			pp = got
		case <-ctx.Done():
			c.deregister(w)
			c.finishWait(w, admissionCanceled)
			return nil, provider, ctx.Err()
		}
	}

	conn, err := c.getOrCreateFromPool(ctx, pp, provider, true)
	if err != nil {
		c.releaseSlot(pp)
		outcome := admissionFailed
		if ctx.Err() != nil {
			outcome = admissionCanceled
		}
		c.finishWait(w, outcome)
		return nil, provider, err
	}
	c.finishWait(w, admissionSucceeded)
	return conn, provider, nil
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
// Phase 2: If all busy, join the workload-aware admission queue
//
// Tiering: providers with Backup=true are NOT considered until every
// non-backup ("primary") provider is excluded. A primary's pool being
// merely busy is not enough — the caller waits for a primary slot to free
// up rather than dipping into a backup, unless urgent stream spillover was
// explicitly configured. The default matches the
// unlimited-primary + block-backup-for-completion model and prevents
// block providers from being billed for articles the primary could have
// served given a moment's patience.
//
// Within a tier, providers are still consumed opportunistically across
// hosts — so two unlimited primaries split load the way they do today.
func (c *Client) getAnyAvailableConnection(ctx context.Context, workload Workload, exclusions providerExclusions) (*Connection, config.UsenetProvider, error) {
	if !workload.valid() {
		return nil, config.UsenetProvider{}, fmt.Errorf("invalid NNTP workload: %s", workload)
	}
	useBackups := !c.hasEligibleProviderInTier(exclusions, false)
	return c.getAnyAvailableConnectionInTier(ctx, workload, exclusions, useBackups)
}

func (c *Client) hasEligibleProviderInTier(exclusions providerExclusions, backup bool) bool {
	for _, provider := range c.providers {
		if provider.Backup == backup && !exclusions.excludes(provider) {
			return true
		}
	}
	return false
}

func (c *Client) getAnyAvailableConnectionInTier(ctx context.Context, workload Workload, exclusions providerExclusions, useBackups bool) (*Connection, config.UsenetProvider, error) {

	// Cooldowns are advisory reroutes, never a denial of service: skip a
	// cooling-down provider only when some other eligible provider is warm
	// (see ProviderPool.isWarm). When the whole tier is cold, dial anyway —
	// otherwise a single-provider setup would trade its fail-fast behavior
	// for a silent wait.
	ignoreCooldowns := true
	for i, provider := range c.providers {
		if provider.Backup != useBackups || exclusions.excludes(provider) {
			continue
		}
		if c.orderedPools[i].isWarm() {
			ignoreCooldowns = false
			break
		}
	}

	// Phase 1: Non-blocking scan - try to get a free slot from any provider
	// within the current tier.
	eligibleCount := 0
	for i, provider := range c.providers {
		if provider.Backup != useBackups || exclusions.excludes(provider) {
			continue
		}
		eligibleCount++
		pp := c.orderedPools[i]
		if !ignoreCooldowns && !pp.isWarm() {
			continue // provider cooling down after dial failures; route around it
		}

		if c.tryAcquireSlot(pp, workload) {
			// Got a slot - try to get or create connection
			conn, err := c.getOrCreateFromPool(ctx, pp, provider, ignoreCooldowns)
			if err != nil {
				c.releaseSlot(pp) // Release slot on error
				continue          // Try next provider
			}
			return conn, provider, nil
		}
	}

	if eligibleCount == 0 {
		return nil, config.UsenetProvider{}, errors.New("no eligible providers available")
	}

	// Phase 2: All providers in this tier busy - block until a slot frees.
	// When the primary tier is in use this is the wait that lets a backup
	// remain idle rather than getting roped in.
	eligible := make([]*ProviderPool, 0, eligibleCount)
	for i, provider := range c.providers {
		if provider.Backup == useBackups && !exclusions.excludes(provider) {
			eligible = append(eligible, c.orderedPools[i])
		}
	}

	if !useBackups && workload == WorkloadStreamDemand && c.streamBackupWait > 0 && c.hasEligibleProviderInTier(exclusions, true) {
		waitCtx, cancel := context.WithTimeoutCause(ctx, c.streamBackupWait, errStreamBackupWaitElapsed)
		conn, provider, err := c.waitForConnection(waitCtx, workload, eligible)
		spillToBackup := errors.Is(context.Cause(waitCtx), errStreamBackupWaitElapsed) && ctx.Err() == nil
		cancel()
		if spillToBackup {
			c.streamBackupSpillovers.Add(1)
			return c.getAnyAvailableConnectionInTier(ctx, workload, exclusions, true)
		}
		return conn, provider, err
	}
	return c.waitForConnection(ctx, workload, eligible)
}

var errStreamBackupWaitElapsed = errors.New("stream primary-tier wait elapsed")

// waitForConnection blocks until a slot is available on any eligible
// provider. The waiter registers in its priority queue before each scan so a
// release between scan and park cannot be missed. Handoffs are strict across
// workload classes and approximately FIFO within each class.
func (c *Client) waitForConnection(ctx context.Context, workload Workload, eligible []*ProviderPool) (*Connection, config.UsenetProvider, error) {
	w := c.newQueuedWaiter(workload, eligible)

	// The fallback tick guards against a slot release that bypasses
	// releaseSlot turning into an indefinite park, and doubles as the
	// cooldown-expiry poll when every eligible provider is cold.
	const wakeFallback = 250 * time.Millisecond
	timer := time.NewTimer(wakeFallback)
	defer timer.Stop()

	var lastErr error
	for {
		if c.closed.Load() {
			c.finishWait(w, admissionFailed)
			return nil, config.UsenetProvider{}, errors.New("nntp client is closed")
		}
		c.register(w)

		busy := 0
		failed := 0
		ignoreCooldowns := true
		for _, pp := range eligible {
			if pp.isWarm() {
				ignoreCooldowns = false
				break
			}
		}
		for _, pp := range eligible {
			if !ignoreCooldowns && !pp.isWarm() {
				continue
			}
			if c.tryAcquireSlot(pp, workload) {
				conn, err := c.getOrCreateFromPool(ctx, pp, pp.config, ignoreCooldowns)
				if err != nil {
					c.releaseSlot(pp)
					if errors.Is(err, errDialCooldown) {
						// Raced into a cooldown (pool drained after the
						// isWarm check): wait it out, don't surface it.
						busy++
					} else {
						lastErr = err
						failed++
					}
					continue
				}
				c.deregister(w)
				c.finishWait(w, admissionSucceeded)
				return conn, pp.config, nil
			} else {
				busy++
			}
		}
		// Every provider had a free slot and failed to produce a connection:
		// surface the error instead of spinning on dial failures.
		if busy == 0 && failed > 0 {
			c.deregister(w)
			c.finishWait(w, admissionFailed)
			return nil, config.UsenetProvider{}, lastErr
		}

		timer.Reset(wakeFallback)
		select {
		case pp := <-w.handoff:
			// The releaser already removed us from the queue; we own a held
			// slot on pp now. Do not deregister here — the token is consumed.
			conn, err := c.getOrCreateFromPool(ctx, pp, pp.config, false)
			if err != nil {
				c.releaseSlot(pp)
				if !errors.Is(err, errDialCooldown) {
					lastErr = err
				}
				continue
			}
			c.finishWait(w, admissionSucceeded)
			return conn, pp.config, nil
		case <-timer.C:
			c.deregister(w)
		case <-ctx.Done():
			c.deregister(w)
			c.finishWait(w, admissionCanceled)
			return nil, config.UsenetProvider{}, ctx.Err()
		}
	}
}

// errDialCooldown is returned by getOrCreateFromPool when the pool has no
// idle connection and new dials are suppressed by an active cooldown.
var errDialCooldown = errors.New("provider dials cooling down after failures")

// getOrCreateFromPool tries to get an existing connection from pool, or creates a new one.
// Caller must have already acquired a slot from pp.slots. allowDial=false
// makes an active dial cooldown return errDialCooldown instead of dialing;
// pooled connections are always eligible regardless.
func (c *Client) getOrCreateFromPool(ctx context.Context, pp *ProviderPool, provider config.UsenetProvider, allowDial bool) (*Connection, error) {
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
			if c.isIdleExpired(entry.lastUsed, now) {
				conn := entry.conn
				releaseConnectionEntry(entry)
				_ = conn.Close()
				continue
			}

			// Health check outside lock
			healthy, pingTimedOut := c.checkEntryHealth(entry)
			if healthy {
				conn := entry.conn
				releaseConnectionEntry(entry)
				conn.pool = pp                         // already set at creation; kept authoritative
				pp.activeConns.Store(conn, struct{}{}) // Register as active (checked-out)
				return conn, nil
			}
			// Unhealthy - close and try next pooled connection
			conn := entry.conn
			releaseConnectionEntry(entry)
			_ = conn.Close()
			if pingTimedOut {
				// The freshest pooled connection timed out its verify ping:
				// the path to this provider is gone and the older entries
				// share its fate. Close them all now instead of paying
				// PingTimeout for each in sequence.
				c.flushIdle(pp)
			}
			continue
		}
		pp.mu.Unlock()
		break
	}

	// No pooled connection available, create new one
	if !allowDial && pp.inDialCooldown() {
		return nil, errDialCooldown
	}
	conn, err := c.createConnection(ctx, provider)
	if err != nil {
		if ctx.Err() == nil {
			pp.noteDialFailure()
		}
		return nil, err
	}
	pp.noteDialSuccess()
	conn.pool = pp                         // identity for put/release routing
	pp.activeConns.Store(conn, struct{}{}) // Register as active (checked-out)
	return conn, nil
}

// parseSockBuf converts a size string ("4MB") to a byte count for SO_RCVBUF/
// SO_SNDBUF. Empty/"0"/invalid/negative → 0, meaning "leave OS autotuning
// untouched". Clamped to a sane ceiling so a typo can't request gigabytes.
func parseSockBuf(s string) int {
	n, err := config.ParseSize(s)
	if err != nil || n <= 0 {
		return 0
	}
	const maxSockBuf = 256 << 20 // 256MB
	if n > maxSockBuf {
		n = maxSockBuf
	}
	return int(n)
}

// socketControl returns a Dialer.Control hook that sets SO_RCVBUF/SO_SNDBUF on
// the socket *before* connect, so the SYN advertises a window scale large
// enough for the configured buffer — the part that actually matters at high
// RTT. Returns nil (no hook, zero overhead) when both sizes are 0. The OS
// still caps the effective size (Linux net.core.rmem_max/wmem_max, macOS
// kern.ipc.maxsockbuf); those sysctls must be raised to realise large windows.
func (c *Client) socketControl() func(network, address string, rc syscall.RawConn) error {
	rb, wb := c.sockReadBuf, c.sockWriteBuf
	if rb <= 0 && wb <= 0 {
		return nil
	}
	return func(_, _ string, rc syscall.RawConn) error {
		return rc.Control(func(fd uintptr) {
			setSocketBuffers(fd, rb, wb)
		})
	}
}

// tuneTCP applies TCP_NODELAY and (re)applies the configured socket buffers
// on the established connection. The pre-connect Control hook does the work
// that matters for window scaling; this reinforces the sizes post-dial and
// covers the TLS-wrapped path. Sizes of 0 are skipped so OS autotuning is
// preserved when the operator opted into it.
func (c *Client) tuneTCP(tcpConn *net.TCPConn) {
	_ = tcpConn.SetNoDelay(true)
	if c.sockReadBuf > 0 {
		_ = tcpConn.SetReadBuffer(c.sockReadBuf)
	}
	if c.sockWriteBuf > 0 {
		_ = tcpConn.SetWriteBuffer(c.sockWriteBuf)
	}
}

// createConnection creates a new NNTP connection to a provider
func (c *Client) createConnection(ctx context.Context, provider config.UsenetProvider) (*Connection, error) {
	address := fmt.Sprintf("%s:%d", provider.Host, provider.Port)

	var netConn net.Conn
	var err error

	dialer := &net.Dialer{
		Timeout:   timeouts.DialTimeout,
		KeepAlive: timeouts.KeepAlive,
		Control:   c.socketControl(),
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

	// Optimize TCP socket (buffer sizing already applied pre-connect via
	// Dialer.Control; this reinforces it and covers the TLS-wrapped conn).
	if tcpConn, ok := netConn.(*net.TCPConn); ok {
		c.tuneTCP(tcpConn)
	}
	if tlsConn, ok := netConn.(*tls.Conn); ok {
		if tcpConn, ok := tlsConn.NetConn().(*net.TCPConn); ok {
			c.tuneTCP(tcpConn)
		}
	}

	// The reader matches the 128KB chunks the body copier consumes (the
	// socket buffer, not bufio, is the RTT window); the writer carries only
	// short command lines.
	reader := bufio.NewReaderSize(netConn, 128*1024)
	writer := bufio.NewWriterSize(netConn, 4*1024)

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
	// bodyReader follows conn.reader, so this stays valid across the
	// STARTTLS reader swap.
	conn.bodyDec = nntpyenc.NewBodyDecoder(&bodyReader{c: conn}, conn.nextBodyBuffer)

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

	// Registered with the body-idle janitor for the connection's lifetime;
	// idleNS=0 disarms it whenever no body copy is in flight.
	bodyIdleJanitor.add(conn)

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
		var toClose, toPing []*connectionEntry

		// Cap how many entries one sweep may hold slots for: after a playback
		// pause every pooled connection crosses pingInterval in the same
		// sweep, and pinging them all at once would leave a resuming reader
		// with no free slots. The remainder is pinged on later sweeps.
		maxPing := max(1, pp.max/4)

		pp.mu.Lock()
		kept := pp.conns[:0]
		for _, entry := range pp.conns {
			switch {
			case c.isIdleExpired(entry.lastUsed, now):
				toClose = append(toClose, entry)
			case now.Sub(entry.lastActivity()) > c.pingInterval && len(toPing) < maxPing:
				// Candidate for a keepalive ping. Take a connection slot so
				// the provider's total stays capped while the entry is out of
				// the pool being pinged — otherwise a checkout burst could
				// dial replacements and briefly exceed max_connections. If no
				// slot is free the pool is busy and the conn will either be
				// used (real activity) or expire soon; skip this sweep.
				select {
				case pp.slots <- struct{}{}:
					toPing = append(toPing, entry)
				default:
					kept = append(kept, entry)
				}
			default:
				kept = append(kept, entry)
			}
		}
		// Nil out trailing pointers to help GC
		for i := len(kept); i < len(pp.conns); i++ {
			pp.conns[i] = nil
		}
		pp.conns = kept
		pp.mu.Unlock()

		for _, entry := range toClose {
			conn := entry.conn
			releaseConnectionEntry(entry)
			_ = conn.Close()
		}
		// Ping outside the pool lock, in parallel, so slot-held time stays
		// one round-trip rather than the whole batch's.
		if len(toPing) > 0 {
			c.keepAliveBatch(pp, toPing, now)
		}
	}
}

// keepaliveState is one sweep's shared verdict on whether a provider is
// still reachable.
type keepaliveState struct {
	ok       atomic.Bool // some ping answered: the path is up
	pathDown atomic.Bool // a ping timed out and none has answered
}

// errPathDown marks an entry discarded unpinged because an earlier ping in
// the same sweep timed out.
var errPathDown = errors.New("provider path down, skipped keepalive ping")

// keepAliveBatch pings one sweep's worth of idle entries in parallel.
//
// A ping that times out is not one dead session: a live peer that dropped a
// session answers with RST or EOF, so silence means the path to the provider
// is gone and every other idle connection on it is dead too. The first
// timeout therefore short-circuits the rest of the batch and flushes the
// pool, the same rule checkout applies in getOrCreateFromPool. Without it a
// provider blip costs len(batch) × KeepalivePingTimeout of held slots to
// learn what the first ping already proved.
func (c *Client) keepAliveBatch(pp *ProviderPool, toPing []*connectionEntry, now time.Time) {
	var wg sync.WaitGroup
	var st keepaliveState
	workers := min(len(toPing), 4)
	pingCh := make(chan *connectionEntry, len(toPing))
	for _, entry := range toPing {
		pingCh <- entry
	}
	close(pingCh)

	// Per-worker tallies, merged after the wait: rolling them up avoids one
	// log line per dead connection, which is what made a single provider
	// blip look like a flood.
	type tally struct {
		failed int
		err    error
	}
	tallies := make([]tally, workers)
	for i := range workers {
		wg.Go(func() {
			for entry := range pingCh {
				err := c.keepAlive(pp, entry, now, &st)
				if err == nil {
					continue
				}
				tallies[i].failed++
				// errPathDown is a consequence, not a cause — keep looking
				// for the ping error that actually condemned the batch.
				if tallies[i].err == nil && !errors.Is(err, errPathDown) {
					tallies[i].err = err
				}
			}
		})
	}
	wg.Wait()

	failed, firstErr := 0, error(nil)
	for _, t := range tallies {
		failed += t.failed
		if firstErr == nil {
			firstErr = t.err
		}
	}
	if failed == 0 {
		return
	}
	// Flush only if nothing on this provider answered: a sweep with both a
	// timeout and a live reply means one wedged session, not a dead path,
	// and closing the connections that just proved themselves would throw
	// away exactly the warm pool this reaper exists to keep.
	flushed := st.pathDown.Load() && !st.ok.Load()
	if flushed {
		c.flushIdle(pp)
	}

	// If it's a timeout, or skippable error, skip logs
	if !customerror.IsSilentError(firstErr) {
		c.logger.Trace().Err(firstErr).
			Str("provider", pp.config.Host).
			Int("failed", failed).
			Int("batch", len(toPing)).
			Bool("pool_flushed", flushed).
			Msg("keepalive pings failed, closed idle connections")
	}
}

// keepAlive pings an idle connection that was removed from the pool (with a
// slot held) and returns it on success. Failed pings close the connection —
// exactly the sessions the old aggressive idle timeout existed to avoid
// handing out, caught here without sacrificing the warm pool. A timeout may
// also condemn the path, which tells the rest of the batch to give up
// unpinged.
func (c *Client) keepAlive(pp *ProviderPool, entry *connectionEntry, now time.Time, st *keepaliveState) error {
	discard := func() {
		conn := entry.conn
		releaseConnectionEntry(entry)
		_ = conn.Close()
		c.releaseSlot(pp)
	}

	if st.pathDown.Load() {
		// A sibling ping already timed out; this one would too.
		discard()
		return errPathDown
	}
	if err := entry.conn.ping(c.keepalivePing); err != nil {
		// Silence condemns the path, but only while nothing has answered:
		// a live peer that dropped one session sends RST or EOF, so a
		// timeout alongside a working sibling is a wedged session, not an
		// outage.
		if isTimeoutLike(err) && !st.ok.Load() {
			st.pathDown.Store(true)
		}
		discard()
		return err
	}
	st.ok.Store(true)
	entry.lastPing = now

	pp.mu.Lock()
	if c.closed.Load() || len(pp.conns) >= pp.max {
		pp.mu.Unlock()
		discard()
		return nil
	}
	pp.conns = append(pp.conns, entry)
	pp.mu.Unlock()
	c.releaseSlot(pp) // connection is available again
	return nil
}

// Stats returns current pool statistics
func (c *Client) Stats() map[string]any {
	if c.closed.Load() {
		return nil
	}

	stats := make(map[string]any)
	providers := make([]map[string]any, 0, len(c.providers))

	totalActive := 0
	totalIdle := 0
	totalMax := 0

	for _, p := range c.providers {
		pp, ok := c.pools[p.ID()]
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

		providerInfo := map[string]any{
			"id":              p.ID(),
			"host":            p.Host,
			"username":        p.Username,
			"port":            p.Port,
			"max_connections": maxC,
			"active":          active,
			"idle":            idle,
			"ssl":             p.SSL,
			"waiting":         c.providerWaiting(pp),
		}

		// Add speed test result if available
		if result, ok := c.speedTestResults.Load(p.ID()); ok {
			providerInfo["speed_test"] = map[string]any{
				"latency_ms": result.LatencyMs,
				"speed_mbps": result.SpeedMBps,
				"bytes_read": result.BytesRead,
				"tested_at":  result.TestedAt.Format("2006-01-02T15:04:05Z07:00"),
				"error":      result.Error,
			}
		}

		providers = append(providers, providerInfo)
	}

	waiting, oldestWaitNS := c.queueSnapshot()
	admissionStats := make(map[string]any, workloadCount)
	for workload := WorkloadStreamDemand; workload < workloadCount; workload++ {
		admissionStats[workload.String()] = c.admission[workload].snapshot().stats(waiting[workload], oldestWaitNS[workload])
	}
	poolStats := map[string]any{
		"max_connections": totalMax,
		"total_created":   totalActive + totalIdle,
		"active":          totalActive,
		"idle":            totalIdle,
		"waiting": map[string]int{
			WorkloadStreamDemand.String():   waiting[WorkloadStreamDemand],
			WorkloadStreamPrefetch.String(): waiting[WorkloadStreamPrefetch],
			WorkloadDownload.String():       waiting[WorkloadDownload],
			WorkloadBackground.String():     waiting[WorkloadBackground],
		},
		"admission":                      admissionStats,
		"stream_backup_spillovers_total": c.streamBackupSpillovers.Load(),
	}

	stats["pool"] = poolStats
	stats["providers"] = providers

	return stats
}

func (c *Client) Stat(ctx context.Context, workload Workload, messageID string) (int, string, error) {
	if c.closed.Load() {
		return 0, "", errors.New("nntp client is closed")
	}

	var num int
	var id string
	err := c.ExecuteWithFailover(ctx, workload, func(conn *Connection) error {
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

type batchStatState struct {
	sawNotFound bool
	sawOtherErr bool
	lastErr     error
	exclusions  providerExclusions
}

type providerExclusions struct {
	hosts     map[string]struct{}
	backbones map[string]struct{}
}

func (e *providerExclusions) excludeHost(host string) {
	if host == "" {
		return
	}
	if e.hosts == nil {
		e.hosts = make(map[string]struct{})
	}
	e.hosts[host] = struct{}{}
}

func (e *providerExclusions) excludeBackbone(backbone string) {
	backbone = normalizeBackbone(backbone)
	if backbone == "" {
		return
	}
	if e.backbones == nil {
		e.backbones = make(map[string]struct{})
	}
	e.backbones[backbone] = struct{}{}
}

func (e providerExclusions) excludes(provider config.UsenetProvider) bool {
	// Fast path: the overwhelming majority of acquisitions happen with
	// no exclusions in flight (first attempt before any failover). Skip
	// the map lookups and backbone work entirely.
	if e.hosts == nil && e.backbones == nil {
		return false
	}
	if _, ok := e.hosts[provider.Host]; ok {
		return true
	}
	// Backbone is pre-normalized at NewClient time, so no per-call
	// strings.ToLower / TrimSpace allocation here.
	if provider.Backbone == "" {
		return false
	}
	_, ok := e.backbones[provider.Backbone]
	return ok
}

func normalizeBackbone(backbone string) string {
	return strings.ToLower(strings.TrimSpace(backbone))
}

func excludeForArticleNotFound(exclusions *providerExclusions, provider config.UsenetProvider) {
	if exclusions == nil {
		return
	}
	// Backbone is pre-normalized at NewClient time, so we read it raw.
	if provider.Backbone != "" {
		exclusions.excludeBackbone(provider.Backbone)
		return
	}
	exclusions.excludeHost(provider.Host)
}

// BatchStat checks the availability of many message IDs using NNTP STAT and
// stops queued work after the first article is definitively missing.
func (c *Client) BatchStat(ctx context.Context, messageIDs []string) (*BatchStatResult, error) {
	if c.closed.Load() {
		return nil, errors.New("nntp client is closed")
	}
	if len(messageIDs) == 0 {
		return &BatchStatResult{}, nil
	}

	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Per-chunk batch size is adaptive: we want enough chunks to keep
	// every pool worker busy on this BatchStat call, but not so few IDs
	// per chunk that we pay the per-chunk overhead (provider-acquire
	// round-trips, per-call slice allocations in batchStatAcrossProviders,
	// callback dispatch) on near-nothing.
	//
	// Ceiling: keeps cancellation latency and connection-drop blast
	// radius bounded for a single chunk's worth of STATs.
	// Floor: smaller than this and per-chunk overhead starts dominating
	// the actual STAT round-trip.
	const (
		statBatchSize    = 50
		statBatchMinSize = 10
	)
	batchSize := pickStatBatchSize(len(messageIDs), c.repairPool.Capacity(), statBatchSize, statBatchMinSize)

	type chunk struct {
		startIdx   int
		messageIDs []string
	}
	chunks := make([]chunk, 0, (len(messageIDs)+batchSize-1)/batchSize)
	for i := 0; i < len(messageIDs); i += batchSize {
		end := min(i+batchSize, len(messageIDs))
		chunks = append(chunks, chunk{startIdx: i, messageIDs: messageIDs[i:end]})
	}

	allResults := make([]StatResult, len(messageIDs))
	for i, msgID := range messageIDs {
		allResults[i].MessageID = msgID
	}

	// Each chunk submits to the shared RepairPool. Concurrency is bounded
	// by the pool's worker count, NOT by a per-call pool — so M concurrent
	// BatchStat calls share the same pool.Capacity() workers in FIFO
	// arrival order instead of each spinning up its own bank-sized pool.
	// Tasks write disjoint index ranges of allResults; the only shared
	// mutable state is the early-bailout cancel.
	markChunkErr := func(startIdx, n int, e error) {
		for i := startIdx; i < startIdx+n; i++ {
			allResults[i].Available = false
			allResults[i].Error = e
		}
	}
	var bailOnce sync.Once
	var wg sync.WaitGroup
	for _, ch := range chunks {
		wg.Add(1)
		err := c.repairPool.Submit(workCtx, ch.messageIDs, func(results []StatResult, taskErr error) {
			defer wg.Done()
			if taskErr != nil {
				// Mirrors the previous behaviour: a chunk-level connection
				// error fails the whole chunk (partial results discarded).
				markChunkErr(ch.startIdx, len(ch.messageIDs), taskErr)
				return
			}
			for i := range results {
				allResults[ch.startIdx+i] = results[i]
			}
			// Bail out the rest of the sample as soon as one segment is
			// definitively missing — not-found on every provider, so the
			// terminal classification carries an ArticleNotFound error.
			// Per-segment provider failover has already completed inside
			// this chunk before we get here, so this never short-circuits
			// failover.
			for _, r := range results {
				if !r.Available && IsArticleNotFoundError(r.Error) {
					bailOnce.Do(cancel)
					break
				}
			}
		})
		if err != nil {
			// Submit refused the task — caller's ctx expired before a worker
			// took it, or the pool is shutting down. Synthesize a chunk-wide
			// error so the result vector still has the right shape.
			markChunkErr(ch.startIdx, len(ch.messageIDs), err)
			wg.Done()
		}
	}
	wg.Wait()

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
		if nntpErr, ok := errors.AsType[*Error](r.Error); ok && nntpErr.Type == ErrorTypeArticleNotFound {
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

		queryIdxs := make([]int, 0, len(unresolved))
		chunkIDs := make([]string, 0, len(unresolved))
		for _, idx := range unresolved {
			if states[idx].exclusions.excludes(provider) {
				continue
			}
			queryIdxs = append(queryIdxs, idx)
			chunkIDs = append(chunkIDs, messageIDs[idx])
		}
		if len(queryIdxs) == 0 {
			continue
		}

		providerResults, err := c.batchStatOnProvider(ctx, provider, chunkIDs)
		if err != nil && len(providerResults) == 0 {
			for _, idx := range queryIdxs {
				states[idx].sawOtherErr = true
				states[idx].lastErr = err
			}
			continue
		}

		nextUnresolved := make([]int, 0, len(unresolved))
		queryPos := 0
		for _, idx := range unresolved {
			if states[idx].exclusions.excludes(provider) {
				nextUnresolved = append(nextUnresolved, idx)
				continue
			}

			if queryPos >= len(providerResults) {
				states[idx].sawOtherErr = true
				if err != nil {
					states[idx].lastErr = err
				} else {
					states[idx].lastErr = NewConnectionError(fmt.Errorf("provider %s returned incomplete batch results", provider.Host))
				}
				nextUnresolved = append(nextUnresolved, idx)
				continue
			}

			res := providerResults[queryPos]
			queryPos++
			if res.Available {
				results[idx] = res
				continue
			}

			if nntpErr, ok := errors.AsType[*Error](res.Error); res.Error != nil && ok && nntpErr.Type == ErrorTypeArticleNotFound {
				states[idx].sawNotFound = true
				excludeForArticleNotFound(&states[idx].exclusions, provider)
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

const statPipelineDepth = 16

func (c *Client) batchStatOnProvider(ctx context.Context, provider config.UsenetProvider, messageIDs []string) ([]StatResult, error) {
	// A background worker returns its connection after every shallow pipeline.
	// This bounds the delay seen by a stream that arrives while all provider
	// slots are busy without reintroducing one RTT per STAT.
	results := make([]StatResult, 0, len(messageIDs))
	for start := 0; start < len(messageIDs); start += statPipelineDepth {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		end := min(start+statPipelineDepth, len(messageIDs))
		conn, providerCfg, err := c.getConnectionFromProvider(ctx, WorkloadBackground, provider)
		if err != nil {
			return results, err
		}

		cancelFinished := make(chan struct{})
		stopCancel := context.AfterFunc(ctx, func() {
			defer close(cancelFinished)
			_ = conn.Close()
		})
		window, statErr := conn.StatBatch(messageIDs[start:end])
		if !stopCancel() {
			// The callback has started. Wait until it has closed the connection
			// before returnOrReleaseConn can inspect and potentially pool it.
			<-cancelFinished
		}
		results = append(results, window...)
		if statErr != nil {
			c.release(conn)
			return results, statErr
		}
		c.returnOrReleaseConn(conn, providerCfg)
	}
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
			releaseConnectionEntry(entry)
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

	// Stop the BatchStat worker pool last — its workers may be holding
	// connections we just force-closed, which makes them return with
	// errors and exit cleanly.
	c.repairPool.Stop()

	c.logger.Info().
		Int("total_closed", totalClosed).
		Msg("Connection manager closed")

	return nil
}

// findProvider resolves a provider by its canonical ID. A bare host is
// accepted as a fallback when exactly one provider uses that host; with
// two accounts on the same host it returns nil rather than guessing.
func (c *Client) findProvider(key string) *config.UsenetProvider {
	var hostMatch *config.UsenetProvider
	hostMatches := 0
	for i := range c.providers {
		if c.providers[i].ID() == key {
			return &c.providers[i]
		}
		if c.providers[i].Host == key {
			hostMatch = &c.providers[i]
			hostMatches++
		}
	}
	if hostMatches == 1 {
		return hostMatch
	}
	return nil
}

// SpeedTest runs a speed test for a specific provider, identified by
// config.UsenetProvider.ID(). A bare host is accepted for compatibility
// with older callers as long as only one provider uses that host.
func (c *Client) SpeedTest(ctx context.Context, providerID string, messageID string) SpeedTestResult {
	result := SpeedTestResult{
		Provider: providerID,
		TestedAt: utils.Now(),
	}

	targetProvider := c.findProvider(providerID)
	if targetProvider == nil {
		result.Error = "provider not found"
		c.speedTestResults.Store(providerID, result)
		return result
	}
	// Store under the canonical ID so Stats() finds it regardless of how
	// the caller named the provider.
	providerID = targetProvider.ID()
	result.Provider = providerID

	// Acquire through the pool so the test counts against the provider's
	// connection cap — a direct dial could exceed max_connections by one,
	// which strict providers answer with 502 for the whole account. A
	// healthy connection goes back to the pool warm when the test is done.
	conn, providerCfg, err := c.getConnectionFromProvider(ctx, WorkloadBackground, *targetProvider)
	if err != nil {
		result.Error = fmt.Sprintf("connection failed: %v", err)
		c.speedTestResults.Store(providerID, result)
		return result
	}

	// Measure latency using ping (true network RTT). time.Now, not the
	// cached clock: utils.Now lags up to 500ms, which would swamp the RTT.
	pingStart := time.Now()
	if err := conn.ping(c.pingTimeout); err != nil {
		c.release(conn)
		result.Error = fmt.Sprintf("ping failed: %v", err)
		c.speedTestResults.Store(providerID, result)
		return result
	}
	result.LatencyMs = time.Since(pingStart).Milliseconds()

	// If no messageID provided, just return latency
	if messageID == "" {
		c.put(conn, providerCfg)
		c.speedTestResults.Store(providerID, result)
		return result
	}

	// Download the segment to measure actual speed
	downloadStart := time.Now()
	data, err := conn.GetBody(messageID)
	downloadDuration := time.Since(downloadStart)

	if err != nil {
		c.release(conn)
		result.Error = fmt.Sprintf("download failed: %v", err)
		c.speedTestResults.Store(providerID, result)
		return result
	}
	c.put(conn, providerCfg)

	result.BytesRead = int64(len(data))

	// Calculate speed in MB/s
	if downloadDuration.Seconds() > 0 {
		result.SpeedMBps = float64(result.BytesRead) / downloadDuration.Seconds() / (1024 * 1024)
	}

	c.speedTestResults.Store(providerID, result)
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

// GetSpeedTestResult returns the speed test result for a specific provider,
// keyed by config.UsenetProvider.ID().
func (c *Client) GetSpeedTestResult(providerID string) (SpeedTestResult, bool) {
	return c.speedTestResults.Load(providerID)
}
