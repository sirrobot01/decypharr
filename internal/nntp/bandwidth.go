package nntp

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
)

// bandwidthFileName is the per-provider usage ledger, stored alongside
// config.json in the main data dir so quota counters survive restarts.
const bandwidthFileName = "usenet_bandwidth.json"

// providerQuota is the parsed quota rule for one provider.
type providerQuota struct {
	limitBytes   int64  // 0 = unlimited (usage is still tracked for display)
	reserveBytes int64  // tail of the quota held back for fills only
	period       string // "day" | "week" | "month"
	resetDay     int    // week: 0=Sun..6=Sat ; month: 1..31 ; unused for day
	resetHour    int    // 0..23, server local time
}

// softThreshold is limit minus reserve: usage below this lets the provider lead
// bulk; at or above it (but below limit) the provider is demoted to fills only.
func (q providerQuota) softThreshold() int64 {
	if q.limitBytes <= 0 {
		return 0
	}
	s := q.limitBytes - q.reserveBytes
	if s < 0 {
		return 0
	}
	return s
}

// QuotaTier is a provider's live serving state derived from usage vs quota.
type QuotaTier int

const (
	QuotaNormal  QuotaTier = iota // below the soft threshold — may lead bulk
	QuotaReserve                  // in the reserve band — fills only
	QuotaBlocked                  // at/over the hard cap — unused
)

// bwProvider is the live per-provider tracker. used is incremented on the
// socket read hot path via a plain atomic add; window rollover happens on the
// far less frequent check/snapshot path under mu.
type bwProvider struct {
	used          atomic.Int64
	periodStart   atomic.Int64 // unix nanos of current window start
	quota         providerQuota
	loggedBlock   atomic.Bool // one-shot: logged the hard-cap block this period?
	loggedReserve atomic.Bool // one-shot: logged entry into the reserve band this period?
	mu            sync.Mutex
}

// persistedProvider is the on-disk form.
type persistedProvider struct {
	BytesUsed   int64     `json:"bytes_used"`
	PeriodStart time.Time `json:"period_start"`
}

type persistedState struct {
	Providers map[string]persistedProvider `json:"providers"`
}

// ProviderBandwidthSnapshot is a read-only view for the stats page.
type ProviderBandwidthSnapshot struct {
	BytesUsed     int64
	QuotaBytes    int64
	ReserveBytes  int64
	SoftThreshold int64 // QuotaBytes - ReserveBytes; below this the provider leads bulk
	Period        string
	ResetAt       time.Time
	Exceeded      bool // at/over hard quota
	FillOnly      bool // in the reserve band: demoted to fills only
}

// BandwidthTracker meters per-provider downloaded bytes and enforces quotas.
// byHost is fixed for the client's lifetime, so lookups need no lock.
type BandwidthTracker struct {
	byHost map[string]*bwProvider
	path   string
	dirty  atomic.Bool
	stop   chan struct{}
	wg     sync.WaitGroup
	logger zerolog.Logger
}

func newBandwidthTracker(providers []config.UsenetProvider, log zerolog.Logger) *BandwidthTracker {
	bt := &BandwidthTracker{
		byHost: make(map[string]*bwProvider, len(providers)),
		path:   filepath.Join(config.GetMainPath(), bandwidthFileName),
		stop:   make(chan struct{}),
		logger: log,
	}
	for _, p := range providers {
		bt.byHost[p.Host] = &bwProvider{quota: parseProviderQuota(p)}
	}
	bt.load()

	// Initialize any provider without persisted state to the current window;
	// roll any loaded state forward if we've crossed a boundary while offline.
	now := time.Now()
	for _, bp := range bt.byHost {
		if bp.periodStart.Load() == 0 {
			bp.periodStart.Store(currentWindowStart(now, bp.quota).UnixNano())
		} else {
			bt.rollIfNeeded(bp, now)
		}
	}

	bt.wg.Add(1)
	go bt.saveLoop()
	return bt
}

func parseProviderQuota(p config.UsenetProvider) providerQuota {
	q := providerQuota{
		period:    normalizePeriod(p.QuotaPeriod),
		resetDay:  p.QuotaResetDay,
		resetHour: p.QuotaResetHour,
	}
	if p.Quota != "" {
		if n, err := config.ParseSize(p.Quota); err == nil && n > 0 {
			q.limitBytes = n
		}
	}
	// Reserve is the tail of the quota held back for fills only. Default to 10%
	// of the cap when unset, so the feature behaves sensibly without tuning and
	// doesn't confuse first-time users. Clamp to [0, limit].
	if q.limitBytes > 0 {
		if p.Reserve != "" {
			if n, err := config.ParseSize(p.Reserve); err == nil && n >= 0 {
				q.reserveBytes = n
			}
		} else {
			q.reserveBytes = q.limitBytes / 10
		}
		if q.reserveBytes > q.limitBytes {
			q.reserveBytes = q.limitBytes
		}
	}
	if q.resetHour < 0 || q.resetHour > 23 {
		q.resetHour = 0
	}
	switch q.period {
	case "week":
		q.resetDay = ((q.resetDay % 7) + 7) % 7
	case "month":
		if q.resetDay < 1 {
			q.resetDay = 1
		}
		if q.resetDay > 31 {
			q.resetDay = 31
		}
	}
	return q
}

