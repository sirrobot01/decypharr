package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// staleNZBMinAge is the minimum age (by AddedOn or UpdatedAt, whichever is
// more recent) an NZB entry must have before it's even considered for
// staleness. A freshly-completed download is unreferenced by any Arr until
// the Arr actually imports it - without this guard, a good, in-flight grab
// would look identical to a genuinely abandoned one.
const staleNZBMinAge = 24 * time.Hour

// staleNZBCleanupRunID is a sentinel activeRunID, mirroring
// clearSupersededRunID: it holds the same singleton slot a repair sweep/fix/clear
// run does for the duration of a stale-NZB cleanup, since deletion reads and
// mutates storage a concurrent repair sweep could be probing/saving at the same
// moment. Nothing is persisted under this ID.
const staleNZBCleanupRunID = "stale-nzb-cleanup"

// StaleNZBBytes is a local-disk byte breakdown, reported in two categories
// per the feature's "local disk only" accounting: this never includes the
// entry's virtual/content size, since the actual article bytes live on the
// news servers, not on this host's disk.
type StaleNZBBytes struct {
	NZBMeta int64 `json:"nzbMeta"`
	Cache   int64 `json:"cache"`
	Total   int64 `json:"total"`
}

// StaleNZBEntry is one candidate in a stale-NZB preview: a real entry,
// still on record in storage, that no Arr references anymore. The common
// case is a quality upgrade or a PROPER/REPACK replacement - the Arr grabs
// a better release (a WEB-DL upgraded to a BluRay or REMUX, a PROPER
// replacing the original) and switches its symlink/import to the new
// download's files, leaving the old entry's NZB, metadata and cache behind
// with nothing pointing at it anymore.
type StaleNZBEntry struct {
	ID         string        `json:"id"` // infohash
	Name       string        `json:"name"`
	FileCount  int           `json:"fileCount"`
	AddedOn    time.Time     `json:"addedOn"`
	LocalBytes StaleNZBBytes `json:"localBytes"`
}

// StaleNZBOrphan is a group of on-disk .nzb/.meta files with no
// corresponding entry in storage at all - e.g. left behind by a crash, a
// manual entry deletion, or a past bug. Unlike StaleNZBEntry there's no
// InfoHash to classify against an Arr, no cache dir keyed to it, and no
// database record: deletion is just removing the files themselves.
type StaleNZBOrphan struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	AddedOn    time.Time     `json:"addedOn"` // most recent mtime among its files
	LocalBytes StaleNZBBytes `json:"localBytes"`
}

// StaleNZBAggregateBytes is a local-disk byte breakdown at the aggregate
// (whole-preview or whole-cleanup) level. Same three categories as
// StaleNZBBytes, but with the *Bytes-suffixed JSON field names used
// everywhere this feature reports a total rather than a single entry's
// figure - StaleNZBPreviewTotals and StaleNZBCleanupResult.Freed both embed
// this, so "totals" and "freed" always shape identically.
type StaleNZBAggregateBytes struct {
	NZBMetaBytes int64 `json:"nzbMetaBytes"`
	CacheBytes   int64 `json:"cacheBytes"`
	TotalBytes   int64 `json:"totalBytes"`
}

// staleNZBMajorityStaleThreshold is the fraction of everything scanned
// (every NZB entry plus every on-disk nzbs/meta file group) that Count would
// have to exceed for MajorityStale to flip on - a guardrail against a
// classification bug silently telling the user to delete most of their
// library, the exact failure mode a broken orphan-ID match produced before
// it was caught by inspection.
const staleNZBMajorityStaleThreshold = 0.5

// StaleNZBPreviewTotals summarizes a StaleNZBPreview.
type StaleNZBPreviewTotals struct {
	Count int `json:"count"`
	StaleNZBAggregateBytes
	ScannedTotal  int  `json:"scannedTotal"`  // every NZB entry + every on-disk file group considered, stale or not
	MajorityStale bool `json:"majorityStale"` // Count is more than half of ScannedTotal - review before deleting
}

// StaleNZBPreview is the read-only result of classifying every NZB entry,
// plus a second, independent category of on-disk files with no entry at all.
type StaleNZBPreview struct {
	Entries []StaleNZBEntry       `json:"entries"`
	Orphans []StaleNZBOrphan      `json:"orphans"`
	Totals  StaleNZBPreviewTotals `json:"totals"`
}

// StaleNZBSkip records why a requested entry was not deleted.
type StaleNZBSkip struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// StaleNZBCleanupResult is the outcome of a cleanup pass.
type StaleNZBCleanupResult struct {
	Deleted int                    `json:"deleted"`
	Skipped []StaleNZBSkip         `json:"skipped"`
	Failed  int                    `json:"failed"`
	Freed   StaleNZBAggregateBytes `json:"freed"`
}

