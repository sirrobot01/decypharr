package manager

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

// supersessionResult is the outcome of comparing one broken entry's
// BrokenFiles against the current Arr reference set.
type supersessionResult struct {
	// entryReferenced gates entry deletion: true when at least one of THIS
	// broken entry's own InfoHashes (from h.BrokenFiles) still backs some
	// file slot currently served under this Name - i.e. this entry's own
	// content, broken or not, is still in active use somewhere (a season
	// pack where only some episodes were individually re-grabbed). This is
	// deliberately InfoHash-aware, not just "is this Name referenced by
	// anything": a Name-only check would also come back true whenever a
	// healthy DUPLICATE entry (different InfoHash, same Name) has taken over
	// every file slot, permanently blocking cleanup of the broken one even
	// though none of ITS content is served anymore.
	entryReferenced bool
	// referencedHashes are every InfoHash currently backing any file slot
	// under this entry Name (the values of refs[EntryName]), independent of
	// which entry - broken or healthy - they belong to. Passed to
	// deleteSupersededEntry as a do-not-touch set so a "safe to delete"
	// verdict for the broken duplicate can never end up deleting a healthy
	// sibling duplicate instead (see deleteSupersededEntry for the residual
	// limitation this doesn't fully close).
	referencedHashes map[string]struct{}
	// stillBroken are the BrokenFiles that remain genuinely broken: the
	// current merged slot for that file name either isn't referenced by any
	// Arr at all, or is referenced but still backed by this same InfoHash.
	stillBroken []storage.BrokenFile
	// superseded are the BrokenFiles whose current merged slot is either
	// unreferenced, or referenced but now backed by a DIFFERENT InfoHash -
	// i.e. the Arr (or the storage-layer merge, for a same-name duplicate)
	// has already moved on to a working copy.
	superseded []storage.BrokenFile
}

// classifySupersession decides, per broken file, whether it's still needed
// or was replaced.
//
// Referencedness is judged purely from the reference set (which Arr paths
// resolve to which entry+file+InfoHash right now) - NOT from whether the
// BrokenFile row itself happens to carry ArrName/ArrFileID. Those fields are
// only ever populated when the specific probe pass that produced this
// BrokenFile had Arr context attached (an arr-source repair sweep, RecheckMedia, or
// RecheckEntry called with fix=true); a plain magnifier recheck (fix=false)
// or a managed-source repair sweep never attaches it, which previously meant such a
// BrokenFile could never be judged superseded no matter what the reference
// set said. A file whose current entry Name has no Arr reference at all is
// exactly as "superseded" whether or not this particular probe happened to
// capture Arr metadata for it.
//
// One consequence: a purely managed-source entry that no configured Arr has
// ever referenced now also reads as "superseded" (there is nothing to compare
// it against), and becomes eligible for clearing/deletion the same as a
// genuine Arr replacement would. buildArrReferencedSet refuses to run at all
// when there are zero eligible Arrs, to prevent that from cascading into
// clearing every broken entry on a pure-managed, no-Arr install; it does NOT
// protect a mixed install's manually-added, non-Arr-tracked downloads that
// coexist alongside Arr-managed ones - those are treated as superseded too.
func classifySupersession(h *storage.EntryHealth, refs map[string]map[string]string) supersessionResult {
	entryRefs := refs[h.EntryName]

	referencedHashes := make(map[string]struct{}, len(entryRefs))
	for _, hash := range entryRefs {
		if hash != "" {
			referencedHashes[hash] = struct{}{}
		}
	}

	brokenHashes := make(map[string]struct{})
	for _, bf := range h.BrokenFiles {
		if bf.InfoHash != "" {
			brokenHashes[bf.InfoHash] = struct{}{}
		}
	}
	entryReferenced := false
	for hash := range brokenHashes {
		if _, ok := referencedHashes[hash]; ok {
			entryReferenced = true
			break
		}
	}
	if len(brokenHashes) == 0 {
		// No InfoHash recorded on any broken file to check (shouldn't
		// normally happen - brokenFiles() always tries to set one). Fall
		// back to the coarser "is this Name referenced by anything at all"
		// so a data gap never reads as "safe to delete".
		entryReferenced = len(entryRefs) > 0
	}

	res := supersessionResult{entryReferenced: entryReferenced, referencedHashes: referencedHashes}
	for _, bf := range h.BrokenFiles {
		if fileSuperseded(refs, h.EntryName, bf.FileName, bf.InfoHash) {
			res.superseded = append(res.superseded, bf)
		} else {
			res.stillBroken = append(res.stillBroken, bf)
		}
	}
	return res
}

