package repair

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
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
	unbound := make(map[string][]arr.ContentFile)
	for _, broken := range health.BrokenFiles {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if broken.ArrName == "" || !r.reacquirable(broken) {
			continue
		}
		job, err := r.reacquireBrokenFile(broken)
		switch {
		case err == nil:
			initiated++
			r.logger.Info().
				Str("arr", broken.ArrName).
				Str("entry_id", broken.InfoHash).
				Str("file", broken.FileName).
				Str("job_id", job.ID).
				Msg("Repair: queued Arr reacquisition")
		case unmappedBrokenFile(err):
			file, ok := legacyArrFile(broken)
			if !ok {
				failed++
				r.logger.Warn().Err(err).
					Str("arr", broken.ArrName).
					Str("entry_id", broken.InfoHash).
					Str("file", broken.FileName).
					Msg("Repair: broken file has neither an Arr binding nor an Arr file ID")
				continue
			}
			unbound[broken.ArrName] = append(unbound[broken.ArrName], file)
		default:
			failed++
			r.logger.Warn().Err(err).
				Str("arr", broken.ArrName).
				Str("entry_id", broken.InfoHash).
				Str("file", broken.FileName).
				Msg("Repair: failed to queue Arr reacquisition")
		}
	}

	repaired := make(map[string]struct{}, len(unbound))
	for arrName, files := range unbound {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if r.repairArrFiles(ctx, arrName, files) {
			initiated += len(files)
			repaired[arrName] = struct{}{}
			continue
		}
		failed += len(files)
	}
	if len(repaired) > 0 {
		r.deleteRepairedEntry(health, repaired)
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

// unmappedBrokenFile reports an error that means the Arr service cannot act on
// this file, as opposed to one that means the attempt itself failed.
func unmappedBrokenFile(err error) bool {
	return errors.Is(err, reacquire.ErrBindingNotFound) ||
		errors.Is(err, reacquire.ErrBindingUnsafe) ||
		errors.Is(err, reacquire.ErrServiceNotStarted) ||
		errors.Is(err, errReacquirerUnavailable) ||
		errors.Is(err, errUnmappedBrokenFile)
}

func legacyArrFile(broken storage.BrokenFile) (arr.ContentFile, bool) {
	if broken.ArrFileID == 0 {
		return arr.ContentFile{}, false
	}
	return arr.ContentFile{
		Id:        broken.MediaID,
		EpisodeId: broken.EpisodeID,
		FileId:    broken.ArrFileID,
		Name:      broken.FileName,
		Path:      broken.SourcePath,
		Size:      broken.Size,
		IsBroken:  true,
	}, true
}

func arrMediaID(kind arr.Type, content arr.ContentFile) int {
	switch kind {
	case arr.Sonarr:
		return content.EpisodeId
	case arr.Radarr:
		return content.Id
	default:
		return 0
	}
}

// repairArrFiles is the pre-index repair path. It runs only for broken files
// the Arr binding index cannot resolve, so libraries the indexer cannot map
// still recover instead of silently staying broken.
// repairArrFiles is the pre-index repair path. It runs only for broken files
// the Arr binding index cannot resolve, so libraries the indexer cannot map
// still recover instead of silently staying broken.
func (r *Service) repairArrFiles(ctx context.Context, arrName string, files []arr.ContentFile) bool {
	instance, ok := r.arrInstance(arrName)
	if !ok {
		return false
	}
	historyIDs := make(map[int]struct{})
	needSearch := make([]arr.ContentFile, 0)
	for _, file := range files {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		mediaID := arrMediaID(instance.Type, file)
		if mediaID == 0 {
			needSearch = append(needSearch, file)
			continue
		}
		historyID, _, err := r.arrs.LatestGrabID(ctx, arrName, mediaID)
		if err != nil || historyID == 0 {
			needSearch = append(needSearch, file)
			continue
		}
		historyIDs[historyID] = struct{}{}
	}

	if err := r.arrs.DeleteFiles(ctx, arrName, files); err != nil {
		r.logger.Warn().Err(err).Str("arr", arrName).Msg("Repair: bulk delete failed")
		return false
	}
	for historyID := range historyIDs {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if err := r.arrs.FailHistory(ctx, arrName, historyID); err != nil {
			r.logger.Warn().Err(err).Str("arr", arrName).Int("history_id", historyID).Msg("Repair: blocklisting the grab failed")
		}
	}
	if len(needSearch) > 0 {
		if err := r.arrs.SearchMissing(ctx, arrName, needSearch); err != nil {
			r.logger.Warn().Err(err).Str("arr", arrName).Msg("Repair: re-search fallback failed")
		}
	}
	return true
}

// deleteRepairedEntry removes an entry whose every file was re-searched through
// the legacy path. The reacquisition service invalidates its own entries, so
// this covers only files it could not map.
func (r *Service) deleteRepairedEntry(health *storage.EntryHealth, repaired map[string]struct{}) {
	if r.backend == nil || health.BrokenCount == 0 || health.BrokenCount != health.FileCount {
		return
	}
	hashes := make(map[string]struct{})
	for _, broken := range health.BrokenFiles {
		if broken.ArrName == "" || broken.ArrFileID == 0 || broken.InfoHash == "" {
			return
		}
		if _, ok := repaired[broken.ArrName]; !ok {
			return
		}
		hashes[broken.InfoHash] = struct{}{}
	}
	for hash := range hashes {
		if err := r.backend.DeleteEntry(hash, true); err != nil {
			r.logger.Warn().Err(err).Str("entry", health.EntryName).Str("infohash", hash).Msg("Repair: failed to delete fully-broken entry after re-search")
			continue
		}
		r.logger.Info().Str("entry", health.EntryName).Str("infohash", hash).Msg("Repair: deleted fully-broken entry after re-search")
	}
}

func (r *Service) reacquireBrokenFile(broken storage.BrokenFile) (*reacquire.Job, error) {
	if r.reacquirer == nil {
		return nil, errReacquirerUnavailable
	}
	entryID, fileID, err := r.resolveBrokenFile(broken)
	if err != nil {
		return nil, err
	}
	job, err := r.reacquirer.Reacquire(reacquire.Request{
		EntryID:  entryID,
		FileID:   fileID,
		Cause:    reacquire.CauseRepair,
		Strategy: reacquire.StrategyHistoryFailed,
	})
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
	if err != nil {
		return "", "", fmt.Errorf("%w: load entry %q: %w", errUnmappedBrokenFile, broken.InfoHash, err)
	}
	if entry == nil || entry.InfoHash != broken.InfoHash {
		return "", "", fmt.Errorf("%w: entry %q identity mismatch", errUnmappedBrokenFile, broken.InfoHash)
	}
	file, ok := entry.Files[broken.FileName]
	if !ok || file == nil {
		return "", "", fmt.Errorf("%w: file %q is not in entry %q", errUnmappedBrokenFile, broken.FileName, broken.InfoHash)
	}
	if file.Deleted {
		return "", "", fmt.Errorf("%w: file %q in entry %q is deleted", errUnmappedBrokenFile, broken.FileName, broken.InfoHash)
	}
	if file.InfoHash != "" && file.InfoHash != entry.InfoHash {
		return "", "", fmt.Errorf("%w: file %q belongs to entry %q, not %q", errUnmappedBrokenFile, broken.FileName, file.InfoHash, entry.InfoHash)
	}
	if file.ID == "" {
		return "", "", fmt.Errorf("%w: file %q in entry %q has no stable ID", errUnmappedBrokenFile, broken.FileName, broken.InfoHash)
	}
	return entry.InfoHash, file.ID, nil
}
