// The repair service is the manager's health-checker. When enabled in config
// it registers a recurring sweep that probes only the entries that need
// probing (unhealthy, dirty, or stale) and persists per-entry health live
// during the run.
package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/notifications"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// RepairStatus is the snapshot returned by the /api/repair/status endpoint.
type RepairStatus struct {
	Enabled      bool                         `json:"enabled"`
	NextRunAt    *time.Time                   `json:"next_run_at,omitempty"`
	ActiveRun    *storage.RepairRun           `json:"active_run,omitempty"`
	LastRun      *storage.RepairRun           `json:"last_run,omitempty"`
	HealthCounts map[storage.HealthStatus]int `json:"health_counts"`
}

const (
	repairSchedulerTag    = "repair-sweep"
	repairDefaultWorkers  = 5
	repairDefaultRecheck  = 7 * 24 * time.Hour
	repairHistoryRetained = 100
	// At most this many files probed concurrently within a single entry. The
	// outer worker count comes from cfg.Repair.Workers.
	repairFilesPerEntry    = 2
	repairStopDrainTimeout = 30 * time.Second
)

// Repair is the health-check / auto-repair service. One instance per Manager.
type Repair struct {
	manager   *Manager
	scheduler gocron.Scheduler
	logger    zerolog.Logger

	mu          sync.Mutex
	parentCtx   context.Context
	activeRunID string
	cancelRun   context.CancelFunc
	scheduled   bool
	runWG       sync.WaitGroup
}

// NewRepair builds the repair service for the given manager. Call
// Repair.Start to register the recurring sweep with the scheduler.
func NewRepair(m *Manager) *Repair {
	return &Repair{
		manager:   m,
		scheduler: m.scheduler,
		logger:    logger.New("repair"),
		parentCtx: context.Background(),
	}
}

func (r *Repair) cfg() config.RepairConfig { return config.Get().Repair }

func (r *Repair) workers() int {
	if w := r.cfg().Workers; w > 0 {
		return w
	}
	return repairDefaultWorkers
}

func (r *Repair) recheckInterval() time.Duration {
	raw := r.cfg().RecheckInterval
	if raw == "" {
		return repairDefaultRecheck
	}
	d, err := utils.ParseDuration(raw)
	if err != nil || d <= 0 {
		return repairDefaultRecheck
	}
	return d
}

// Start registers the recurring sweep with the scheduler if repair is
// enabled. It also reconciles any orphaned state left by a previous process:
// runs marked running flip to cancelled; entries stuck on `repairing` revert
// to their previous status. Idempotent.
func (r *Repair) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentCtx = ctx

	r.reconcileOrphans()

	cfg := r.cfg()
	if !cfg.Enabled {
		r.logger.Info().Msg("Repair disabled in config")
		return nil
	}
	if strings.TrimSpace(cfg.Schedule) == "" {
		return fmt.Errorf("repair enabled but schedule is empty")
	}

	jd, err := utils.ConvertToJobDef(cfg.Schedule)
	if err != nil {
		return fmt.Errorf("invalid repair schedule %q: %w", cfg.Schedule, err)
	}

	r.scheduler.RemoveByTags(repairSchedulerTag)
	if _, err := r.scheduler.NewJob(jd,
		gocron.NewTask(func() {
			if _, err := r.runSweep(storage.RepairTriggerScheduled, false); err != nil {
				r.logger.Warn().Err(err).Msg("Scheduled repair sweep skipped")
			}
		}),
		gocron.WithTags(repairSchedulerTag),
	); err != nil {
		return fmt.Errorf("failed to register repair sweep: %w", err)
	}
	r.scheduled = true
	r.logger.Info().Str("schedule", cfg.Schedule).Msg("Repair sweep scheduled")
	return nil
}

// Stop cancels any running sweep and unregisters the scheduled job. It blocks
// until the sweep goroutine exits (bounded by repairStopDrainTimeout) so
// in-flight saves don't race with storage.Close.
func (r *Repair) Stop() {
	r.mu.Lock()
	cancel := r.cancelRun
	r.cancelRun = nil
	r.activeRunID = ""
	if r.scheduled {
		r.scheduler.RemoveByTags(repairSchedulerTag)
		r.scheduled = false
	}
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		r.runWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(repairStopDrainTimeout):
		r.logger.Warn().Dur("timeout", repairStopDrainTimeout).Msg("Repair: drain timed out")
	}
}

// ApplyConfig reconciles the scheduler with the latest repair config. Called
// after /api/repair/config is updated.
func (r *Repair) ApplyConfig() error {
	r.Stop()
	return r.Start(r.parentCtx)
}

// RunNow triggers a manual sweep. Returns the new run ID.
func (r *Repair) RunNow(ignoreLastChecked bool) (string, error) {
	return r.runSweep(storage.RepairTriggerManual, ignoreLastChecked)
}