func normalizePeriod(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "day", "daily":
		return "day"
	case "month", "monthly":
		return "month"
	default:
		return "week"
	}
}

// newCountingReader wraps a socket reader so every byte read from the wire is
// attributed to the given provider. Returns r unchanged if the host is unknown.
func (bt *BandwidthTracker) newCountingReader(r io.Reader, host string) io.Reader {
	bp := bt.byHost[host]
	if bp == nil {
		return r
	}
	return &countingReader{r: r, bp: bp, bt: bt}
}

type countingReader struct {
	r  io.Reader
	bp *bwProvider
	bt *BandwidthTracker
}

func (cr *countingReader) Read(p []byte) (int, error) {
	n, err := cr.r.Read(p)
	if n > 0 {
		cr.bp.used.Add(int64(n))
		// Avoid hammering the shared flag from every connection once it's set.
		if !cr.bt.dirty.Load() {
			cr.bt.dirty.Store(true)
		}
	}
	return n, err
}

// tierOf computes the provider's live quota tier and current usage. Rolls the
// window first. No logging side effects, so it is safe for the stats path.
func (bt *BandwidthTracker) tierOf(bp *bwProvider) (QuotaTier, int64) {
	if bp.quota.limitBytes <= 0 {
		return QuotaNormal, bp.used.Load()
	}
	bt.rollIfNeeded(bp, time.Now())
	used := bp.used.Load()
	switch {
	case used >= bp.quota.limitBytes:
		return QuotaBlocked, used
	case used >= bp.quota.softThreshold():
		return QuotaReserve, used
	default:
		return QuotaNormal, used
	}
}

// Tier returns the provider's live quota tier, logging once on each transition
// into the reserve band and into the hard-blocked state within a period.
func (bt *BandwidthTracker) Tier(host string) QuotaTier {
	bp := bt.byHost[host]
	if bp == nil {
		return QuotaNormal
	}
	t, used := bt.tierOf(bp)
	switch t {
	case QuotaBlocked:
		if !bp.loggedBlock.Swap(true) {
			bt.logger.Warn().
				Str("host", host).
				Int64("used", used).
				Int64("quota", bp.quota.limitBytes).
				Str("period", bp.quota.period).
				Msg("provider over hard bandwidth quota; excluded (fills included) until period resets")
		}
	case QuotaReserve:
		if !bp.loggedReserve.Swap(true) {
			bt.logger.Info().
				Str("host", host).
				Int64("used", used).
				Int64("soft", bp.quota.softThreshold()).
				Int64("quota", bp.quota.limitBytes).
				Msg("provider reached soft threshold; demoted to fills-only, reserve held back")
		}
	}
	return t
}

// Snapshot returns a read-only view for stats. Rolls the window first.
func (bt *BandwidthTracker) Snapshot(host string) (ProviderBandwidthSnapshot, bool) {
	bp := bt.byHost[host]
	if bp == nil {
		return ProviderBandwidthSnapshot{}, false
	}
	t, used := bt.tierOf(bp)
	s := ProviderBandwidthSnapshot{
		BytesUsed:    used,
		QuotaBytes:   bp.quota.limitBytes,
		ReserveBytes: bp.quota.reserveBytes,
		Period:       bp.quota.period,
		Exceeded:     t == QuotaBlocked,
		FillOnly:     t == QuotaReserve,
	}
	if bp.quota.limitBytes > 0 {
		s.SoftThreshold = bp.quota.softThreshold()
		s.ResetAt = nextReset(time.Now(), bp.quota)
	}
	return s, true
}

// rollIfNeeded resets the counter when the wall clock has moved into a new
// window. Deriving the window start directly from now handles arbitrary
// offline gaps in one step (no catch-up loop needed).
func (bt *BandwidthTracker) rollIfNeeded(bp *bwProvider, now time.Time) {
	ws := currentWindowStart(now, bp.quota).UnixNano()
	if bp.periodStart.Load() == ws {
		return
	}
	bp.mu.Lock()
	if bp.periodStart.Load() != ws {
		bp.periodStart.Store(ws)
		bp.used.Store(0)
		bp.loggedBlock.Store(false)
		bp.loggedReserve.Store(false)
		bt.dirty.Store(true)
	}
	bp.mu.Unlock()
}

