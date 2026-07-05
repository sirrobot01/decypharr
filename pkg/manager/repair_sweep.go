package manager

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/puzpuzpuz/xsync/v4"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/singleflight"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// candidate is the unit of work for a sweep. One per entry-folder.
//
// item is loaded lazily: enumeration records only the entry name (cheap,
// index-only) so a sweep doesn't decode and hold every entry body at once.
// probeEntry populates item just before probing and the worker releases it
// straight after, bounding resident entry bodies to the in-flight worker count
// rather than the whole store.
type candidate struct {
	name       string
	item       *storage.EntryItem
	arrName    string
	arrKind    storage.ArrKind
	contentMap map[string]arr.ContentFile // file_name -> Arr metadata when source=arr
}

// healCache memoizes per-infohash auto-heal results within one sweep so
// duplicate torrent sightings don't trigger repeated re-inserts. A stored
// nil means "healed"; a non-nil error means "previously failed".
type healCache struct {
	sf      singleflight.Group
	results *xsync.Map[string, error] // infohash -> heal error (nil if healed)
}

func newHealCache() *healCache {
	return &healCache{results: xsync.NewMap[string, error]()}
}

// do runs fix at most once per infohash, deduplicating concurrent callers via
// singleflight and memoizing the result for subsequent calls.
func (c *healCache) do(infoHash string, fix func() error) error {
	if c == nil || infoHash == "" {
		return fix()
	}
	if v, ok := c.results.Load(infoHash); ok {
		return v
	}
	_, err, _ := c.sf.Do(infoHash, func() (any, error) {
		if v, ok := c.results.Load(infoHash); ok {
			return nil, v
		}
		err := fix()
		c.results.Store(infoHash, err)
		return nil, err
	})
	return err
}

// fileResult is the outcome of probing one file in an entry.
type fileResult struct {
	name     string
	infoHash string
	protocol config.Protocol
	healthy  bool
	broken   bool
	reason   string // populated only when broken or unknown
}

// executeSweep is the body of a sweep: enumerate, filter due, probe, repair.
func (r *Repair) executeSweep(ctx context.Context, run *storage.RepairRun, opts RepairRunOptions) {
	cfg := r.cfg()
	log := r.logger.With().Str("run_id", run.ID).Logger()

	log.Info().Str("source", string(cfg.Source)).Msg("Sweep: selecting candidates")
	candidates, err := r.enumerateCandidates(ctx, cfg)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during selection")
			return
		}
		log.Error().Err(err).Msg("Sweep: enumeration failed")
		r.finalizeRun(run, storage.RepairRunFailed, err.Error(), "")
		return
	}
	if ctx.Err() != nil {
		r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled after selection")
		return
	}

	due, skipped := r.filterDueCandidates(candidates, opts.IgnoreLastChecked)
	// The full candidate set is only needed to compute `due`; drop it now so the
	// EntryItems we filtered out don't pin memory for the whole probe pass.
	candidates = nil
	protocolScope := r.effectiveProtocolScope(opts)
	due = r.filterCandidatesByProtocol(due, protocolScope)
	run.Stats.Candidates = len(due)
	run.Stats.SkippedFresh = skipped

	// Resolve auto-repair once: when off, the sweep is a pure health check —
	// it probes and records broken state but attempts no debrid re-insert and
	// no Arr delete/re-search.
	autoRepair := cfg.AutoRepair
	if opts.AutoRepair != nil {
		autoRepair = *opts.AutoRepair
	}

	run.Stage = storage.RepairStageProbing
	r.saveRun(run)
	log.Info().Int("due", len(due)).Int("skipped_fresh", skipped).Str("protocol", protocolScope).Bool("auto_repair", autoRepair).Msg("Sweep: probing")

	heal := newHealCache()
	err = r.probeAndHealCandidates(ctx, run, due, heal, opts, autoRepair)
	due = nil
	if err != nil {
		if errors.Is(err, context.Canceled) {
			r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled during probing")
			return
		}
		log.Error().Err(err).Msg("Sweep: probing failed")
		r.finalizeRun(run, storage.RepairRunFailed, err.Error(), "")
		return
	}
	if ctx.Err() != nil {
		r.finalizeRun(run, storage.RepairRunCancelled, "", "context cancelled after probing")
		return
	}

	r.finalizeRun(run, storage.RepairRunCompleted, "", "")
	log.Info().
		Int("probed", run.Stats.Probed).
		Int("broken", run.Stats.Broken).
		Int("healthy", run.Stats.Healthy).
		Int("repaired", run.Stats.Repaired).
		Int("repair_failed", run.Stats.RepairFailed).
		Msg("Sweep: completed")
}