// StopRun cancels the currently-active sweep, if any. The run record is also
// flipped to cancelled in storage immediately so the UI sees the stop on the
// next poll, even before the goroutine unwinds.
func (r *Repair) StopRun() error {
	r.mu.Lock()
	cancel := r.cancelRun
	id := r.activeRunID
	r.mu.Unlock()
	if cancel == nil {
		return errors.New("no active repair run")
	}

	if id != "" {
		if run, err := r.manager.storage.GetRepairRun(id); err == nil && run != nil && run.Status == storage.RepairRunRunning {
			run.Status = storage.RepairRunCancelled
			run.Stage = storage.RepairStageDone
			run.CancelReason = "stopped by user"
			run.CompletedAt = time.Now()
			if err := r.manager.storage.SaveRepairRun(run); err != nil {
				r.logger.Warn().Err(err).Str("run_id", id).Msg("Stop: failed to persist optimistic cancel")
			}
		}
	}

	r.logger.Info().Str("run_id", id).Msg("Cancelling repair run")
	cancel()
	return nil
}

// Status reports the current repair state for the API.
func (r *Repair) Status() RepairStatus {
	cfg := r.cfg()
	st := RepairStatus{
		Enabled:      cfg.Enabled,
		HealthCounts: r.manager.storage.CountEntryHealthByStatus(),
	}
	if next := r.nextScheduledRun(); next != nil {
		st.NextRunAt = next
	}

	r.mu.Lock()
	activeID := r.activeRunID
	r.mu.Unlock()
	if activeID != "" {
		if run, err := r.manager.storage.GetRepairRun(activeID); err == nil {
			st.ActiveRun = run
		}
	}

	if runs, err := r.manager.storage.ListRepairRuns(); err == nil {
		for _, run := range runs {
			if st.ActiveRun != nil && run.ID == st.ActiveRun.ID {
				continue
			}
			if run.Status == storage.RepairRunRunning {
				continue
			}
			st.LastRun = run
			break
		}
	}
	return st
}

func (r *Repair) nextScheduledRun() *time.Time {
	if !r.scheduled {
		return nil
	}
	for _, j := range r.scheduler.Jobs() {
		for _, tag := range j.Tags() {
			if tag != repairSchedulerTag {
				continue
			}
			if next, err := j.NextRun(); err == nil {
				return &next
			}
		}
	}
	return nil
}

// reconcileOrphans cleans up state left by a previous process that died
// mid-sweep. Called from Start under r.mu.
func (r *Repair) reconcileOrphans() {
	s := r.manager.storage
	if s == nil {
		return
	}

	if runs, err := s.ListRepairRuns(); err == nil {
		now := time.Now()
		n := 0
		for _, run := range runs {
			if run == nil || run.Status != storage.RepairRunRunning {
				continue
			}
			run.Status = storage.RepairRunCancelled
			run.Stage = storage.RepairStageDone
			run.CompletedAt = now
			run.CancelReason = "interrupted by restart"
			if err := s.SaveRepairRun(run); err != nil {
				r.logger.Warn().Err(err).Str("run_id", run.ID).Msg("Reconcile: failed to persist orphaned run")
				continue
			}
			n++
		}
		if n > 0 {
			r.logger.Info().Int("count", n).Msg("Reconciled orphaned repair runs")
		}
	}

	cleared := 0
	_ = s.ForEachEntryHealth(func(state *storage.EntryHealth) error {
		if state == nil || state.ActiveRunID == "" {
			return nil
		}
		if state.PreviousStatus != "" {
			state.Status = state.PreviousStatus
		} else {
			state.Status = storage.HealthUnknown
		}
		state.ActiveRunID = ""
		state.PreviousStatus = ""
		if err := s.SaveEntryHealth(state); err == nil {
			cleared++
		}
		return nil
	})
	if cleared > 0 {
		r.logger.Info().Int("count", cleared).Msg("Reverted entries stuck on 'repairing'")
	}
}