// PreviewStaleNZBs classifies every NZB entry and returns the ones that
// currently qualify as stale, with the local disk each would free. Read-only:
// nothing is deleted or measured-and-discarded here beyond a stat/dir-walk.
//
// On an Arr reference-set failure (build error, or zero eligible Arrs),
// returns the error as-is and previews nothing - a partial/failed Arr lookup
// must never be treated as "nothing is referenced".
func (r *Repair) PreviewStaleNZBs(ctx context.Context) (StaleNZBPreview, error) {
	var preview StaleNZBPreview
	if ctx == nil {
		ctx = r.parentCtx
	}
	start := time.Now()
	defer r.clearStaleNZBProgress()

	phaseStart := time.Now()
	initialArrs := len(r.eligibleArrs(nil))
	r.setStaleNZBProgress("Fetching Arr references…", 0, int64(initialArrs), start)
	refs, err := r.buildArrReferencedSet(ctx, func(done, total int, name string) {
		r.setStaleNZBProgress(fmt.Sprintf("Fetching %s… (%d/%d)", name, done, total), int64(done), int64(total), start)
	})
	if err != nil {
		r.logger.Error().Err(err).Msg("StaleNZB: preview failed - could not build Arr reference set")
		return preview, fmt.Errorf("failed to build Arr reference set: %w", err)
	}
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("referenced_names", len(refs)).
		Msg("StaleNZB: preview phase - Arr reference set built")

	now := time.Now()

	phaseStart = time.Now()
	nzbEntries := r.collectNZBEntries()
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("scanned", len(nzbEntries)).
		Msg("StaleNZB: preview phase - entry scan complete")

	phaseStart = time.Now()
	r.setStaleNZBProgress("Checking entries", 0, int64(len(nzbEntries)), start)
	for i, entry := range nzbEntries {
		if i%staleNZBProgressStride == 0 {
			r.setStaleNZBProgress("Checking entries", int64(i), int64(len(nzbEntries)), start)
		}
		if !r.isStaleNZBCandidate(entry, refs, now) {
			continue
		}
		nzbMetaBytes := r.staleNZBFileBytes(entry.InfoHash)
		var cacheBytes int64
		if len(preview.Entries) < staleNZBCacheSparseLogLimit {
			cacheBytes = r.peekStaleNZBCacheBytesLogged(entry.GetFolder())
		} else {
			cacheBytes = r.peekStaleNZBCacheBytes(entry.GetFolder())
		}
		e := StaleNZBEntry{
			ID:        entry.InfoHash,
			Name:      entry.Name,
			FileCount: len(entry.Files),
			AddedOn:   entry.AddedOn,
			LocalBytes: StaleNZBBytes{
				NZBMeta: nzbMetaBytes,
				Cache:   cacheBytes,
				Total:   nzbMetaBytes + cacheBytes,
			},
		}
		preview.Entries = append(preview.Entries, e)
		preview.Totals.Count++
		preview.Totals.NZBMetaBytes += nzbMetaBytes
		preview.Totals.CacheBytes += cacheBytes
		preview.Totals.TotalBytes += e.LocalBytes.Total
	}
	r.setStaleNZBProgress("Checking entries", int64(len(nzbEntries)), int64(len(nzbEntries)), start)
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("stale_entries", len(preview.Entries)).
		Msg("StaleNZB: preview phase - entry classification complete")

	phaseStart = time.Now()
	r.setStaleNZBProgress("Scanning disk for orphans", 0, 0, start)
	candidates := r.collectOrphanNZBCandidates()
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("candidates", len(candidates)).
		Msg("StaleNZB: preview phase - orphan disk walk complete")

	phaseStart = time.Now()
	r.setStaleNZBProgress("Classifying orphans", 0, int64(len(candidates)), start)
	known := r.buildStaleNZBKnownKeys()
	for i, c := range candidates {
		if i%staleNZBProgressStride == 0 {
			r.setStaleNZBProgress("Classifying orphans", int64(i), int64(len(candidates)), start)
		}
		if !r.isStaleOrphanCandidate(c, now, known) {
			continue
		}
		var size int64
		for _, p := range c.Paths {
			size += statSize(p)
		}
		o := StaleNZBOrphan{
			ID:      c.ID,
			Name:    c.ID,
			AddedOn: c.ModTime,
			LocalBytes: StaleNZBBytes{
				NZBMeta: size,
				Total:   size,
			},
		}
		preview.Orphans = append(preview.Orphans, o)
		preview.Totals.Count++
		preview.Totals.NZBMetaBytes += size
		preview.Totals.TotalBytes += size
	}
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("known_hashes", len(known.hashes)).
		Int("known_names", len(known.names)).
		Int("orphans", len(preview.Orphans)).
		Msg("StaleNZB: preview phase - orphan classification complete")

	sort.Slice(preview.Entries, func(i, j int) bool {
		return preview.Entries[i].AddedOn.Before(preview.Entries[j].AddedOn)
	})
	sort.Slice(preview.Orphans, func(i, j int) bool {
		return preview.Orphans[i].AddedOn.Before(preview.Orphans[j].AddedOn)
	})

	preview.Totals.ScannedTotal = len(nzbEntries) + len(candidates)
	if preview.Totals.ScannedTotal > 0 && float64(preview.Totals.Count) > float64(preview.Totals.ScannedTotal)*staleNZBMajorityStaleThreshold {
		preview.Totals.MajorityStale = true
	}

	r.logger.Info().
		Dur("duration", time.Since(start)).
		Int("stale_entries", len(preview.Entries)).
		Int("orphans", len(preview.Orphans)).
		Int("scanned_total", preview.Totals.ScannedTotal).
		Int64("total_bytes", preview.Totals.TotalBytes).
		Bool("majority_stale", preview.Totals.MajorityStale).
		Msg("StaleNZB: preview completed")

	return preview, nil
}