// fileSuperseded reports whether refs shows the (entryName, fileName) slot as
// already replaced: either nothing currently references it, or something
// does but the slot is backed by a different InfoHash than infoHash (a
// healthy duplicate has taken over it). This is the single source of truth
// for "is this specific file superseded," shared by classifySupersession
// (whole-entry, from a BrokenFile row) and filterSupersededFiles (per-file,
// during a probe pass) so the two can never disagree.
//
// infoHash == "" can only ever prove "unreferenced" - it can never disprove
// a match against a referenced slot's hash, so a referenced slot with no
// known InfoHash to compare against is never treated as superseded. Any
// ambiguity resolves to "not superseded."
func fileSuperseded(refs map[string]map[string]string, entryName, fileName, infoHash string) bool {
	hash, ok := refs[entryName][fileName]
	if !ok {
		return true
	}
	if infoHash == "" {
		return false
	}
	return hash != infoHash
}

// supersessionContext carries the per-run Arr reference set (nil when
// unavailable this run - build failed, zero eligible Arrs, or the caller
// never built one) down through the probe path alongside the existing
// candidate/opts threading, plus a shared counter for how many files this
// run skipped as already-superseded. Safe for concurrent use: probeEntry
// runs for many entries in parallel, each incrementing the same counter.
type supersessionContext struct {
	refs    map[string]map[string]string
	skipped atomic.Int64
}

// filterSupersededFiles drops any name from names whose (entry, file) slot
// the reference set shows as already superseded - unreferenced, or
// referenced but now backed by a different InfoHash (a healthy duplicate
// serves it instead). Applied once, before any probing of this entry's
// files, so a season pack's individually-replaced episodes are never
// re-probed (and never re-added to BrokenFiles) repair sweep after repair sweep: no probe
// happens for them at all, base STAT check or otherwise, and one DEBUG line
// is logged per skip.
//
// sc == nil or sc.refs == nil means no reference set was available this run
// - names is returned unfiltered. Never skip a file on missing information.
func (r *Repair) filterSupersededFiles(item *storage.EntryItem, names []string, sc *supersessionContext) []string {
	if sc == nil || sc.refs == nil || item == nil {
		return names
	}
	out := make([]string, 0, len(names))
	for _, name := range names {
		var infoHash string
		if file := item.Files[name]; file != nil {
			infoHash = file.InfoHash
		}
		if fileSuperseded(sc.refs, item.Name, name, infoHash) {
			sc.skipped.Add(1)
			r.logger.Debug().Str("entry", item.Name).Str("file", name).Msg("Repair: skipping superseded file")
			continue
		}
		out = append(out, name)
	}
	return out
}

// applySupersession persists the outcome of classifySupersession for one
// entry's health record. Returns cleared=true when the broken-list record
// was removed entirely, which happens whenever every remaining broken file
// turned out to be superseded (res.stillBroken is empty) - regardless of
// whether other, non-broken files in the same entry are still referenced.
//
// Deleting the underlying entry from decypharr (cleanupEntry) is strictly
// narrower than clearing the health record: it only ever happens when
// res.entryReferenced is also false, i.e. none of THIS entry's own InfoHashes
// are backing anything currently served under this Name. The season-pack
// case (some other file backed by the same InfoHash is still referenced)
// always survives with its broken list merely trimmed or cleared, never
// deleted.
func (r *Repair) applySupersession(h *storage.EntryHealth, res supersessionResult, cleanupEntry bool) (cleared bool, err error) {
	if len(res.superseded) == 0 {
		return false, nil
	}

	if len(res.stillBroken) == 0 {
		if err := r.manager.storage.DeleteEntryHealth(h.EntryName); err != nil {
			return false, err
		}
		r.logger.Info().Str("entry", h.EntryName).Int("superseded_files", len(res.superseded)).
			Msg("Repair: superseded by replacement; removed from broken list")

		if cleanupEntry && !res.entryReferenced {
			r.deleteSupersededEntry(h.EntryName, res.referencedHashes)
		}
		return true, nil
	}

	h.BrokenFiles = res.stillBroken
	h.FailureReason = topReason(h.BrokenFiles)
	if err := r.manager.storage.SaveEntryHealth(h); err != nil {
		return false, err
	}
	r.logger.Info().Str("entry", h.EntryName).
		Int("superseded_files", len(res.superseded)).
		Int("remaining_broken", len(h.BrokenFiles)).
		Msg("Repair: some broken files superseded by replacement; removed from broken list")
	return false, nil
}

