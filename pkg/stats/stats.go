package stats

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

// Collector owns the cached stats snapshot and the HTTP handler.
type Collector struct {
	mgr    *manager.Manager
	logger zerolog.Logger

	mu       sync.RWMutex
	snapshot *Snapshot

	// Cached debrid profiles with TTL
	profileMu      sync.RWMutex
	profileCache   map[string]*debridTypes.Profile
	profileFetched time.Time
	profileTTL     time.Duration

	cancel context.CancelFunc
}

// New creates a Collector and starts the background refresh goroutine.
func New(mgr *manager.Manager) *Collector {
	c := &Collector{
		mgr:          mgr,
		logger:       logger.New("stats"),
		profileCache: make(map[string]*debridTypes.Profile),
		profileTTL:   60 * time.Second,
	}
	// Build an initial snapshot synchronously so the first request is served immediately.
	c.snapshot = c.collect()
	return c
}

// Start begins the background refresh loop. Call from server startup.
func (c *Collector) Start(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	go c.loop(ctx)
}

// Stop cancels the background loop.
func (c *Collector) Stop() {
	if c.cancel != nil {
		c.cancel()
	}
}

// Snapshot returns the latest cached snapshot (zero-alloc per call).
func (c *Collector) Snapshot() *Snapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.snapshot
}

// Handler returns an http.HandlerFunc that serves the cached snapshot as JSON.
func (c *Collector) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		snap := c.Snapshot()
		utils.JSONResponse(w, snap, http.StatusOK)
	}
}

// loop refreshes the snapshot on a timer.
func (c *Collector) loop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snap := c.collect()
			c.mu.Lock()
			c.snapshot = snap
			c.mu.Unlock()
		}
	}
}

// collect builds a full Snapshot from all subsystems.
func (c *Collector) collect() *Snapshot {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	uptime := c.mgr.Uptime()
	startTime := c.mgr.StartTime()
	cfg := config.Get()

	snap := &Snapshot{}

	// --- System ---
	mb := func(b uint64) string { return fmt.Sprintf("%.2fMB", float64(b)/1024/1024) }
	snap.System = SystemStats{
		// Sys - HeapReleased is the heap actually held from the OS; HeapReleased
		// has been handed back (MADV_DONTNEED on Linux) so it does not count.
		MemoryUsed:     mb(memStats.Sys - memStats.HeapReleased),
		HeapAllocMB:    mb(memStats.HeapAlloc),
		HeapInuseMB:    mb(memStats.HeapInuse),
		HeapReleasedMB: mb(memStats.HeapReleased),
		SysMB:          mb(memStats.Sys),
		GCCycles:       memStats.NumGC,
		Goroutines:     runtime.NumGoroutine(),
		NumCPU:         runtime.NumCPU(),
		OS:             runtime.GOOS,
		Arch:           runtime.GOARCH,
		GoVersion:      runtime.Version(),
		UptimeSeconds:  int64(uptime.Seconds()),
		Uptime:         uptime.String(),
		StartTime:      startTime.Format("2006-01-02 15:04:05"),
	}

	// --- Debrids ---
	snap.Debrids = c.collectDebrids(cfg)

	// --- Mount ---
	snap.Mount = c.collectMount(cfg)

	// --- Usenet ---
	if c.mgr.HasUsenet() {
		snap.Usenet = c.mgr.UsenetStats()
	}

	// --- Active Streams ---
	streams := c.mgr.GetActiveStreams()
	snap.ActiveStreams = ActiveStreamStats{
		Count:   len(streams),
		Streams: streams,
	}

	// --- Storage ---
	torrentCount, err := c.mgr.GetTorrentsCount()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get torrents count")
	}
	snap.Storage = StorageStats{
		DBSize:       c.mgr.Storage().DiskSize(),
		TotalEntries: torrentCount,
	}

	// --- Queue ---
	if queue := c.mgr.JobQueue(); queue != nil {
		snap.Queue = QueueStats{
			Pending: queue.Len(),
			Active:  queue.ActiveCount(),
		}
	}

	// --- Arrs ---
	arrs := c.mgr.Arr().GetAll()
	arrNames := make([]string, 0, len(arrs))
	for _, a := range arrs {
		arrNames = append(arrNames, a.Name)
	}
	snap.Arrs = ArrStats{
		Count: len(arrs),
		Names: arrNames,
	}

	// --- Repair ---
	if svc := c.mgr.Repair(); svc != nil {
		st := svc.Status()
		health := make(map[string]int, len(st.HealthCounts))
		for k, v := range st.HealthCounts {
			health[string(k)] = v
		}
		snap.Repair = RepairStats{
			Enabled: st.Enabled,
			Active:  st.ActiveRun != nil,
			Health:  health,
		}
	}

	return snap
}

