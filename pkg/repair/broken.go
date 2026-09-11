package repair

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

func (r *Service) collectBrokenHealths(names []string, requireArrFile bool) (*xsync.Map[string, *storage.EntryHealth], int) {
	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			wanted[n] = struct{}{}
		}
	}

	healths := xsync.NewMap[string, *storage.EntryHealth]()
	_ = r.storage.ForEachEntryHealth(func(h *storage.EntryHealth) error {
		if h == nil || h.Status != storage.HealthBroken {
			return nil
		}
		if len(wanted) > 0 {
			if _, ok := wanted[h.EntryName]; !ok {
				return nil
			}
		}
		if requireArrFile {
			if len(h.BrokenFiles) == 0 {
				return nil
			}
			hasArrFile := false
			for _, bf := range h.BrokenFiles {
				if bf.ArrName != "" && bf.InfoHash != "" && bf.FileName != "" {
					hasArrFile = true
					break
				}
			}
			if !hasArrFile {
				return nil
			}
		}
		healths.Store(h.EntryName, h)
		return nil
	})
	return healths, len(wanted)
}

func (r *Service) markBrokenHealthCleared(h *storage.EntryHealth, at time.Time) {
	if h == nil {
		return
	}
	if _, err := r.storage.GetEntryItem(h.EntryName); err != nil {
		_ = r.storage.DeleteEntryHealth(h.EntryName)
		return
	}
	h.Status = storage.HealthUnknown
	h.BrokenFiles = nil
	h.FailureReason = ""
	h.LastRepairAt = at
	h.Dirty = false
	h.DirtyReason = ""
	h.NextCheckDueAt = time.Time{}
	r.saveHealth(h)
}

func isAlreadyClearedFileError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "file does not exist") ||
		strings.Contains(msg, "file is deleted")
}

// FixBroken repairs persisted broken entries without probing them again.
func (r *Service) FixBroken(ctx context.Context, names []string) (*storage.RepairRun, error) {
	if ctx == nil {
		ctx = r.parentCtx
	}

	healths, wantedCount := r.collectBrokenHealths(names, true)
	if healths.Size() == 0 {
		return nil, errors.New("no fixable broken entries")
	}

	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		return nil, fmt.Errorf("repair already running (run %s)", id)
	}
	runCtx, cancel := context.WithCancel(ctx)
	source := "fix-broken:all"
	if wantedCount > 0 {
		source = fmt.Sprintf("fix-broken:%d", wantedCount)
	}
	run := &storage.RepairRun{
		ID:        uuid.NewString(),
		Trigger:   storage.RepairTriggerManual,
		Status:    storage.RepairRunRunning,
		Stage:     storage.RepairStageRepairing,
		StartedAt: time.Now(),
		Source:    source,
	}
	run.Stats.Candidates = healths.Size()
	r.activeRunID = run.ID
	r.cancelRun = cancel
	r.mu.Unlock()

	if err := r.storage.SaveRepairRun(run); err != nil {
		r.mu.Lock()
		r.activeRunID = ""
		r.cancelRun = nil
		r.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("failed to persist repair run: %w", err)
	}

	r.runWG.Go(func() {
		defer func() {
			r.mu.Lock()
			if r.activeRunID == run.ID {
				r.activeRunID = ""
				r.cancelRun = nil
			}
			r.mu.Unlock()
			cancel()
		}()
		r.repairBroken(runCtx, run, healths)
		if runCtx.Err() != nil {
			r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during repair")
			return
		}
		r.finalizeRun(run, storage.RepairRunCompleted, "", "")
		r.logger.Info().
			Str("run_id", run.ID).
			Int("candidates", run.Stats.Candidates).
			Int("repaired", run.Stats.Repaired).
			Int("repair_failed", run.Stats.RepairFailed).
			Msg("FixBroken: completed")
	})
	return run, nil
}

// ClearBroken removes persisted broken files without calling Arr.
func (r *Service) ClearBroken(ctx context.Context, names []string) (*storage.RepairRun, error) {
	if ctx == nil {
		ctx = r.parentCtx
	}

	healths, wantedCount := r.collectBrokenHealths(names, false)
	if healths.Size() == 0 {
		return nil, errors.New("no broken files to clear")
	}

	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		return nil, fmt.Errorf("repair already running (run %s)", id)
	}
	runCtx, cancel := context.WithCancel(ctx)
	source := "clear-broken:all"
	if wantedCount > 0 {
		source = fmt.Sprintf("clear-broken:%d", wantedCount)
	}
	run := &storage.RepairRun{
		ID:        uuid.NewString(),
		Trigger:   storage.RepairTriggerManual,
		Status:    storage.RepairRunRunning,
		Stage:     storage.RepairStageRepairing,
		StartedAt: time.Now(),
		Source:    source,
	}
	run.Stats.Candidates = healths.Size()
	r.activeRunID = run.ID
	r.cancelRun = cancel
	r.mu.Unlock()

	if err := r.storage.SaveRepairRun(run); err != nil {
		r.mu.Lock()
		r.activeRunID = ""
		r.cancelRun = nil
		r.mu.Unlock()
		cancel()
		return nil, fmt.Errorf("failed to persist repair run: %w", err)
	}

	r.runWG.Go(func() {
		defer func() {
			r.mu.Lock()
			if r.activeRunID == run.ID {
				r.activeRunID = ""
				r.cancelRun = nil
			}
			r.mu.Unlock()
			cancel()
		}()
		r.clearBroken(runCtx, run, healths)
		if runCtx.Err() != nil {
			r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during clear")
			return
		}
		r.finalizeRun(run, storage.RepairRunCompleted, "", "")
		r.logger.Info().
			Str("run_id", run.ID).
			Int("candidates", run.Stats.Candidates).
			Int("cleared", run.Stats.Cleared).
			Int("clear_failed", run.Stats.RepairFailed).
			Msg("ClearBroken: completed")
	})
	return run, nil
}

func (r *Service) clearBroken(ctx context.Context, run *storage.RepairRun, healths *xsync.Map[string, *storage.EntryHealth]) {
	now := time.Now()
	healths.Range(func(name string, h *storage.EntryHealth) bool {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		if h == nil {
			return true
		}
		if len(h.BrokenFiles) == 0 {
			r.markBrokenHealthCleared(h, now)
			run.Stats.Cleared++
			r.saveRun(run)
			return true
		}

		remaining := make([]storage.BrokenFile, 0, len(h.BrokenFiles))
		for _, bf := range h.BrokenFiles {
			if err := r.backend.RemoveTorrentFile(bf.EntryName, bf.FileName); err != nil {
				if isAlreadyClearedFileError(err) {
					run.Stats.Cleared++
					r.saveRun(run)
					continue
				}
				r.logger.Warn().Err(err).Str("entry", bf.EntryName).Str("file", bf.FileName).Msg("ClearBroken: failed to remove broken file from mount")
				run.Stats.RepairFailed++
				remaining = append(remaining, bf)
				continue
			}
			run.Stats.Cleared++
			r.saveRun(run)
		}

		h.LastRepairAt = now
		h.BrokenFiles = remaining
		if len(remaining) == 0 {
			r.markBrokenHealthCleared(h, now)
			return true
		}

		h.Status = storage.HealthBroken
		h.FailureReason = topReason(remaining)
		r.saveHealth(h)
		return true
	})
}
