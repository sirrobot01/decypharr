package arr

import (
	"sync"
	"time"
)

// Queue cleanup runs on a timer, and every manual import makes the Arr probe
// each file in the download. On a network mount a stuck download would be
// rescanned on every pass, piling probes onto the mount. These cooldowns bound
// that: one manual import per instance at a time window, and each download at
// most once per longer window.
const (
	manualImportInstanceCooldown = 5 * time.Minute
	manualImportDownloadCooldown = 30 * time.Minute
)

// manualImportGuard rate-limits automatic manual imports. The zero value is
// ready to use.
type manualImportGuard struct {
	mu          sync.Mutex
	nextAttempt map[string]time.Time            // by instance
	lastAttempt map[string]map[string]time.Time // by instance, then download ID
}

// reserve reports whether downloadID on instance may be imported now. When it
// may not, it returns how long until it could be.
func (g *manualImportGuard) reserve(instance, downloadID string, now time.Time) (bool, time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.nextAttempt == nil {
		g.nextAttempt = make(map[string]time.Time)
		g.lastAttempt = make(map[string]map[string]time.Time)
	}
	if next := g.nextAttempt[instance]; now.Before(next) {
		return false, next.Sub(now)
	}
	attempts := g.lastAttempt[instance]
	if last, ok := attempts[downloadID]; ok {
		if ready := last.Add(manualImportDownloadCooldown); now.Before(ready) {
			return false, ready.Sub(now)
		}
	}

	if attempts == nil {
		attempts = make(map[string]time.Time)
		g.lastAttempt[instance] = attempts
	}
	for id, attempted := range attempts {
		if now.Sub(attempted) >= manualImportDownloadCooldown {
			delete(attempts, id)
		}
	}
	attempts[downloadID] = now
	g.nextAttempt[instance] = now.Add(manualImportInstanceCooldown)
	return true, 0
}