// CleanupStaleNZBs deletes the entries in entryIDs and the orphan file
// groups in orphanIDs that are STILL stale/orphaned at the moment this runs.
// The caller's selection is a request, not an authorization: every ID is
// re-classified against a freshly-built reference set (or a freshly-scanned
// disk state, for orphans) before anything is touched, and anything that no
// longer qualifies - now referenced, too new, active, entry now exists - is
// skipped and reported with a reason instead of deleted.
//
// On an Arr reference-set failure, nothing is deleted and the error is
// returned as-is, exactly like PreviewStaleNZBs.
func (r *Repair) CleanupStaleNZBs(ctx context.Context, entryIDs []string, orphanIDs []string) (StaleNZBCleanupResult, error) {
	var result StaleNZBCleanupResult
	if ctx == nil {
		ctx = r.parentCtx
	}
	start := time.Now()
	defer r.clearStaleNZBProgress()
	if len(entryIDs) == 0 && len(orphanIDs) == 0 {
		err := errors.New("no entry or orphan ids provided")
		r.logger.Error().Err(err).Msg("StaleNZB: cleanup failed - empty request")
		return result, err
	}

	// Hold the same singleton slot a repair sweep/fix/clear run does for the
	// duration of this pass: it reads and deletes entries a concurrent repair sweep
	// could be probing/saving at the same moment.
	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		err := fmt.Errorf("repair already running (run %s)", id)
		r.logger.Error().Err(err).
			Int("requested_entries", len(entryIDs)).
			Int("requested_orphans", len(orphanIDs)).
			Msg("StaleNZB: cleanup failed - another run is active")
		return result, err
	}
	r.activeRunID = staleNZBCleanupRunID
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.activeRunID == staleNZBCleanupRunID {
			r.activeRunID = ""
		}
		r.mu.Unlock()
	}()

	r.logger.Debug().
		Int("requested_entries", len(entryIDs)).
		Int("requested_orphans", len(orphanIDs)).
		Msg("StaleNZB: cleanup started")

	phaseStart := time.Now()
	initialArrs := len(r.eligibleArrs(nil))
	r.setStaleNZBProgress("Fetching Arr references…", 0, int64(initialArrs), start)
	refs, err := r.buildArrReferencedSet(ctx, func(done, total int, name string) {
		r.setStaleNZBProgress(fmt.Sprintf("Fetching %s… (%d/%d)", name, done, total), int64(done), int64(total), start)
	})
	if err != nil {
		r.logger.Error().Err(err).
			Int("requested_entries", len(entryIDs)).
			Int("requested_orphans", len(orphanIDs)).
			Msg("StaleNZB: cleanup failed - could not build Arr reference set")
		return result, fmt.Errorf("failed to build Arr reference set: %w", err)
	}
	r.logger.Debug().
		Dur("duration", time.Since(phaseStart)).
		Int("referenced_names", len(refs)).
		Msg("StaleNZB: cleanup phase - Arr reference set built")

	// Every InfoHash currently backing any referenced slot anywhere in the
	// system - not just the requested IDs. Mirrors deleteSupersededEntry's
	// exclude-set: even if the classification above ever slipped, nothing in
	// this set is deleted, full stop.
	referencedHashes := make(map[string]struct{})
	for _, files := range refs {
		for _, hash := range files {
			if hash != "" {
				referencedHashes[hash] = struct{}{}
			}
		}
	}

	now := time.Now()
	seen := make(map[string]struct{}, len(entryIDs))
	r.setStaleNZBProgress("Deleting entries", 0, int64(len(entryIDs)), start)
	for i, rawID := range entryIDs {
		r.setStaleNZBProgress("Deleting entries", int64(i), int64(len(entryIDs)), start)
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}

		entry, err := r.manager.storage.Get(id)
		if err != nil || entry == nil {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Reason: "entry not found"})
			r.logger.Debug().Str("id", id).Msg("StaleNZB: cleanup skipped entry - not found")
			continue
		}
		if _, referenced := referencedHashes[id]; referenced {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Name: entry.Name, Reason: "now referenced by an Arr"})
			r.logger.Debug().Str("entry", entry.Name).Str("id", id).Msg("StaleNZB: cleanup skipped entry - now referenced by an Arr")
			continue
		}
		if reason := staleNZBSkipReason(entry, refs, now); reason != "" {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Name: entry.Name, Reason: reason})
			r.logger.Debug().Str("entry", entry.Name).Str("id", id).Str("reason", reason).Msg("StaleNZB: cleanup skipped entry")
			continue
		}

		freed, err := r.deleteStaleNZBEntry(entry, referencedHashes)
		if err != nil {
			r.logger.Error().Err(err).Str("entry", entry.Name).Str("id", id).Msg("StaleNZB: failed to delete entry")
			result.Failed++
			continue
		}
		result.Deleted++
		result.Freed.NZBMetaBytes += freed.NZBMeta
		result.Freed.CacheBytes += freed.Cache
		result.Freed.TotalBytes += freed.Total
		r.logger.Debug().
			Str("entry", entry.Name).
			Str("id", id).
			Int64("nzb_meta_bytes", freed.NZBMeta).
			Int64("cache_bytes", freed.Cache).
			Msg("StaleNZB: deleted stale entry")
	}
	r.setStaleNZBProgress("Deleting entries", int64(len(entryIDs)), int64(len(entryIDs)), start)

	// Orphans are re-scanned from disk, and known entry keys re-snapshotted,
	// once up front rather than per-ID: the classification below is a lookup
	// into these snapshots, not a re-walk or a storage round-trip per
	// requested ID, since either would be quadratic against a large library.
	orphanCandidates := make(map[string]staleNZBOrphanCandidate)
	var orphanKnown staleNZBKnownKeys
	if len(orphanIDs) > 0 {
		phaseStart = time.Now()
		for _, c := range r.collectOrphanNZBCandidates() {
			orphanCandidates[c.ID] = c
		}
		orphanKnown = r.buildStaleNZBKnownKeys()
		r.logger.Debug().
			Dur("duration", time.Since(phaseStart)).
			Int("candidates", len(orphanCandidates)).
			Int("known_hashes", len(orphanKnown.hashes)).
			Int("known_names", len(orphanKnown.names)).
			Msg("StaleNZB: cleanup phase - orphan disk snapshot built")
	}

	seenOrphans := make(map[string]struct{}, len(orphanIDs))
	r.setStaleNZBProgress("Deleting orphaned files", 0, int64(len(orphanIDs)), start)
	for i, rawID := range orphanIDs {
		r.setStaleNZBProgress("Deleting orphaned files", int64(i), int64(len(orphanIDs)), start)
		id := strings.TrimSpace(rawID)
		if id == "" {
			continue
		}
		if _, dup := seenOrphans[id]; dup {
			continue
		}
		seenOrphans[id] = struct{}{}

		c, ok := orphanCandidates[id]
		if !ok {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Reason: "orphan files no longer present"})
			r.logger.Debug().Str("id", id).Msg("StaleNZB: cleanup skipped orphan - files no longer present")
			continue
		}
		if orphanKnown.has(id) {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Reason: "entry now exists"})
			r.logger.Debug().Str("id", id).Msg("StaleNZB: cleanup skipped orphan - entry now exists")
			continue
		}
		if now.Sub(c.ModTime) < staleNZBMinAge {
			result.Skipped = append(result.Skipped, StaleNZBSkip{ID: id, Reason: "added or modified too recently"})
			r.logger.Debug().Str("id", id).Msg("StaleNZB: cleanup skipped orphan - added or modified too recently")
			continue
		}

		freed, err := r.deleteOrphanNZBFiles(c)
		if err != nil {
			r.logger.Error().Err(err).Str("id", id).Msg("StaleNZB: failed to delete orphan files")
			result.Failed++
			continue
		}
		result.Deleted++
		result.Freed.NZBMetaBytes += freed.NZBMeta
		result.Freed.TotalBytes += freed.Total
		r.logger.Debug().
			Str("id", id).
			Int64("bytes", freed.Total).
			Msg("StaleNZB: deleted orphan nzb/meta files")
	}

	r.logger.Info().
		Dur("duration", time.Since(start)).
		Int("deleted", result.Deleted).
		Int("skipped", len(result.Skipped)).
		Int("failed", result.Failed).
		Int64("freed_bytes", result.Freed.TotalBytes).
		Msg("StaleNZB: cleanup completed")
	return result, nil
}

