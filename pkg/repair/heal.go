package repair

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/appendstore"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

var (
	errReacquirerUnavailable = errors.New("arr reacquirer is unavailable")
	// errUnmappedBrokenFile marks a broken file the Arr service cannot act on
	// because it has no stable managed identity to bind to.
	errUnmappedBrokenFile = errors.New("broken file has no managed identity")
)

func (r *Service) repairBroken(ctx context.Context, run *storage.RepairRun, healths *xsync.Map[string, *storage.EntryHealth]) {
	var statsMu sync.Mutex
	healths.Range(func(_ string, health *storage.EntryHealth) bool {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		r.healBrokenEntry(ctx, run, &statsMu, health)
		return true
	})
}

func (r *Service) healBrokenEntry(ctx context.Context, run *storage.RepairRun, statsMu *sync.Mutex, health *storage.EntryHealth) {
	if health == nil || health.Status != storage.HealthBroken {
		return
	}

	initiated := 0
	failed := 0
	for _, broken := range health.BrokenFiles {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if broken.ArrName == "" || !r.reacquirable(broken) {
			continue
		}
		job, err := r.reacquireBrokenFile(ctx, broken)
		switch {
		case err == nil:
			initiated++
			r.logger.Info().
				Str("arr", broken.ArrName).
				Str("entry_id", broken.InfoHash).
				Str("file", broken.FileName).
				Str("job_id", job.ID).
				Msg("Repair: queued Arr reacquisition")
		default:
			failed++
			r.logger.Warn().Err(err).
				Str("arr", broken.ArrName).
				Str("entry_id", broken.InfoHash).
				Str("file", broken.FileName).
				Msg("Repair: failed to queue Arr reacquisition")
		}
	}

	if initiated == 0 && failed == 0 {
		return
	}
	health.LastRepairAt = time.Now()
	r.saveHealth(health)
	statsMu.Lock()
	run.Stats.Repaired += initiated
	run.Stats.RepairFailed += failed
	r.saveRun(run)
	statsMu.Unlock()
}

func (r *Service) arrInstance(name string) (arr.Arr, bool) {
	if r.arrs == nil {
		return arr.Arr{}, false
	}
	return r.arrs.Get(name)
}

// reacquirable reports whether the owning Arr can run a reacquisition. Only
// Sonarr and Radarr expose the file, history, and search APIs the Arr service
// drives; every other kind is left untouched instead of failing every sweep.
func (r *Service) reacquirable(broken storage.BrokenFile) bool {
	kind := broken.ArrKind
	if instance, ok := r.arrInstance(broken.ArrName); ok {
		kind = arrKindFromType(instance.Type)
	}
	return kind == "" || kind == storage.ArrKindSonarr || kind == storage.ArrKindRadarr
}

func (r *Service) reacquireBrokenFile(ctx context.Context, broken storage.BrokenFile) (*reacquire.Job, error) {
	if r.reacquirer == nil {
		return nil, errReacquirerUnavailable
	}
	entryID, fileID, err := r.resolveBrokenFile(broken)
	var job *reacquire.Job
	if err == nil {
		job, err = r.reacquirer.Reacquire(reacquire.Request{
			EntryID: entryID, FileID: fileID, Cause: reacquire.CauseRepair, Strategy: reacquire.StrategyHistoryFailed,
		})
	}
	if errors.Is(err, reacquire.ErrBindingNotFound) || errors.Is(err, errUnmappedBrokenFile) {
		job, err = r.reacquirer.ReacquireLibraryFile(ctx, reacquire.LibraryRequest{
			ArrName: broken.ArrName, ArrFileID: broken.ArrFileID, LibraryPath: broken.SourcePath,
			Cause: reacquire.CauseRepair,
		})
	}
	if err != nil {
		return nil, err
	}
	if job == nil || job.ID == "" {
		return nil, errors.New("arr reacquirer returned no job")
	}
	return job, nil
}

func (r *Service) resolveBrokenFile(broken storage.BrokenFile) (string, string, error) {
	if r.storage == nil {
		return "", "", errors.New("repair storage is unavailable")
	}
	if broken.InfoHash == "" {
		return "", "", fmt.Errorf("%w: no entry ID", errUnmappedBrokenFile)
	}
	if broken.FileName == "" {
		return "", "", fmt.Errorf("%w: no file name", errUnmappedBrokenFile)
	}
	entry, err := r.storage.Get(broken.InfoHash)
	if errors.Is(err, appendstore.ErrKeyNotFound) {
		return "", "", fmt.Errorf("%w: load entry %q: %w", errUnmappedBrokenFile, broken.InfoHash, err)
	}
	if err != nil {
		return "", "", fmt.Errorf("load broken entry: %w", err)
	}
	if entry == nil || entry.InfoHash != broken.InfoHash {
		return "", "", fmt.Errorf("%w: entry %q identity mismatch", reacquire.ErrBindingUnsafe, broken.InfoHash)
	}
	file, ok := entry.Files[broken.FileName]
	if !ok || file == nil {
		return "", "", fmt.Errorf("%w: file %q is not in entry %q", errUnmappedBrokenFile, broken.FileName, broken.InfoHash)
	}
	if file.Deleted {
		return "", "", fmt.Errorf("%w: file %q in entry %q is deleted", reacquire.ErrBindingUnsafe, broken.FileName, broken.InfoHash)
	}
	if file.InfoHash != "" && file.InfoHash != entry.InfoHash {
		return "", "", fmt.Errorf("%w: file %q belongs to entry %q, not %q", reacquire.ErrBindingUnsafe, broken.FileName, file.InfoHash, entry.InfoHash)
	}
	if file.ID == "" {
		return "", "", fmt.Errorf("%w: file %q in entry %q has no stable ID", errUnmappedBrokenFile, broken.FileName, broken.InfoHash)
	}
	return entry.InfoHash, file.ID, nil
}