// probeAndHealCandidates fans out across candidates with cfg.Repair.Workers
// concurrency. Each entry then probes its own files internally with at most
// repairFilesPerEntry concurrency, so total file probes in flight = workers × 2.
//
// Healing is folded into the per-entry pass: probeEntry runs auto-heal (debrid
// re-insert) inline, and when an entry is still broken afterwards this kicks
// off the Arr delete/blocklist/re-search for that one entry — so there's no
// separate end-of-run repair pass holding every health in memory. All healing
// is gated on autoRepair.
func (r *Repair) probeAndHealCandidates(ctx context.Context, run *storage.RepairRun, candidates map[string]*candidate, heal *healCache, opts RepairRunOptions, autoRepair bool) error {
	// run.Stats has plain int fields, so a single mutex guards every mutation
	// and the saveRun that follows it.
	var runMu sync.Mutex

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(max(1, r.workers()))

	for name, c := range candidates {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}
			h := r.probeEntry(gctx, run.ID, c, heal, opts, autoRepair)
			if h == nil {
				// Entry vanished or had no files between enumeration and probe;
				// skip without counting. Release any loaded body.
				c.item = nil
				c.contentMap = nil
				return nil
			}

			// Still broken after the inline debrid re-insert: escalate to the
			// Arr delete + re-search for just this entry.
			if autoRepair && h.Status == storage.HealthBroken {
				r.healBrokenEntry(gctx, run, &runMu, name, h)
			}

			runMu.Lock()
			run.Stats.Probed++
			switch h.Status {
			case storage.HealthHealthy:
				run.Stats.Healthy++
			case storage.HealthBroken:
				run.Stats.Broken++
			case storage.HealthUnknown, storage.HealthUnsupported:
				run.Stats.Unknown++
			}
			r.saveRun(run)
			runMu.Unlock()

			// Release this entry's body so it can be collected immediately
			// rather than lingering until the run ends.
			c.item = nil
			c.contentMap = nil
			return nil
		})
	}
	return g.Wait()
}

// probeEntry probes one entry: marks it repairing, probes its files (≤2 in
// parallel), runs auto-heal on broken torrents (only when autoRepair is set),
// then persists final health.
func (r *Repair) probeEntry(ctx context.Context, runID string, c *candidate, heal *healCache, opts RepairRunOptions, autoRepair bool) *storage.EntryHealth {
	s := r.manager.storage
	// Lazily load the entry body. Enumeration only recorded the name, so the
	// store isn't fully decoded up front. A vanished or empty entry is a skip
	// (nil tells the worker not to count it).
	if c.item == nil {
		item, err := s.GetEntryItem(c.name)
		if err != nil || item == nil || len(item.Files) == 0 {
			return nil
		}
		c.item = item
	}

	h, _ := s.GetEntryHealth(c.name)
	if h == nil {
		h = &storage.EntryHealth{EntryName: c.name}
	}
	previous := h.Status

	// Live update: surface 'repairing' before we start the probes.
	h.PreviousStatus = previous
	h.Status = storage.HealthRepairing
	h.ActiveRunID = runID
	h.Protocol = ""
	r.saveHealth(h)

	names := orderedFilenames(c.item)
	results := r.probeFiles(ctx, c.item, names, opts)
	if autoRepair {
		r.autoHealResults(ctx, results, heal)
	}

	broken := r.brokenFiles(c, results)
	final := rollupStatus(results)

	h.Status = final
	h.FileCount = len(names)
	h.BrokenFiles = broken
	h.BrokenCount = len(broken)
	h.Fingerprint = storage.EntryItemRepairFingerprint(c.item)
	h.LastCheckedAt = time.Now()
	h.NextCheckDueAt = h.LastCheckedAt.Add(r.recheckInterval())
	h.Dirty = false
	h.DirtyReason = ""
	h.ActiveRunID = ""
	h.PreviousStatus = ""
	if proto := firstProtocol(results); proto != "" {
		h.Protocol = proto
	}
	switch final {
	case storage.HealthHealthy:
		h.LastOKAt = h.LastCheckedAt
		h.FailureReason = ""
	case storage.HealthBroken:
		h.LastFailedAt = h.LastCheckedAt
		h.FailureReason = topReason(broken)
	}

	r.saveHealth(h)
	return h
}

// probeFiles fans per-file probes inside a single entry, capped at
// repairFilesPerEntry concurrent workers.
func (r *Repair) probeFiles(ctx context.Context, item *storage.EntryItem, names []string, opts RepairRunOptions) []fileResult {
	results := make([]fileResult, len(names))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(repairFilesPerEntry)
	for i, name := range names {
		g.Go(func() error {
			if gctx.Err() != nil {
				results[i] = fileResult{name: name, reason: "context_cancelled"}
				return nil
			}
			results[i] = r.probeFile(gctx, item, name, opts)
			return nil
		})
	}
	_ = g.Wait()
	return results
}