// runSweep is the entry-point shared by RunNow and the scheduled callback. It
// guards against concurrent runs, persists the run record, then dispatches.
func (r *Repair) runSweep(trigger storage.RepairRunTrigger, ignoreLastChecked bool) (string, error) {
	cfg := r.cfg()
	if !cfg.Enabled && trigger == storage.RepairTriggerScheduled {
		return "", errors.New("repair disabled")
	}

	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		return id, errors.New("repair already running")
	}

	runCtx, cancel := context.WithCancel(r.parentCtx)
	source := string(cfg.Source)
	if ignoreLastChecked {
		source += ":ignore-last-checked"
	}
	run := &storage.RepairRun{
		ID:        uuid.NewString(),
		Trigger:   trigger,
		Status:    storage.RepairRunRunning,
		Stage:     storage.RepairStageSelecting,
		StartedAt: time.Now(),
		Source:    source,
	}
	r.activeRunID = run.ID
	r.cancelRun = cancel
	r.mu.Unlock()

	if err := r.manager.storage.SaveRepairRun(run); err != nil {
		r.mu.Lock()
		r.activeRunID = ""
		r.cancelRun = nil
		r.mu.Unlock()
		cancel()
		return "", fmt.Errorf("failed to persist repair run: %w", err)
	}

	r.runWG.Add(1)
	go func() {
		defer r.runWG.Done()
		defer func() {
			r.mu.Lock()
			if r.activeRunID == run.ID {
				r.activeRunID = ""
				r.cancelRun = nil
			}
			r.mu.Unlock()
			cancel()
		}()
		r.executeSweep(runCtx, run, ignoreLastChecked)
	}()

	r.logger.Info().Str("run_id", run.ID).Str("trigger", string(trigger)).Msg("Repair sweep started")
	return run.ID, nil
}

func (r *Repair) finalizeRun(run *storage.RepairRun, status storage.RepairRunStatus, errStr, cancelReason string) {
	// A user-initiated cancel that already landed in storage must not be
	// clobbered by a sweep that completed successfully after Stop was pressed.
	if existing, err := r.manager.storage.GetRepairRun(run.ID); err == nil && existing != nil && existing.Status == storage.RepairRunCancelled {
		status = storage.RepairRunCancelled
		if cancelReason == "" {
			cancelReason = existing.CancelReason
		}
	}

	run.Status = status
	run.Stage = storage.RepairStageDone
	run.CompletedAt = time.Now()
	if errStr != "" {
		run.Error = errStr
	}
	if cancelReason != "" {
		run.CancelReason = cancelReason
	}
	if err := r.manager.storage.SaveRepairRun(run); err != nil {
		r.logger.Warn().Err(err).Str("run_id", run.ID).Msg("Failed to persist final run state")
	}
	_ = r.manager.storage.PruneRepairRuns(repairHistoryRetained)

	if r.cfg().NotifyOnComplete && r.manager.Notifications != nil {
		if event := notificationEventFor(status); event != "" {
			r.manager.Notifications.Notify(notifications.Event{
				Type:    event,
				Status:  string(status),
				Message: discordContextFor(run),
			})
		}
	}

	// Repair scans the full entry set and allocates aggressively (sonic JSON
	// decode, appendLog.ReadAt buffers). Hand the freed heap back to the OS
	// so RSS doesn't sit at the post-repair peak.
	debug.FreeOSMemory()
}

func notificationEventFor(status storage.RepairRunStatus) config.NotificationEvent {
	switch status {
	case storage.RepairRunCompleted:
		return config.EventRepairComplete
	case storage.RepairRunFailed:
		return config.EventRepairFailed
	case storage.RepairRunCancelled:
		return config.EventRepairCancelled
	}
	return ""
}

func discordContextFor(run *storage.RepairRun) string {
	const dateFmt = "2006-01-02 15:04:05"
	return fmt.Sprintf(
		"\n**Run**: %s\n**Trigger**: %s\n**Source**: %s\n**Status**: %s\n**Started**: %s\n**Completed**: %s\n**Probed**: %d (broken: %d, repaired: %d)\n",
		run.ID, run.Trigger, run.Source, run.Status,
		run.StartedAt.Format(dateFmt), run.CompletedAt.Format(dateFmt),
		run.Stats.Probed, run.Stats.Broken, run.Stats.Repaired,
	)
}

func (r *Repair) saveRun(run *storage.RepairRun) {
	if err := r.manager.storage.SaveRepairRun(run); err != nil {
		r.logger.Trace().Err(err).Str("run_id", run.ID).Msg("Failed to persist run progress")
	}
}

func (r *Repair) saveHealth(state *storage.EntryHealth) {
	if err := r.manager.storage.SaveEntryHealth(state); err != nil {
		r.logger.Trace().Err(err).Str("entry", state.EntryName).Msg("Failed to persist entry health")
	}
}

// ReinsertEntry attempts to fix a torrent by re-inserting it across debrids.
// Used by the link service and by the repair auto-heal pass.
func (m *Manager) ReinsertEntry(ctx context.Context, entry *storage.Entry) error {
	if m.fixer == nil {
		return fmt.Errorf("fixer not initialized")
	}
	res, err := m.fixer.FixTorrent(ctx, entry, false)
	if err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("failed to re-insert torrent")
	}
	return nil
}

// linkOf returns the resolvable link/id for a torrent file in its active
// provider placement, or "" when no link is available.
func linkOf(entry *storage.Entry, name string) string {
	pe := entry.GetActiveProvider()
	if pe == nil || pe.Files == nil {
		return ""
	}
	f, ok := pe.Files[name]
	if !ok || f == nil {
		return ""
	}
	return cmp.Or(f.Link, f.Id)
}