// collectNZBEntries returns every entry whose protocol is nzb. Uses the
// metadata-only index for a cheap protocol filter before loading full
// records, so a mixed torrent+NZB library doesn't decode entries this
// feature will never consider.
func (r *Repair) collectNZBEntries() []*storage.Entry {
	var ids []string
	_ = r.manager.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		if meta.Protocol == string(config.ProtocolNZB) {
			ids = append(ids, meta.InfoHash)
		}
		return nil
	})

	out := make([]*storage.Entry, 0, len(ids))
	for _, id := range ids {
		entry, err := r.manager.storage.Get(id)
		if err != nil || entry == nil {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// isStaleNZBCandidate applies every classification rule except the
// referenced-hash exclude-set (that's checked separately by
// CleanupStaleNZBs, against the full-system reference set rather than just
// this entry's own name).
func (r *Repair) isStaleNZBCandidate(entry *storage.Entry, refs map[string]map[string]string, now time.Time) bool {
	return staleNZBSkipReason(entry, refs, now) == ""
}

// staleNZBSkipReason returns "" when entry currently qualifies as stale, or a
// human-readable reason it doesn't - shared by the preview filter and the
// cleanup pass's re-classification, so a skip reason shown to the user is
// always the literal reason the entry wasn't deleted.
//
// Deliberately does not check entry.IsComplete: its only job here would be
// guarding against an in-flight grab, and staleNZBMinAge plus IsDownloading
// already cover that. IsComplete was also, until the downloader persistence
// fix, never actually written back to this store on completion - relying on
// it here would keep excluding every entry that finished downloading before
// that fix shipped, forever.
func staleNZBSkipReason(entry *storage.Entry, refs map[string]map[string]string, now time.Time) string {
	if entry == nil {
		return "entry not found"
	}
	if entry.Protocol != config.ProtocolNZB {
		return "not an NZB entry"
	}
	if now.Sub(entry.AddedOn) < staleNZBMinAge || now.Sub(entry.UpdatedAt) < staleNZBMinAge {
		return "added or modified too recently"
	}
	if entry.IsDownloading {
		return "still downloading"
	}
	if entryReferencedByArr(entry, refs) {
		return "now referenced by an Arr"
	}
	return ""
}

// entryReferencedByArr reports whether any of entry's own files still backs
// a (Name, FileName) slot the Arr reference set shows as currently in use -
// i.e. whether THIS entry, by InfoHash, is still what an Arr's path resolves
// to for at least one of its files. This is the same InfoHash-aware
// referencedness classifySupersession computes from a BrokenFiles list,
// generalized to work from a raw storage.Entry's own Files map, since a
// stale-NZB candidate need not ever have been "broken" to be stale.
func entryReferencedByArr(entry *storage.Entry, refs map[string]map[string]string) bool {
	entryRefs := refs[entry.GetFolder()]
	if len(entryRefs) == 0 {
		return false
	}
	for name, file := range entry.Files {
		if file == nil || file.Deleted {
			continue
		}
		if hash, ok := entryRefs[name]; ok && hash == entry.InfoHash {
			return true
		}
	}
	return false
}

// deleteStaleNZBEntry removes one entry's database record, its .nzb/.processed
// /.failed and .meta files, and (when safe) its DFS cache directory, and
// reports the local disk bytes actually freed.
//
// exclude is the full-system referenced-hash set; this is a hard backstop,
// not just a courtesy - even if every classification check above it somehow
// slipped, this refuses to touch a hash any Arr still references.
func (r *Repair) deleteStaleNZBEntry(entry *storage.Entry, exclude map[string]struct{}) (StaleNZBBytes, error) {
	var freed StaleNZBBytes
	if _, keep := exclude[entry.InfoHash]; keep {
		return freed, fmt.Errorf("refusing to delete an entry still referenced by an Arr")
	}

	freed.NZBMeta = r.staleNZBFileBytes(entry.InfoHash)

	if r.manager.usenet != nil {
		if err := r.manager.usenet.Delete(entry.InfoHash); err != nil {
			return freed, fmt.Errorf("failed to delete nzb/meta files: %w", err)
		}
	}

	entryName := entry.GetFolder()
	// removePlacements=false: the usenet placement (the .nzb/meta files) was
	// already removed synchronously above so its size could be measured;
	// DeleteEntry's own async removal would just be redundant here.
	if err := r.manager.DeleteEntry(entry.InfoHash, false); err != nil {
		return freed, fmt.Errorf("failed to delete entry record: %w", err)
	}

	if cacheBytes, ok := r.removeStaleNZBCacheDir(entryName); ok {
		freed.Cache = cacheBytes
	}
	freed.Total = freed.NZBMeta + freed.Cache
	return freed, nil
}

// staleNZBFileBytes sums the on-disk size of an NZB entry's .nzb, .processed,
// .failed and .meta files - the same set usenet.Delete removes. Best-effort:
// a file that can't be stat'd (already gone, permissions) just contributes 0
// rather than failing the whole measurement.
func (r *Repair) staleNZBFileBytes(infohash string) int64 {
	if r.manager.usenet == nil {
		return 0
	}
	var total int64
	if nzb, err := r.manager.usenet.GetNZBHeader(infohash); err == nil && nzb != nil && nzb.Path != "" {
		total += statSize(nzb.Path)
		total += statSize(nzb.Path + ".processed")
		total += statSize(nzb.Path + ".failed")
	}
	total += statSize(r.manager.usenet.NZBStorage().MetaFilePath(infohash))
	return total
}

// peekStaleNZBCacheBytes measures (without removing) the DFS cache
// directory's actual disk usage for the preview's byte accounting. Shares
// every safety check with removeStaleNZBCacheDir except the actual removal.
func (r *Repair) peekStaleNZBCacheBytes(entryName string) int64 {
	path, ok := r.staleNZBCacheDirPath(entryName)
	if !ok {
		return 0
	}
	allocated, _, err := dirSize(path)
	if err != nil {
		return 0
	}
	return allocated
}

// staleNZBCacheSparseLogLimit caps how many entries per preview pass get the
// logical-vs-allocated DEBUG line below - enough to see the sparse-file
// effect in logs if the reported cache total ever looks wrong again,
// without a log line per entry on a large library.
const staleNZBCacheSparseLogLimit = 5

// peekStaleNZBCacheBytesLogged is peekStaleNZBCacheBytes plus a DEBUG line
// showing logical vs allocated bytes for this entry's cache dir - see
// staleNZBCacheSparseLogLimit for how many entries per pass get this.
func (r *Repair) peekStaleNZBCacheBytesLogged(entryName string) int64 {
	path, ok := r.staleNZBCacheDirPath(entryName)
	if !ok {
		return 0
	}
	allocated, logical, err := dirSize(path)
	if err != nil {
		return 0
	}
	r.logger.Debug().
		Str("entry", entryName).
		Int64("logical_bytes", logical).
		Int64("allocated_bytes", allocated).
		Msg("StaleNZB: cache dir size - logical (preallocated sparse length) vs allocated (actual disk usage)")
	return allocated
}

// removeStaleNZBCacheDir removes the DFS cache directory for entryName and
// returns its size, but only when it's safe to do so: DFS mode must be
// active with a configured cache dir, the resolved path must stay inside it,
// and - since the cache directory is keyed by folder Name rather than
// InfoHash - no other entry may still occupy that Name in the merged
// EntryItem view. That last check matters specifically for the
// same-release-name duplicate case: DeleteEntry's removeFromEntryItem only
// strips the just-deleted entry's own files out of the merged item, so if
// anything is left after that call, a healthy twin still needs this
// directory and it must not be touched.
//
// ok=false (0 bytes, nothing removed) covers every "not safe" and "nothing
// there" case alike - this must never be treated as a failure; not removing
// a directory is always safe, removing the wrong one is not.
func (r *Repair) removeStaleNZBCacheDir(entryName string) (int64, bool) {
	if item, err := r.manager.GetEntryItem(entryName); err == nil && item != nil {
		for _, f := range item.Files {
			if f != nil && !f.Deleted {
				r.logger.Debug().Str("entry", entryName).Msg("StaleNZB: cache dir still claimed by another entry sharing this name; leaving it")
				return 0, false
			}
		}
	}

	path, ok := r.staleNZBCacheDirPath(entryName)
	if !ok {
		return 0, false
	}

	allocated, _, err := dirSize(path)
	if err != nil {
		return 0, false
	}
	if err := os.RemoveAll(path); err != nil {
		r.logger.Warn().Err(err).Str("path", path).Msg("StaleNZB: failed to remove cache dir")
		return 0, false
	}
	return allocated, true
}

// staleNZBCacheDirPath resolves and validates the DFS cache directory for
// entryName. ok=false whenever DFS caching isn't in play at all (rclone
// mode, cache dir unset) or the resolved path would land outside the
// configured cache root - this is the path-safety guard: filepath.Join from
// the configured base, then verify containment before any caller acts on it.
func (r *Repair) staleNZBCacheDirPath(entryName string) (string, bool) {
	if entryName == "" {
		return "", false
	}
	cfg := config.Get()
	if cfg.Mount.Type != config.MountTypeDFS || cfg.Mount.DFS.CacheDir == "" {
		return "", false
	}

	base := filepath.Clean(cfg.Mount.DFS.CacheDir)
	resolved := filepath.Clean(filepath.Join(base, entryName))
	if resolved != base && !strings.HasPrefix(resolved, base+string(filepath.Separator)) {
		r.logger.Warn().Str("entry", entryName).Str("resolved", resolved).Msg("StaleNZB: refusing a cache path outside the configured cache dir")
		return "", false
	}

	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", false
	}
	return resolved, true
}

// staleNZBOrphanCandidate groups every on-disk file under the nzbs/meta
// directories that reduces to one derived ID, so a single orphan is
// reported (and deleted) as one unit even though it may span both
// directories (e.g. a leftover <id>.nzb alongside its <id>.meta).
type staleNZBOrphanCandidate struct {
	ID      string
	Paths   []string
	ModTime time.Time // most recent mtime among Paths
}

// staleNZBOrphanID derives the grouping key a nzbs/meta directory filename
// was saved under by stripping every marker/extension suffix decypharr's
// usenet subsystem appends, repeatedly, until none match. <id>.nzb,
// <id>.nzb.processed and <id>.meta all reduce to the same "<id>", so they're
// grouped as one candidate; a name that matches none of these (unexpected
// cruft) reduces to itself and is still grouped and considered on its own.
func staleNZBOrphanID(name string) string {
	suffixes := []string{".processed", ".failed", ".processing", ".importing", ".queued", ".nzb", ".meta"}
	for {
		stripped := false
		for _, suf := range suffixes {
			if strings.HasSuffix(name, suf) {
				name = strings.TrimSuffix(name, suf)
				stripped = true
				break
			}
		}
		if !stripped {
			return name
		}
	}
}

// collectOrphanNZBCandidates scans the nzbs and meta directories and groups
// every file by its derived ID. No entry-existence or age filtering happens
// here - see isStaleOrphanCandidate - this is purely a disk-state snapshot.
func (r *Repair) collectOrphanNZBCandidates() []staleNZBOrphanCandidate {
	if r.manager.usenet == nil {
		return nil
	}

	groups := make(map[string]*staleNZBOrphanCandidate)
	addFile := func(dir, name string) {
		id := staleNZBOrphanID(name)
		if id == "" {
			return
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			return
		}
		g, ok := groups[id]
		if !ok {
			g = &staleNZBOrphanCandidate{ID: id}
			groups[id] = g
		}
		g.Paths = append(g.Paths, path)
		if info.ModTime().After(g.ModTime) {
			g.ModTime = info.ModTime()
		}
	}

	if nzbsDir := r.manager.usenet.NZBsDir(); nzbsDir != "" {
		if entries, err := os.ReadDir(nzbsDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() {
					addFile(nzbsDir, e.Name())
				}
			}
		}
	}

	// Only .meta files are considered here - this naturally excludes the
	// meta dir's own housekeeping marker (the legacy-codec migration
	// sentinel), which doesn't carry that extension.
	if metaDir := r.manager.usenet.NZBStorage().MetaDir(); metaDir != "" {
		if entries, err := os.ReadDir(metaDir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".meta") {
					addFile(metaDir, e.Name())
				}
			}
		}
	}

	out := make([]staleNZBOrphanCandidate, 0, len(groups))
	for _, g := range groups {
		out = append(out, *g)
	}
	return out
}