// probeFile checks one file. NZB probes use usenet.CheckFile. Torrent probes
// use the provider CheckFile endpoint unless this run requests unrestrict-link
// probing.
func (r *Repair) probeFile(ctx context.Context, item *storage.EntryItem, name string, opts RepairRunOptions) fileResult {
	file := item.Files[name]
	res := fileResult{name: name}

	if file == nil || file.InfoHash == "" {
		res.reason = "missing_infohash"
		return res
	}
	res.infoHash = file.InfoHash

	entry, err := r.manager.GetEntry(file.InfoHash)
	if err != nil || entry == nil {
		res.reason = "entry_not_found"
		return res
	}
	res.protocol = entry.Protocol
	if !repairProtocolMatches(r.effectiveProtocolScope(opts), entry.Protocol) {
		res.reason = "protocol_skipped"
		return res
	}

	if entry.IsNZB() {
		return r.probeNZBFile(ctx, entry, name, res)
	}
	return r.probeTorrentFile(ctx, entry, file, name, res, opts)
}

func (r *Repair) probeNZBFile(ctx context.Context, entry *storage.Entry, name string, res fileResult) fileResult {
	if r.manager.usenet == nil {
		res.reason = "usenet_client_not_configured"
		return res
	}
	err := r.manager.usenet.CheckFile(ctx, entry.InfoHash, name)
	if err == nil {
		res.healthy = true
		return res
	}
	if errors.Is(err, customerror.UsenetSegmentMissingError) {
		res.broken = true
		res.reason = "usenet_segment_missing"
	} else {
		res.reason = "usenet_probe_error"
	}
	return res
}

func (r *Repair) probeTorrentFile(ctx context.Context, entry *storage.Entry, file *storage.File, name string, res fileResult, opts RepairRunOptions) fileResult {
	client := r.manager.ProviderClient(entry.ActiveProvider)
	if client == nil {
		res.reason = "provider_client_not_found"
		return res
	}
	if opts.UnrestrictLink {
		return r.probeTorrentFileByUnrestrict(entry, file, name, res, client)
	}
	if !client.SupportsCheck() {
		res.reason = "provider_check_unsupported"
		return res
	}
	link := linkOf(entry, name)
	if link == "" {
		res.broken = true
		res.reason = "missing_provider_link"
		return res
	}
	err := client.CheckFile(ctx, file.InfoHash, link)
	if err == nil {
		res.healthy = true
		return res
	}
	if errors.Is(err, customerror.HosterUnavailableError) {
		res.broken = true
		res.reason = "hoster_unavailable"
	} else {
		res.reason = "provider_probe_error"
	}
	return res
}

func (r *Repair) probeTorrentFileByUnrestrict(entry *storage.Entry, file *storage.File, name string, res fileResult, client debrid.Client) fileResult {
	placement := entry.GetActiveProvider()
	if placement == nil {
		res.reason = "placement_not_found"
		return res
	}
	placementFile := placement.Files[name]
	if placementFile == nil {
		res.reason = "placement_file_not_found"
		return res
	}
	if placementFile.Link == "" && placementFile.Id == "" {
		res.broken = true
		res.reason = "missing_provider_link"
		return res
	}

	debridFile := &debridTypes.File{
		Id:        placementFile.Id,
		Link:      placementFile.Link,
		Path:      placementFile.Path,
		Name:      file.Name,
		Size:      file.Size,
		ByteRange: file.ByteRange,
		Deleted:   file.Deleted,
	}
	downloadLink, err := client.GetDownloadLink(placement.ID, debridFile)
	if err == nil && !downloadLink.Empty() {
		res.healthy = true
		return res
	}
	if err == nil || errors.Is(err, debridTypes.EmptyDownloadLinkError) || errors.Is(err, customerror.HosterUnavailableError) {
		res.broken = true
		if errors.Is(err, customerror.HosterUnavailableError) {
			res.reason = "hoster_unavailable"
		} else {
			res.reason = "empty_download_link"
		}
		return res
	}
	res.reason = "unrestrict_link_error"
	return res
}

// autoHealResults walks broken torrent infohashes and tries one re-insert per
// infohash (singleflighted). On success, every file in that infohash group is
// marked healthy.
func (r *Repair) autoHealResults(ctx context.Context, results []fileResult, heal *healCache) {
	byHash := make(map[string][]int)
	for i, res := range results {
		if !res.broken || res.protocol != config.ProtocolTorrent || res.infoHash == "" {
			continue
		}
		byHash[res.infoHash] = append(byHash[res.infoHash], i)
	}
	if len(byHash) == 0 {
		return
	}
	for infoHash, indices := range byHash {
		entry, err := r.manager.GetEntry(infoHash)
		if err != nil || entry == nil {
			continue
		}
		err = heal.do(infoHash, func() error {
			return r.manager.ReinsertEntry(ctx, entry)
		})
		if err != nil {
			continue
		}
		for _, i := range indices {
			results[i].broken = false
			results[i].healthy = true
			results[i].reason = "repaired"
		}
	}
}

