package manager

import "time"

// staleNZBProgressStride caps how often a scan-phase loop pays for an
// atomic store: every Nth item is enough for the modal to feel live without
// the bookkeeping mattering next to the actual work. Delete phases in
// CleanupStaleNZBs update every item instead - deletions are comparatively
// rare and slow (real I/O per item), so the per-item feedback is worth it
// and the stride wouldn't save anything meaningful anyway.
const staleNZBProgressStride = 100

// StaleNZBProgress is a live snapshot of an in-progress stale-NZB preview or
// cleanup pass, returned by GET /api/browse/stale-nzbs/progress. Total is 0
// while the true count isn't known yet (e.g. mid-disk-walk); the client
// treats that as "unknown" and shows a running count instead of a bar.
type StaleNZBProgress struct {
	Running   bool      `json:"running"`
	Phase     string    `json:"phase"`
	Done      int64     `json:"done"`
	Total     int64     `json:"total"`
	StartedAt time.Time `json:"startedAt"`
}

// setStaleNZBProgress publishes a new progress snapshot. Cheap enough to
// call from a tight loop at staleNZBProgressStride granularity - it's a
// struct allocation plus an atomic store, not a lock.
func (r *Repair) setStaleNZBProgress(phase string, done, total int64, startedAt time.Time) {
	r.staleNZBProgress.Store(&StaleNZBProgress{
		Running:   true,
		Phase:     phase,
		Done:      done,
		Total:     total,
		StartedAt: startedAt,
	})
}

// clearStaleNZBProgress marks no pass as running. Callers defer this
// immediately after starting a pass, so it fires on every exit path
// (success, error, panic) without needing to be repeated at each return.
func (r *Repair) clearStaleNZBProgress() {
	r.staleNZBProgress.Store(&StaleNZBProgress{Running: false})
}

// StaleNZBProgress returns the current progress snapshot for an in-flight
// preview or cleanup pass, or Running: false if neither is active.
func (r *Repair) StaleNZBProgress() StaleNZBProgress {
	p := r.staleNZBProgress.Load()
	if p == nil {
		return StaleNZBProgress{Running: false}
	}
	return *p
}