// staleNZBKnownKeys is a one-time snapshot of every identifier a disk file's
// derived ID might legitimately resolve to. .nzb/.meta files are saved under
// the entry's InfoHash today, but decypharr saved them under the release
// name before a since-fixed refactor (long names plus a marker suffix could
// exceed ext4's 255-byte path-component limit) - a library with pre-refactor
// entries still has files named that way, so a derived ID must be checked
// against both to avoid flagging every one of them as ownerless. Built once
// per preview/cleanup pass, not once per candidate: a storage lookup per
// file is the difference between an instant preview and a multi-second one
// (or worse) on a library with tens of thousands of NZB files.
type staleNZBKnownKeys struct {
	hashes map[string]struct{} // every entry's InfoHash (raw rows, via ForEachMeta)
	names  map[string]struct{} // every entry-item Name (pkg/storage.GetEntryItems)
}

func (r *Repair) buildStaleNZBKnownKeys() staleNZBKnownKeys {
	hashes := make(map[string]struct{})
	_ = r.manager.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		hashes[meta.InfoHash] = struct{}{}
		return nil
	})
	return staleNZBKnownKeys{
		hashes: hashes,
		names:  r.manager.storage.GetEntryItems(),
	}
}

func (k staleNZBKnownKeys) has(id string) bool {
	if _, ok := k.hashes[id]; ok {
		return true
	}
	_, ok := k.names[id]
	return ok
}