// deleteSupersededEntry removes the underlying entry once none of its own
// InfoHashes are referenced by any Arr anymore. Mirrors the identifier + call
// pattern finalizeEntryRepair uses to delete a fully-broken entry after a
// successful re-search: DeleteEntry is keyed by infohash, and a single entry
// folder can span more than one (a merged candidate), so every distinct
// infohash across the entry's files is a deletion candidate - except any
// hash in exclude, which some file under this Name is still actively serving
// (a healthy duplicate) and must never be touched.
//
// KNOWN LIMITATION (not fixed here, tracked separately): this only ever sees
// InfoHashes still present in the CURRENT merged EntryItem returned by
// GetEntryItem, which is itself a per-filename "newest AddedOn wins" merge
// across every raw Entry row sharing this Name (storage.updateEntryItem).
// Once a duplicate's files have all been evicted from that merge by a newer
// sibling, its original Entry row becomes permanently unreachable from here
// and is never cleaned up - a pre-existing storage leak, not something this
// change introduces or resolves. The exclude set above only prevents this
// function from deleting the WRONG (still-live) entry; it cannot make it find
// the already-shadowed one.
func (r *Repair) deleteSupersededEntry(entryName string, exclude map[string]struct{}) {
	item, err := r.manager.GetEntryItem(entryName)
	if err != nil || item == nil {
		return
	}
	hashes := make(map[string]struct{})
	for _, f := range item.Files {
		if f == nil || f.InfoHash == "" {
			continue
		}
		if _, keep := exclude[f.InfoHash]; keep {
			continue
		}
		hashes[f.InfoHash] = struct{}{}
	}
	for hash := range hashes {
		if err := r.manager.DeleteEntry(hash, true); err != nil {
			r.logger.Warn().Err(err).Str("entry", entryName).Str("infohash", hash).Msg("Repair: failed to delete superseded entry")
			continue
		}
		r.logger.Info().Str("entry", entryName).Str("infohash", hash).Msg("Repair: deleted superseded entry")
	}
}