// brokenFiles flattens broken results into BrokenFile records, attaching Arr
// identifiers so the repair pass can delete + re-search.
func (r *Repair) brokenFiles(c *candidate, results []fileResult) []storage.BrokenFile {
	out := make([]storage.BrokenFile, 0)
	for _, res := range results {
		if !res.broken {
			continue
		}
		bf := storage.BrokenFile{
			EntryName: c.name,
			FileName:  res.name,
			InfoHash:  res.infoHash,
			Protocol:  res.protocol,
			Reason:    res.reason,
		}
		if file, ok := c.item.Files[res.name]; ok && file != nil {
			bf.Size = file.Size
			if bf.InfoHash == "" {
				bf.InfoHash = file.InfoHash
			}
		}
		if cf, ok := c.contentMap[res.name]; ok {
			bf.ArrName = c.arrName
			bf.ArrKind = c.arrKind
			bf.MediaID = cf.Id
			bf.EpisodeID = cf.EpisodeId
			bf.ArrFileID = cf.FileId
			bf.TargetPath = cf.TargetPath
			bf.SourcePath = cf.Path
			if bf.Size == 0 {
				bf.Size = cf.Size
			}
		}
		out = append(out, bf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].FileName < out[j].FileName })
	return out
}

// rollupStatus collapses per-file results into a single EntryHealth status.
// Any broken file fails the entry; otherwise healthy wins over unknown.
func rollupStatus(results []fileResult) storage.HealthStatus {
	if len(results) == 0 {
		return storage.HealthUnknown
	}
	hasBroken, hasHealthy := false, false
	for _, res := range results {
		if res.broken {
			hasBroken = true
		}
		if res.healthy {
			hasHealthy = true
		}
	}
	switch {
	case hasBroken:
		return storage.HealthBroken
	case hasHealthy:
		return storage.HealthHealthy
	default:
		return storage.HealthUnknown
	}
}

func firstProtocol(results []fileResult) config.Protocol {
	for _, res := range results {
		if res.protocol != "" {
			return res.protocol
		}
	}
	return ""
}

// repairBroken runs the Arr delete + re-search heal over a set of already-known
// broken entries without reprobing. Used by the batch entry-points (FixBroken,
// media/entry rechecks). The sweep itself heals inline per entry via
// healBrokenEntry and does not call this.
func (r *Repair) repairBroken(ctx context.Context, run *storage.RepairRun, healths *xsync.Map[string, *storage.EntryHealth]) {
	var statsMu sync.Mutex
	healths.Range(func(name string, h *storage.EntryHealth) bool {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		r.healBrokenEntry(ctx, run, &statsMu, name, h)
		return true
	})
}

// healBrokenEntry runs the Arr delete + blocklist + re-search for one broken
// entry, then deletes the entry when it's fully broken and every file was
// handled. It does not verify the outcome: SearchMissing/MarkHistoryFailed only
// queue a download in the Arr — the replacement lands minutes-to-hours later,
// so the next scheduled sweep is where verification happens. statsMu guards
// run.Stats across concurrent entries.
func (r *Repair) healBrokenEntry(ctx context.Context, run *storage.RepairRun, statsMu *sync.Mutex, name string, h *storage.EntryHealth) {
	if h == nil || h.Status != storage.HealthBroken {
		return
	}

	// An entry's broken files normally all belong to one Arr, but a merged
	// candidate can span more — group defensively.
	byArr := make(map[string][]arr.ContentFile)
	for _, bf := range h.BrokenFiles {
		if bf.ArrName == "" || bf.ArrFileID == 0 {
			continue
		}
		byArr[bf.ArrName] = append(byArr[bf.ArrName], arr.ContentFile{
			Id:        bf.MediaID,
			EpisodeId: bf.EpisodeID,
			FileId:    bf.ArrFileID,
			Name:      bf.FileName,
			Path:      bf.SourcePath,
			Size:      bf.Size,
			IsBroken:  true,
		})
	}
	if len(byArr) == 0 {
		return
	}

	succeeded := make(map[string]struct{}, len(byArr))
	for arrName, files := range byArr {
		if ctx != nil && ctx.Err() != nil {
			return
		}
		a := r.manager.arr.Get(arrName)
		if a == nil {
			continue
		}
		if r.repairArrFiles(ctx, run, statsMu, a, files) {
			succeeded[arrName] = struct{}{}
		}
	}

	r.finalizeEntryRepair(name, h, succeeded)
}

