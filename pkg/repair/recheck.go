package repair

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// RecheckEntry starts a background probe for one entry.
func (r *Service) RecheckEntry(ctx context.Context, entryName string, fix bool) (*storage.EntryHealth, error) {
	if entryName == "" {
		return nil, errors.New("entry name is empty")
	}
	h, _ := r.storage.GetEntryHealth(entryName)
	if h != nil && h.ActiveRunID != "" {
		return nil, fmt.Errorf("entry is being probed by run %s", h.ActiveRunID)
	}

	item, err := r.storage.GetEntryItem(entryName)
	if err != nil || item == nil {
		return nil, fmt.Errorf("entry %q not found", entryName)
	}

	runID := "recheck-" + entryName
	c := &candidate{name: entryName, item: item}

	if ctx == nil {
		ctx = r.parentCtx
	}
	r.runWG.Go(func() {
		if fix {
			r.attachArrContext(ctx, c)
		}
		heal := newErrorCache()
		nzb := newNZBProber(r.usenet)
		final := r.probeEntry(ctx, runID, c, heal, nzb, RunOptions{}, fix)
		if final == nil {
			return
		}
		if !fix || final.Status != storage.HealthBroken {
			return
		}
		pseudo := &storage.RepairRun{ID: runID, Stats: storage.RepairRunStats{}}
		var statsMu sync.Mutex
		r.healBrokenEntry(ctx, pseudo, &statsMu, final)
	})

	if h == nil {
		h = &storage.EntryHealth{EntryName: entryName}
	}
	h.Status = storage.HealthRepairing
	h.ActiveRunID = runID
	return h, nil
}

// RecheckMedia starts a background probe for entries resolved through Arr.
func (r *Service) RecheckMedia(ctx context.Context, arrName, mediaID string, fix bool) (*storage.RepairRun, error) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return nil, errors.New("media_id is required")
	}
	if ctx == nil {
		ctx = r.parentCtx
	}

	arrs, err := r.resolveArrsForMedia(arrName)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		return nil, fmt.Errorf("repair already running (run %s)", id)
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &storage.RepairRun{
		ID:        uuid.NewString(),
		Trigger:   storage.RepairTriggerManual,
		Status:    storage.RepairRunRunning,
		Stage:     storage.RepairStageSelecting,
		StartedAt: time.Now(),
		Source:    fmt.Sprintf("media:%s/%s", arrName, mediaID),
	}
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
		r.executeRecheckMedia(runCtx, run, arrs, arrName, mediaID, fix)
	})
	return run, nil
}

func (r *Service) executeRecheckMedia(ctx context.Context, run *storage.RepairRun, arrs []arr.Arr, arrName, mediaID string, fix bool) {
	candidates := make(map[string]*candidate)
	var lastErr error
	for _, a := range arrs {
		if ctx.Err() != nil {
			break
		}
		sub, err := r.collectArrMediaCandidates(ctx, a, mediaID)
		if err != nil {
			lastErr = err
			r.logger.Trace().Err(err).Str("arr", a.Name).Str("media_id", mediaID).Msg("RecheckMedia: GetMedia failed")
			continue
		}
		mergeCandidates(candidates, sub)
		if arrName == "" && len(sub) > 0 {
			break
		}
	}

	if len(candidates) == 0 {
		msg := fmt.Sprintf("media id %q resolved no entries", mediaID)
		if lastErr != nil {
			msg += " (last error: " + lastErr.Error() + ")"
		}
		r.finalizeRun(run, storage.RepairRunCompleted, msg, "")
		return
	}

	run.Stats.Candidates = len(candidates)
	run.Stage = storage.RepairStageProbing
	r.saveRun(run)

	heal := newErrorCache()
	mediaNames := slices.Sorted(maps.Keys(candidates))
	err := r.probeAndHealCandidates(ctx, run, candidates, mediaNames, heal, RunOptions{}, fix)
	candidates = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during probing")
			return
		}
		r.finalizeRun(run, storage.RepairRunFailed, err.Error(), "")
		return
	}
	if ctx.Err() != nil {
		r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during repair")
		return
	}

	r.finalizeRun(run, storage.RepairRunCompleted, "", "")
	r.logger.Info().
		Str("run_id", run.ID).
		Str("arr", arrName).
		Str("media_id", mediaID).
		Int("candidates", run.Stats.Candidates).
		Int("broken", run.Stats.Broken).
		Int("repaired", run.Stats.Repaired).
		Bool("fix", fix).
		Msg("RecheckMedia: completed")
}

func (r *Service) resolveArrsForMedia(arrName string) ([]arr.Arr, error) {
	if arrName != "" {
		instance, ok := r.arrs.Get(arrName)
		if !ok {
			return nil, fmt.Errorf("arr %q not found", arrName)
		}
		if !instance.Reachable() {
			return nil, fmt.Errorf("arr %q is not configured", arrName)
		}
		if instance.SkipRepair {
			return nil, fmt.Errorf("arr %q has skip_repair set", arrName)
		}
		return []arr.Arr{instance}, nil
	}
	all := r.eligibleArrs(nil)
	if len(all) == 0 {
		return nil, errors.New("no eligible arrs configured")
	}
	return all, nil
}
