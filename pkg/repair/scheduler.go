package repair

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-co-op/gocron/v2"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Start reconciles repair state and registers configured schedules.
func (r *Service) Start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
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
			if _, err := r.runSweep(storage.RepairTriggerScheduled, RunOptions{}); err != nil {
				r.logger.Warn().Err(err).Msg("Scheduled repair sweep skipped")
			}
		}),
		gocron.WithTags(repairSchedulerTag),
	); err != nil {
		return fmt.Errorf("failed to register repair sweep: %w", err)
	}
	r.scheduled = true
	r.logger.Info().Str("schedule", cfg.Schedule).Msg("Repair sweep scheduled")

	r.scheduler.RemoveByTags(repairStopSchedulerTag)
	r.stopScheduled = false
	if stopSchedule := strings.TrimSpace(cfg.StopSchedule); stopSchedule != "" {
		stopJD, err := utils.ConvertToJobDef(stopSchedule)
		if err != nil {
			return fmt.Errorf("invalid repair stop schedule %q: %w", stopSchedule, err)
		}
		if _, err := r.scheduler.NewJob(stopJD,
			gocron.NewTask(func() {
				r.stopActiveRepairSweep()
			}),
			gocron.WithTags(repairStopSchedulerTag),
		); err != nil {
			return fmt.Errorf("failed to register repair stop schedule: %w", err)
		}
		r.stopScheduled = true
		r.logger.Info().Str("stop_schedule", stopSchedule).Msg("Repair sweep stop schedule registered")
	}
	return nil
}

// Stop cancels active repair work and removes its schedules.
func (r *Service) Stop() {
	r.mu.Lock()
	cancel := r.cancelRun
	r.cancelRun = nil
	r.activeRunID = ""
	r.activeStopFunc = nil
	if r.scheduled {
		r.scheduler.RemoveByTags(repairSchedulerTag)
		r.scheduled = false
	}
	if r.stopScheduled {
		r.scheduler.RemoveByTags(repairStopSchedulerTag)
		r.stopScheduled = false
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

// ApplyConfig restarts repair scheduling with the current configuration.
func (r *Service) ApplyConfig() error {
	r.Stop()
	r.mu.Lock()
	ctx := r.parentCtx
	r.mu.Unlock()
	return r.Start(ctx)
}

// RunNow starts a manual repair sweep.
func (r *Service) RunNow(opts RunOptions) (string, error) {
	return r.runSweep(storage.RepairTriggerManual, opts)
}

// ClearStates clears persisted health for the selected statuses.
func (r *Service) ClearStates(statuses []storage.HealthStatus) (ClearStateResult, error) {
	result := ClearStateResult{Statuses: statuses}
	if len(statuses) == 0 {
		return result, errors.New("at least one status is required")
	}

	r.mu.Lock()
	activeID := r.activeRunID
	r.mu.Unlock()
	if activeID != "" {
		return result, fmt.Errorf("repair already running (run %s)", activeID)
	}

	cleared, err := r.storage.ClearEntryHealthByStatuses(statuses)
	if err != nil {
		return result, err
	}
	result.Cleared = cleared
	return result, nil
}

// StopRun cancels the active repair sweep.
func (r *Service) StopRun() error {
	r.mu.Lock()
	cancel := r.cancelRun
	id := r.activeRunID
	r.mu.Unlock()
	if cancel == nil {
		return errors.New("no active repair run")
	}

	if id != "" {
		if run, err := r.storage.GetRepairRun(id); err == nil && run != nil && run.Status == storage.RepairRunRunning {
			run.Status = storage.RepairRunCancelled
			run.Stage = storage.RepairStageDone
			run.CancelReason = "stopped by user"
			run.CompletedAt = time.Now()
			if err := r.storage.SaveRepairRun(run); err != nil {
				r.logger.Warn().Err(err).Str("run_id", id).Msg("Stop: failed to persist optimistic cancel")
			}
		}
	}

	r.logger.Info().Str("run_id", id).Msg("Cancelling repair run")
	cancel()
	return nil
}

func (r *Service) stopActiveRepairSweep() {
	r.mu.Lock()
	cancel := r.cancelRun
	id := r.activeRunID
	stopFunc := r.activeStopFunc
	r.mu.Unlock()
	if cancel == nil {
		return
	}

	r.logger.Info().Str("run_id", id).Msg("Repair sweep stop schedule fired; stopping repair sweep")
	if stopFunc != nil {
		stopFunc()
	}
	cancel()
}

// Status returns the current repair state.
func (r *Service) Status() Status {
	cfg := r.cfg()
	st := Status{
		Enabled:      cfg.Enabled,
		HealthCounts: r.storage.CountEntryHealthByStatus(),
	}
	if next := r.nextScheduledRun(); next != nil {
		st.NextRunAt = next
	}

	r.mu.Lock()
	activeID := r.activeRunID
	r.mu.Unlock()
	if activeID != "" {
		if run, err := r.storage.GetRepairRun(activeID); err == nil {
			st.ActiveRun = run
		}
	}

	if runs, err := r.storage.ListRepairRuns(); err == nil {
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

func (r *Service) nextScheduledRun() *time.Time {
	r.mu.Lock()
	scheduled := r.scheduled
	r.mu.Unlock()
	if !scheduled {
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

func (r *Service) reconcileOrphans() {
	s := r.storage
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