// repairArrFiles deletes the broken files in one Arr, blocklists their grabs,
// and re-searches anything without a grab record. Returns true when the delete
// succeeded (so the caller may consider the files handled). Concurrency is
// bounded by the sweep's worker count; Sonarr/Radarr handle that many in-flight
// API calls fine, and the actual search/grab work is paced by the Arr's own
// command queue regardless of how the calls arrive.
func (r *Repair) repairArrFiles(ctx context.Context, run *storage.RepairRun, statsMu *sync.Mutex, a *arr.Arr, files []arr.ContentFile) bool {
	// Look up the grab history per broken file. Files whose grab record exists
	// get blocklisted via MarkHistoryFailed (which Sonarr/Radarr auto-re-searches
	// when "Redownload Failed" is on — the default). Files with no grab record
	// (history trimmed, manual import) fall back to an explicit SearchMissing.
	//
	// HistoryIDs are deduped per arr — a season-pack grab covers multiple broken
	// files but only needs one history/failed POST.
	historyIDs := make(map[int]struct{})
	needSearch := make([]arr.ContentFile, 0)
	for _, f := range files {
		if ctx != nil && ctx.Err() != nil {
			return false
		}
		var mediaID int
		switch a.Type {
		case arr.Sonarr:
			mediaID = f.EpisodeId
		case arr.Radarr:
			mediaID = f.Id
		}
		if mediaID == 0 {
			needSearch = append(needSearch, f)
			continue
		}
		id, _, herr := a.FindGrabHistoryID(mediaID)
		if herr != nil || id == 0 {
			needSearch = append(needSearch, f)
			continue
		}
		historyIDs[id] = struct{}{}
	}

	// Clear the EpisodeFile/MovieFile rows first so the upcoming re-search isn't
	// rejected by upgrade-only quality logic.
	if err := a.DeleteFiles(ctx, files); err != nil {
		r.logger.Warn().Err(err).Str("arr", a.Name).Msg("Repair: DeleteFiles failed")
		statsMu.Lock()
		run.Stats.RepairFailed += len(files)
		r.saveRun(run)
		statsMu.Unlock()
		return false
	}

	// Blocklist each unique grab. Errors here are non-fatal: a missing blocklist
	// is bad but DeleteFiles already cleared the rows, so the fallback
	// SearchMissing below still has a chance to recover.
	for id := range historyIDs {
		if ctx != nil && ctx.Err() != nil {
			break
		}
		if err := a.MarkHistoryFailed(id); err != nil {
			r.logger.Warn().Err(err).Str("arr", a.Name).Int("history_id", id).Msg("Repair: MarkHistoryFailed failed")
		}
	}

	// SearchMissing only for files without a grab record. With one,
	// MarkHistoryFailed's auto-re-search covers the same ground without creating
	// an extra command row.
	if len(needSearch) > 0 {
		if err := a.SearchMissing(ctx, needSearch); err != nil {
			r.logger.Warn().Err(err).Str("arr", a.Name).Msg("Repair: SearchMissing fallback failed")
		}
	}

	statsMu.Lock()
	run.Stats.Repaired += len(files)
	r.saveRun(run)
	statsMu.Unlock()
	return true
}

// finalizeEntryRepair stamps LastRepairAt and, when the entry is fully broken
// and every broken file was handled (Arr-deleted + re-searched), deletes it.
// Partial-broken entries are left in place so their healthy files survive.
func (r *Repair) finalizeEntryRepair(name string, h *storage.EntryHealth, succeeded map[string]struct{}) {
	now := time.Now()

	shouldDelete := h.BrokenCount > 0 && h.BrokenCount == h.FileCount
	hashes := make(map[string]struct{})
	if shouldDelete {
		for _, bf := range h.BrokenFiles {
			if bf.ArrName == "" || bf.ArrFileID == 0 {
				shouldDelete = false
				break
			}
			if _, ok := succeeded[bf.ArrName]; !ok {
				shouldDelete = false
				break
			}
			if bf.InfoHash != "" {
				hashes[bf.InfoHash] = struct{}{}
			}
		}
		if len(hashes) == 0 {
			shouldDelete = false
		}
	}

	if !shouldDelete {
		h.LastRepairAt = now
		r.saveHealth(h)
		return
	}

	for hash := range hashes {
		if err := r.manager.DeleteEntry(hash, true); err != nil {
			r.logger.Warn().Err(err).Str("entry", name).Str("infohash", hash).Msg("Repair: failed to delete fully-broken entry after re-search")
			continue
		}
		r.logger.Info().Str("entry", name).Str("infohash", hash).Msg("Repair: deleted fully-broken entry after re-search")
	}
}

// === Candidate enumeration ===

func (r *Repair) enumerateCandidates(ctx context.Context, cfg config.RepairConfig) (map[string]*candidate, error) {
	if cfg.Source == config.RepairSourceManaged {
		return r.enumerateManagedCandidates(ctx)
	}
	return r.enumerateArrCandidates(ctx, cfg)
}