// isStaleOrphanCandidate reports whether c currently qualifies as an orphan:
// old enough that it can't be a watch-dir file mid-import (same
// staleNZBMinAge guard as entries), and its derived ID resolves to nothing
// in known - neither an InfoHash nor a Name any entry currently claims.
func (r *Repair) isStaleOrphanCandidate(c staleNZBOrphanCandidate, now time.Time, known staleNZBKnownKeys) bool {
	if now.Sub(c.ModTime) < staleNZBMinAge {
		return false
	}
	return !known.has(c.ID)
}

// deleteOrphanNZBFiles removes every file in c.Paths and returns the bytes
// freed. Each path is re-verified to stay inside the nzbs or meta directory
// before removal - c.Paths was built from a directory scan of exactly those
// dirs, but this is re-checked anyway rather than trusted, mirroring
// staleNZBCacheDirPath's containment guard. A path that's already gone
// (os.IsNotExist) is treated as success, not a failure.
func (r *Repair) deleteOrphanNZBFiles(c staleNZBOrphanCandidate) (StaleNZBBytes, error) {
	var freed StaleNZBBytes
	if r.manager.usenet == nil {
		return freed, fmt.Errorf("usenet not configured")
	}

	bases := []string{
		filepath.Clean(r.manager.usenet.NZBsDir()),
		filepath.Clean(r.manager.usenet.NZBStorage().MetaDir()),
	}
	for _, p := range c.Paths {
		resolved := filepath.Clean(p)
		safe := false
		for _, base := range bases {
			if base != "" && strings.HasPrefix(resolved, base+string(filepath.Separator)) {
				safe = true
				break
			}
		}
		if !safe {
			r.logger.Warn().Str("path", resolved).Msg("StaleNZB: refusing to delete an orphan path outside the nzbs/meta dirs")
			continue
		}
		size := statSize(resolved)
		if err := os.Remove(resolved); err != nil && !os.IsNotExist(err) {
			return freed, fmt.Errorf("failed to remove %s: %w", resolved, err)
		}
		freed.NZBMeta += size
	}
	freed.Total = freed.NZBMeta
	return freed, nil
}

// statSize returns path's actual disk usage (fileDiskUsage), or 0 if it
// can't be stat'd (already gone, permissions) - never an error, since a
// missing file is treated as success (nothing left to account for)
// throughout this feature. Used for every local-bytes measurement this
// feature reports - nzb/meta/orphan files aren't sparse, so this barely
// differs from their logical size, but one measuring rule (allocated, not
// logical) beats two.
func statSize(path string) int64 {
	if path == "" {
		return 0
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return fileDiskUsage(info)
}

// dirSize walks root and sums both the actual disk usage (allocated) and
// the logical size of every regular file under it. Best-effort per-file: a
// file that vanishes mid-walk or can't be stat'd is skipped rather than
// aborting the whole measurement.
//
// Callers that only need the reclaimable figure want allocated - the DFS
// cache stores sparse files (preallocated to full logical length, only
// downloaded chunks actually written), so logical alone can wildly
// overstate what deleting a cache dir actually frees. logical is returned
// alongside it purely so a caller can log the gap when it's worth knowing
// about (see peekStaleNZBCacheBytesLogged).
func dirSize(root string) (allocated int64, logical int64, err error) {
	err = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		logical += info.Size()
		allocated += fileDiskUsage(info)
		return nil
	})
	return allocated, logical, err
}