// buildArrReferencedSet maps entry-folder name -> file name -> the InfoHash
// currently backing that file slot, using the exact same GetMedia +
// symlink-target resolution enumerateArrCandidates already relies on
// (collectArrMediaCandidates / collectArrFiles), reading the InfoHash off the
// same *storage.EntryItem collectArrMediaCandidates already loads via
// GetEntryItem. This deliberately never invents a second path-resolution
// implementation that could silently disagree with the repair sweep's.
//
// The InfoHash (not just presence of the file name) is what lets
// classifySupersession tell apart "this Arr path resolves to a name/file this
// broken copy still owns" from "this Arr path resolves to the same name/file,
// but a healthy DUPLICATE entry has since taken over that slot" - the latter
// is the same-release-name duplicate case, where the Arr's reference never
// moves (same folder, same final filename) even though the specific broken
// copy behind it has already been superseded by a different InfoHash.
//
// It queries every eligible Arr regardless of cfg.Repair.Arrs scoping: "does
// any Arr still use this file" is independent of which Arrs a scheduled
// repair sweep happens to be scoped to check, and narrowing to that scope could
// make a file merely excluded from the repair sweep's Arr filter look superseded.
//
// Returns an error - never an empty-but-successful map - when there are zero
// eligible Arrs, on top of the existing per-Arr fetch/resolution failure
// case. Both must abort the whole build: since classifySupersession no longer
// requires a BrokenFile to carry ArrName/ArrFileID (see classifySupersession),
// an empty reference set now reads as "nothing about this entry is
// referenced", which for a real Arr outage - or an install with zero Arrs
// configured - would otherwise mean every broken entry in the system looks
// superseded at once. Callers must treat any error here as "couldn't
// determine anything" and leave the broken list untouched.
// onArrDone, if given, is called once per Arr as its fetch completes -
// done is the count of Arrs finished so far (including this one), total is
// len(arrs), and name is the Arr that just finished. Arrs are fetched
// concurrently, so this reports completion order, not a sequential "current
// Arr"; existing callers that don't need progress just omit it.
func (r *Repair) buildArrReferencedSet(ctx context.Context, onArrDone ...func(done, total int, name string)) (map[string]map[string]string, error) {
	arrs := r.eligibleArrs(nil)
	if len(arrs) == 0 {
		return nil, fmt.Errorf("no eligible arrs configured")
	}

	total := len(arrs)
	var done atomic.Int32
	notify := func(name string) {
		if len(onArrDone) == 0 {
			return
		}
		d := int(done.Add(1))
		for _, cb := range onArrDone {
			cb(d, total, name)
		}
	}

	out := make(map[string]map[string]string)
	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	for _, a := range arrs {
		g.Go(func() error {
			sub, err := r.collectArrMediaCandidates(gctx, a, "")
			if err != nil {
				return fmt.Errorf("arr %q: %w", a.Name, err)
			}
			mu.Lock()
			for name, c := range sub {
				files, ok := out[name]
				if !ok {
					files = make(map[string]string, len(c.contentMap))
					out[name] = files
				}
				for fileName := range c.contentMap {
					var infoHash string
					if c.item != nil {
						if f, ok := c.item.Files[fileName]; ok && f != nil {
							infoHash = f.InfoHash
						}
					}
					files[fileName] = infoHash
				}
			}
			mu.Unlock()
			notify(a.Name)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// excludeFilesFromItem returns a shallow copy of item with the named files
// removed from its Files map. Used to keep an already-superseded file out of
// a recheck's probe pass without mutating the item's stored representation.
func excludeFilesFromItem(item *storage.EntryItem, exclude map[string]struct{}) *storage.EntryItem {
	if item == nil || len(exclude) == 0 {
		return item
	}
	files := make(map[string]*storage.File, len(item.Files))
	for name, file := range item.Files {
		if _, skip := exclude[name]; skip {
			continue
		}
		files[name] = file
	}
	out := *item
	out.Files = files
	return &out
}

// dropSupersededCandidates removes managed-source repair sweep candidates whose
// existing broken health is fully superseded (no Arr references any of
// their files anymore), clearing their records instead of spending a probe
// re-confirming "broken" on a release the library already replaced.
// Candidates that are healthy, unchecked, or only partially superseded are
// left untouched here - the repair sweep's normal probe pass, with its own per-file
// supersession filter (filterSupersededFiles), handles the rest.
//
// refs is the reference set the caller already built once for this run (see
// executeSweep) - this function does no Arr round trip of its own. refs ==
// nil (build failed, or zero eligible Arrs) leaves candidates untouched
// entirely: the repair sweep proceeds exactly as it would without this check.
//
// Every broken candidate in the batch is considered, not just ones whose
// BrokenFiles happen to carry ArrName/ArrFileID: classifySupersession judges
// referencedness from the reference set alone (see its docstring), so a
// managed-source repair sweep's own broken files - which never carry Arr metadata,
// since managed candidates never resolve Arr context at all - are exactly as
// eligible as ones that do.
func (r *Repair) dropSupersededCandidates(in map[string]*candidate, refs map[string]map[string]string, log zerolog.Logger) map[string]*candidate {
	if refs == nil {
		return in
	}

	cleanup := r.cfg().CleanupSuperseded
	dropped := 0
	for name := range in {
		h, err := r.manager.storage.GetEntryHealth(name)
		if err != nil || h == nil || h.Status != storage.HealthBroken || len(h.BrokenFiles) == 0 {
			continue
		}
		res := classifySupersession(h, refs)
		if len(res.superseded) == 0 || len(res.stillBroken) > 0 {
			// Not superseded, or only partially - the normal probe pass
			// re-derives this entry's broken files from scratch either way.
			continue
		}
		cleared, aerr := r.applySupersession(h, res, cleanup)
		if aerr != nil {
			log.Warn().Err(aerr).Str("entry", name).Msg("Repair sweep: failed to apply supersession")
			continue
		}
		if cleared {
			delete(in, name)
			dropped++
		}
	}
	if dropped > 0 {
		log.Info().Int("dropped", dropped).Msg("Repair sweep: skipped fully-superseded candidates")
	}
	return in
}

// filterSupersededHealths drops or trims broken-entry candidates the Arrs no
// longer reference before a Fix pass acts on them, so "Fix" never blocklists
// or re-searches on behalf of a file the Arr already replaced. Mutates
// healths in place: fully-superseded entries are removed from the map,
// partially-superseded ones are left in with their BrokenFiles trimmed.
//
// On an Arr reference-set failure, healths is left untouched entirely - Fix
// then behaves exactly as it did before this check existed, rather than risk
// treating real breakage as replaced.
func (r *Repair) filterSupersededHealths(ctx context.Context, healths *xsync.Map[string, *storage.EntryHealth]) {
	refs, err := r.buildArrReferencedSet(ctx)
	if err != nil {
		r.logger.Warn().Err(err).Msg("Fix: failed to build Arr reference set for supersession check; fixing all candidates")
		return
	}

	cleanup := r.cfg().CleanupSuperseded
	toDelete := make([]string, 0)
	healths.Range(func(name string, h *storage.EntryHealth) bool {
		res := classifySupersession(h, refs)
		if len(res.superseded) == 0 {
			return true
		}
		cleared, aerr := r.applySupersession(h, res, cleanup)
		if aerr != nil {
			r.logger.Warn().Err(aerr).Str("entry", name).Msg("Fix: failed to apply supersession")
			return true
		}
		if cleared {
			toDelete = append(toDelete, name)
		}
		return true
	})
	for _, name := range toDelete {
		healths.Delete(name)
	}
}

// SupersededClearResult summarizes one run of ClearSuperseded for the API/UI.
type SupersededClearResult struct {
	Checked        int `json:"checked"`
	ClearedEntries int `json:"cleared_entries"`
	ClearedFiles   int `json:"cleared_files"`
	StillBroken    int `json:"still_broken"`
}

// clearSupersededRunID is a sentinel activeRunID used to hold the same
// singleton lock a repair sweep/fix/clear run does, for the duration of
// ClearSuperseded. It is not a real RepairRun: nothing is persisted under
// this ID, it exists only to keep a concurrent repair sweep from writing the same
// EntryHealth records this synchronous pass is reading and mutating.
const clearSupersededRunID = "clear-superseded"

// ClearSuperseded walks every currently-broken entry, checks whether the
// Arrs still reference its files, and clears whatever's already been
// replaced. This is the "Clear replaced" panel action: it runs supersession
// across the whole broken list in one pass, independent of any repair sweep.
//
// On an Arr reference-set failure, nothing is cleared and the error is
// returned as-is so the caller can surface it - a partial/failed Arr lookup
// must never be treated as "nothing is referenced".
func (r *Repair) ClearSuperseded(ctx context.Context) (SupersededClearResult, error) {
	var result SupersededClearResult
	if ctx == nil {
		ctx = r.parentCtx
	}

	// Hold the same singleton slot a repair sweep/fix/clear run does for the
	// duration of this pass: it reads and rewrites EntryHealth records a
	// concurrent repair sweep could be probing and saving at the same moment.
	r.mu.Lock()
	if r.activeRunID != "" {
		id := r.activeRunID
		r.mu.Unlock()
		return result, fmt.Errorf("repair already running (run %s)", id)
	}
	r.activeRunID = clearSupersededRunID
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.activeRunID == clearSupersededRunID {
			r.activeRunID = ""
		}
		r.mu.Unlock()
	}()

	refs, err := r.buildArrReferencedSet(ctx)
	if err != nil {
		return result, fmt.Errorf("failed to build Arr reference set: %w", err)
	}

	cleanup := r.cfg().CleanupSuperseded
	_ = r.manager.storage.ForEachEntryHealth(func(h *storage.EntryHealth) error {
		if h == nil || h.Status != storage.HealthBroken || len(h.BrokenFiles) == 0 {
			return nil
		}
		result.Checked++

		res := classifySupersession(h, refs)
		if len(res.superseded) == 0 {
			result.StillBroken++
			return nil
		}

		cleared, aerr := r.applySupersession(h, res, cleanup)
		if aerr != nil {
			r.logger.Warn().Err(aerr).Str("entry", h.EntryName).Msg("ClearSuperseded: failed to apply supersession")
			result.StillBroken++
			return nil
		}
		result.ClearedFiles += len(res.superseded)
		if cleared {
			result.ClearedEntries++
		} else {
			result.StillBroken++
		}
		return nil
	})

	r.logger.Info().
		Int("checked", result.Checked).
		Int("cleared_entries", result.ClearedEntries).
		Int("cleared_files", result.ClearedFiles).
		Int("still_broken", result.StillBroken).
		Msg("Repair: supersession sweep completed")
	return result, nil
}