func (r *Repair) filterCandidatesByProtocol(in map[string]*candidate, scope string) map[string]*candidate {
	if repairProtocolMatches(scope, config.ProtocolAll) {
		return in
	}
	out := make(map[string]*candidate, len(in))
	for name, c := range in {
		filtered := r.filterCandidateByProtocol(c, scope)
		if filtered != nil {
			out[name] = filtered
		}
	}
	return out
}

func (r *Repair) filterCandidateByProtocol(c *candidate, scope string) *candidate {
	if c == nil {
		return nil
	}
	// Restricted scope needs per-file protocols, so the body must be present.
	// For lazily-enumerated candidates load it here (only the due subset
	// reaches this point, so it doesn't reintroduce a whole-store decode).
	if c.item == nil {
		item, err := r.manager.GetEntryItem(c.name)
		if err != nil || item == nil {
			return nil
		}
		c.item = item
	}
	files := make(map[string]*storage.File, len(c.item.Files))
	for name, file := range c.item.Files {
		if file == nil || file.Deleted || file.InfoHash == "" {
			continue
		}
		entry, err := r.manager.GetEntry(file.InfoHash)
		if err != nil || entry == nil {
			continue
		}
		if repairProtocolMatches(scope, entry.Protocol) {
			files[name] = file
		}
	}
	if len(files) == 0 {
		return nil
	}

	item := *c.item
	item.Files = files
	filtered := *c
	filtered.item = &item
	if c.contentMap != nil {
		filtered.contentMap = make(map[string]arr.ContentFile, len(c.contentMap))
		for name, content := range c.contentMap {
			if _, ok := files[name]; ok {
				filtered.contentMap[name] = content
			}
		}
	}
	return &filtered
}

func (r *Repair) enumerateManagedCandidates(ctx context.Context) (map[string]*candidate, error) {
	// Names only: GetEntryItems walks the in-memory index without reading or
	// decoding any entry body. Bodies are loaded per-entry in probeEntry and
	// released by the worker, so the sweep never holds the whole store's worth
	// of decoded EntryItems in memory at once. Entries that turn out to be
	// empty are skipped when their body is loaded.
	out := make(map[string]*candidate)
	for name := range r.manager.storage.GetEntryItems() {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		out[name] = &candidate{name: name}
	}
	return out, nil
}