// collectDebrids gathers debrid stats with cached profiles.
func (c *Collector) collectDebrids(cfg *config.Config) []debridTypes.Stats {
	torrentCount, err := c.mgr.GetTorrentsCount()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get torrents count for debrid stats")
		torrentCount = 0
	}

	profiles := c.getProfiles()

	result := make([]debridTypes.Stats, 0)
	c.mgr.Clients().Range(func(debridName string, client debrid.Client) bool {
		if client == nil {
			return true
		}

		ds := debridTypes.Stats{}
		ls := debridTypes.LibraryStats{}

		profile := profiles[debridName]
		if profile == nil {
			profile = &debridTypes.Profile{Name: debridName}
		}
		profile.Name = debridName
		ds.Profile = profile

		ls.Total = torrentCount
		ls.ActiveLinks = c.mgr.GetTotalActiveDownloadLinks()
		ds.Library = ls
		ds.Accounts = client.AccountManager().Stats()

		if speedResult, ok := c.mgr.GetDebridSpeedTestResult(debridName); ok {
			ds.SpeedTestResult = &speedResult
		}

		result = append(result, ds)
		return true
	})

	// Order by config
	ordered := make([]debridTypes.Stats, 0, len(result))
	for _, debridCfg := range cfg.Debrids {
		for _, ds := range result {
			if ds.Profile.Name == debridCfg.Name {
				ordered = append(ordered, ds)
				break
			}
		}
	}
	return ordered
}

// getProfiles returns cached debrid profiles, refreshing if stale.
func (c *Collector) getProfiles() map[string]*debridTypes.Profile {
	c.profileMu.RLock()
	if time.Since(c.profileFetched) < c.profileTTL && len(c.profileCache) > 0 {
		defer c.profileMu.RUnlock()
		return c.profileCache
	}
	c.profileMu.RUnlock()

	// Fetch fresh profiles
	fresh := make(map[string]*debridTypes.Profile)
	c.mgr.Clients().Range(func(name string, client debrid.Client) bool {
		if client == nil {
			return true
		}
		profile, err := client.GetProfile()
		if err != nil {
			c.logger.Error().Err(err).Str("debrid", name).Msg("Failed to get debrid profile")
			// Use stale cache entry if available
			c.profileMu.RLock()
			if cached, ok := c.profileCache[name]; ok {
				fresh[name] = cached
			}
			c.profileMu.RUnlock()
			return true
		}
		fresh[name] = profile
		return true
	})

	c.profileMu.Lock()
	c.profileCache = fresh
	c.profileFetched = time.Now()
	c.profileMu.Unlock()

	return fresh
}

// collectMount gathers mount stats.
func (c *Collector) collectMount(cfg *config.Config) MountStats {
	mountMgr := c.mgr.MountManager()
	enabled := cfg.Mount.Type != config.MountTypeNone

	if mountMgr == nil || !mountMgr.IsReady() {
		return MountStats{
			Ready:   false,
			Enabled: enabled,
		}
	}

	mountStats := mountMgr.Stats()
	if mountStats == nil {
		return MountStats{
			Ready:   true,
			Enabled: enabled,
			Type:    mountMgr.Type(),
			Error:   "failed to get mount stats",
		}
	}

	return MountStats{
		Ready:   true,
		Enabled: enabled,
		Type:    mountMgr.Type(),
		Detail:  mountStats,
	}
}