// currentWindowStart returns the most recent reset boundary at or before now.
func currentWindowStart(now time.Time, q providerQuota) time.Time {
	loc := now.Location()
	h := q.resetHour
	switch q.period {
	case "day":
		s := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
		if s.After(now) {
			s = s.AddDate(0, 0, -1)
		}
		return s
	case "month":
		day := clampDay(now.Year(), now.Month(), q.resetDay)
		s := time.Date(now.Year(), now.Month(), day, h, 0, 0, 0, loc)
		if s.After(now) {
			pm := now.AddDate(0, -1, 0)
			day = clampDay(pm.Year(), pm.Month(), q.resetDay)
			s = time.Date(pm.Year(), pm.Month(), day, h, 0, 0, 0, loc)
		}
		return s
	default: // week
		s := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, loc)
		back := (int(s.Weekday()) - q.resetDay + 7) % 7
		s = s.AddDate(0, 0, -back)
		if s.After(now) {
			s = s.AddDate(0, 0, -7)
		}
		return s
	}
}

// nextReset returns the next boundary strictly after now, for display.
func nextReset(now time.Time, q providerQuota) time.Time {
	start := currentWindowStart(now, q)
	loc := start.Location()
	switch q.period {
	case "day":
		return start.AddDate(0, 0, 1)
	case "month":
		nm := start.AddDate(0, 1, 0)
		day := clampDay(nm.Year(), nm.Month(), q.resetDay)
		return time.Date(nm.Year(), nm.Month(), day, q.resetHour, 0, 0, 0, loc)
	default: // week
		return start.AddDate(0, 0, 7)
	}
}

// clampDay clamps a desired day-of-month to the last valid day of that month.
func clampDay(y int, m time.Month, want int) int {
	if want < 1 {
		want = 1
	}
	// Day 0 of the next month == last day of month m.
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if want > last {
		return last
	}
	return want
}

func (bt *BandwidthTracker) load() {
	data, err := os.ReadFile(bt.path)
	if err != nil {
		return
	}
	var st persistedState
	if err := json.Unmarshal(data, &st); err != nil {
		return
	}
	for host, pp := range st.Providers {
		if bp := bt.byHost[host]; bp != nil {
			bp.used.Store(pp.BytesUsed)
			if !pp.PeriodStart.IsZero() {
				bp.periodStart.Store(pp.PeriodStart.UnixNano())
			}
		}
	}
}

func (bt *BandwidthTracker) save() {
	st := persistedState{Providers: make(map[string]persistedProvider, len(bt.byHost))}
	for host, bp := range bt.byHost {
		st.Providers[host] = persistedProvider{
			BytesUsed:   bp.used.Load(),
			PeriodStart: time.Unix(0, bp.periodStart.Load()),
		}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := bt.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, bt.path)
}

func (bt *BandwidthTracker) saveLoop() {
	defer bt.wg.Done()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-bt.stop:
			bt.save()
			return
		case <-t.C:
			if bt.dirty.Swap(false) {
				bt.save()
			}
		}
	}
}

// Close flushes state and stops the background saver.
func (bt *BandwidthTracker) Close() {
	select {
	case <-bt.stop:
	default:
		close(bt.stop)
	}
	bt.wg.Wait()
}

// serveTier is a provider's effective role for the current request.
type serveTier int

const (
	tierLead    serveTier = iota // primary below its soft threshold — carries bulk
	tierFill                     // configured backup, or a primary in its reserve band — fills gaps only
	tierBlocked                  // at/over hard quota — never used
)

// providerTier maps a provider to its effective serving tier right now,
// combining its configured Backup flag with its live quota state.
func (c *Client) providerTier(p config.UsenetProvider) serveTier {
	t := QuotaNormal
	if c.bw != nil {
		t = c.bw.Tier(p.Host)
	}
	switch t {
	case QuotaBlocked:
		return tierBlocked
	case QuotaReserve:
		return tierFill // a capped primary is demoted to fills
	default:
		if p.Backup {
			return tierFill
		}
		return tierLead
	}
}

// tierLabel is the stats-API string for a serveTier: "primary" while a
// provider is leading bulk, "backup" once it's fills-only (reserve band) or
// blocked (over its hard cap) - from the UI's perspective both mean bulk
// sourcing has moved off it, so they're shown identically.
func tierLabel(t serveTier) string {
	if t == tierLead {
		return "primary"
	}
	return "backup"
}