func (r *Repair) enumerateArrCandidates(ctx context.Context, cfg config.RepairConfig) (map[string]*candidate, error) {
	out := make(map[string]*candidate)
	var mu sync.Mutex

	arrs := r.eligibleArrs(cfg.Arrs)
	if len(arrs) == 0 {
		return out, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	for _, a := range arrs {
		g.Go(func() error {
			sub, err := r.collectArrMediaCandidates(gctx, a, "")
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				r.logger.Warn().Err(err).Str("arr", a.Name).Msg("Sweep: GetMedia failed; skipping arr")
				return nil
			}
			mu.Lock()
			mergeCandidates(out, sub)
			mu.Unlock()
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// collectArrMediaCandidates resolves an Arr's media (or a specific media-id
// within that Arr) to entry-keyed candidates.
func (r *Repair) collectArrMediaCandidates(ctx context.Context, a *arr.Arr, mediaID string) (map[string]*candidate, error) {
	out := make(map[string]*candidate)
	media, err := a.GetMedia(ctx, mediaID)
	if err != nil {
		return nil, err
	}
	kind := arrKindFromType(a.Type)
	for _, content := range media {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
		for entryPath, files := range collectArrFiles(content) {
			name := filepath.Clean(filepath.Base(entryPath))
			item, err := r.manager.GetEntryItem(name)
			if err != nil || item == nil {
				continue
			}
			c, ok := out[name]
			if !ok {
				c = &candidate{
					name:       name,
					item:       item,
					arrName:    a.Name,
					arrKind:    kind,
					contentMap: make(map[string]arr.ContentFile),
				}
				out[name] = c
			}
			if c.contentMap == nil {
				c.contentMap = make(map[string]arr.ContentFile)
			}
			for _, f := range files {
				f.EntryName = name
				f.IsSymlink = true
				c.contentMap[f.TargetPath] = f
			}
		}
	}
	return out, nil
}

func mergeCandidates(dst, src map[string]*candidate) {
	for name, c := range src {
		existing, ok := dst[name]
		if !ok {
			dst[name] = c
			continue
		}
		if existing.arrName == "" {
			existing.arrName = c.arrName
			existing.arrKind = c.arrKind
		}
		if existing.contentMap == nil {
			existing.contentMap = make(map[string]arr.ContentFile)
		}
		maps.Copy(existing.contentMap, c.contentMap)
	}
}

func (r *Repair) eligibleArrs(filter []string) []*arr.Arr {
	all := r.manager.arr.GetAll()
	wanted := make(map[string]struct{}, len(filter))
	for _, name := range filter {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = struct{}{}
		}
	}
	out := make([]*arr.Arr, 0, len(all))
	for _, a := range all {
		if a == nil || a.Host == "" || a.Token == "" || a.SkipRepair {
			continue
		}
		if len(wanted) > 0 {
			if _, ok := wanted[a.Name]; !ok {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func (r *Repair) filterDueCandidates(in map[string]*candidate, ignoreLastChecked bool) (map[string]*candidate, int) {
	if ignoreLastChecked {
		return in, 0
	}
	recheck := r.recheckInterval()
	now := time.Now()
	out := make(map[string]*candidate, len(in))
	skipped := 0
	for name, c := range in {
		h, _ := r.manager.storage.GetEntryHealth(name)
		if h != nil && !h.IsDue(now, recheck) {
			skipped++
			continue
		}
		out[name] = c
	}
	return out, skipped
}

// === Manual rechecks (webhooks + API) ===

func (r *Repair) collectBrokenHealths(names []string, requireArrFile bool) (*xsync.Map[string, *storage.EntryHealth], int) {
	wanted := make(map[string]struct{}, len(names))
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			wanted[n] = struct{}{}
		}
	}

	healths := xsync.NewMap[string, *storage.EntryHealth]()
	_ = r.manager.storage.ForEachEntryHealth(func(h *storage.EntryHealth) error {
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
				if bf.ArrName != "" && bf.ArrFileID != 0 {
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

func (r *Repair) markBrokenHealthCleared(h *storage.EntryHealth, at time.Time) {
	if h == nil {
		return
	}
	if _, err := r.manager.storage.GetEntryItem(h.EntryName); err != nil {
		_ = r.manager.storage.DeleteEntryHealth(h.EntryName)
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

// FixBroken triggers the Arr delete + re-search pass on currently-broken
// entries without reprobing. When names is empty, every entry with
// Status=broken in storage is fixed. Returns the new RepairRun record
// immediately; the actual fix runs in the background.
//
// Use this from the UI when a previous sweep already identified broken
// entries and the user wants to act on them without paying for another
// probe pass.
func (r *Repair) FixBroken(ctx context.Context, names []string) (*storage.RepairRun, error) {
	if ctx == nil {
		ctx = r.parentCtx
	}

	// Skip entries with no Arr-known broken files — there's nothing the fix
	// pass can delete and re-search for them.
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

	if err := r.manager.storage.SaveRepairRun(run); err != nil {
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

// ClearBroken removes currently-broken files from the local mount state. It
// deliberately does not call Arrs, mark history failed, or trigger re-search.
func (r *Repair) ClearBroken(ctx context.Context, names []string) (*storage.RepairRun, error) {
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

	if err := r.manager.storage.SaveRepairRun(run); err != nil {
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

func (r *Repair) clearBroken(ctx context.Context, run *storage.RepairRun, healths *xsync.Map[string, *storage.EntryHealth]) {
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
			if err := r.manager.RemoveTorrentFile(bf.EntryName, bf.FileName); err != nil {
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

// RecheckEntry kicks off a recheck for a single entry and returns
// immediately with an in-progress EntryHealth ack. The actual probe and
// optional fix run in the background. With fix=true, broken Arr-known files
// trigger delete + re-search after probing.
func (r *Repair) RecheckEntry(ctx context.Context, entryName string, fix bool) (*storage.EntryHealth, error) {
	if entryName == "" {
		return nil, errors.New("entry name is empty")
	}
	h, _ := r.manager.storage.GetEntryHealth(entryName)
	if h != nil && h.ActiveRunID != "" {
		return nil, fmt.Errorf("entry is being probed by run %s", h.ActiveRunID)
	}

	item, err := r.manager.GetEntryItem(entryName)
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
		heal := newHealCache()
		final := r.probeEntry(ctx, runID, c, heal, RepairRunOptions{}, fix)
		if !fix || final.Status != storage.HealthBroken {
			return
		}
		pseudo := &storage.RepairRun{ID: runID, Stats: storage.RepairRunStats{}}
		var statsMu sync.Mutex
		r.healBrokenEntry(ctx, pseudo, &statsMu, entryName, final)
	})

	// Return an in-memory ack reflecting the freshly-started recheck. The
	// real EntryHealth in storage is updated by probeEntry shortly after.
	if h == nil {
		h = &storage.EntryHealth{EntryName: entryName}
	}
	h.Status = storage.HealthRepairing
	h.ActiveRunID = runID
	return h, nil
}

// RecheckMedia kicks off a recheck for every entry that an Arr's media-id
// resolves to and returns immediately with the in-progress RepairRun. The
// actual probing + repair runs in the background so HTTP callers don't have
// to block. With arrName="" the first eligible Arr that resolves entries
// wins. fix runs the same delete + re-search pass a sweep would. Honors the
// singleton run lock.
func (r *Repair) RecheckMedia(ctx context.Context, arrName, mediaID string, fix bool) (*storage.RepairRun, error) {
	mediaID = strings.TrimSpace(mediaID)
	if mediaID == "" {
		return nil, errors.New("media_id is required")
	}
	if ctx == nil {
		ctx = r.parentCtx
	}

	// Validate arr selection synchronously so callers fail-fast on bad input.
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

	if err := r.manager.storage.SaveRepairRun(run); err != nil {
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

// executeRecheckMedia is the body of a media recheck. Mirrors executeSweep
// but scoped to a specific media-id resolved through one or more Arrs.
func (r *Repair) executeRecheckMedia(ctx context.Context, run *storage.RepairRun, arrs []*arr.Arr, arrName, mediaID string, fix bool) {
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
		// When the caller didn't pin a specific Arr, the first Arr to resolve
		// non-empty entries wins. Avoids double-probing when sonarr+radarr
		// share a folder root.
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

	heal := newHealCache()
	err := r.probeAndHealCandidates(ctx, run, candidates, heal, RepairRunOptions{}, fix)
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

func (r *Repair) resolveArrsForMedia(arrName string) ([]*arr.Arr, error) {
	if arrName != "" {
		a := r.manager.arr.Get(arrName)
		if a == nil {
			return nil, fmt.Errorf("arr %q not found", arrName)
		}
		if a.Host == "" || a.Token == "" {
			return nil, fmt.Errorf("arr %q is not configured", arrName)
		}
		if a.SkipRepair {
			return nil, fmt.Errorf("arr %q has skip_repair set", arrName)
		}
		return []*arr.Arr{a}, nil
	}
	all := r.eligibleArrs(nil)
	if len(all) == 0 {
		return nil, errors.New("no eligible arrs configured")
	}
	return all, nil
}

// attachArrContext walks Arrs looking for the entry's symlink targets so a
// single-entry fix can reach back into the Arr that owns the file.
func (r *Repair) attachArrContext(ctx context.Context, c *candidate) {
	for _, a := range r.eligibleArrs(nil) {
		if ctx.Err() != nil {
			return
		}
		media, err := a.GetMedia(ctx, "")
		if err != nil {
			continue
		}
		kind := arrKindFromType(a.Type)
		for _, content := range media {
			for entryPath, files := range collectArrFiles(content) {
				if filepath.Clean(filepath.Base(entryPath)) != c.name {
					continue
				}
				if c.contentMap == nil {
					c.contentMap = make(map[string]arr.ContentFile)
				}
				c.arrName = a.Name
				c.arrKind = kind
				for _, f := range files {
					f.EntryName = c.name
					f.IsSymlink = true
					c.contentMap[f.TargetPath] = f
				}
			}
		}
	}
}

// === helpers ===

func orderedFilenames(item *storage.EntryItem) []string {
	if item == nil {
		return nil
	}
	out := make([]string, 0, len(item.Files))
	for name, f := range item.Files {
		if f == nil || f.Deleted {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func topReason(files []storage.BrokenFile) string {
	if len(files) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, f := range files {
		if f.Reason != "" {
			counts[f.Reason]++
		}
	}
	best, bestN := "", 0
	for reason, n := range counts {
		if n > bestN {
			best = reason
			bestN = n
		}
	}
	if best != "" {
		return best
	}
	return files[0].Reason
}

// collectArrFiles groups Arr content files by their resolved symlink-target
// parent directory. The parent is the on-disk entry-folder name.
func collectArrFiles(media arr.Content) map[string][]arr.ContentFile {
	out := make(map[string][]arr.ContentFile)
	for _, f := range media.Files {
		target := readSymlinkTarget(f.Path)
		if target == "" {
			continue
		}
		f.IsSymlink = true
		dir, name := filepath.Split(target)
		f.TargetPath = name
		entryPath := filepath.Clean(dir)
		out[entryPath] = append(out[entryPath], f)
	}
	return out
}

func readSymlinkTarget(path string) string {
	path = filepath.Clean(path)
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	target, err := os.Readlink(path)
	if err != nil {
		return ""
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return target
}

func arrKindFromType(t arr.Type) storage.ArrKind {
	switch t {
	case arr.Sonarr:
		return storage.ArrKindSonarr
	case arr.Radarr:
		return storage.ArrKindRadarr
	case arr.Lidarr:
		return storage.ArrKindLidarr
	case arr.Readarr:
		return storage.ArrKindReadarr
	default:
		return storage.ArrKindOther
	}
}
