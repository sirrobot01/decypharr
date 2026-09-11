package repair

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

func (r *Service) probeEntry(ctx context.Context, runID string, c *candidate, heal *errorCache, nzb *nzbProber, opts RunOptions, autoRepair bool) *storage.EntryHealth {
	s := r.storage
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

	h.PreviousStatus = previous
	h.Status = storage.HealthRepairing
	h.ActiveRunID = runID
	h.Protocol = ""
	r.saveHealth(h)

	names := orderedFilenames(c.item)
	results := r.probeFiles(ctx, c, names, nzb, opts)
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
	case storage.HealthUnknown:
		h.FailureReason = firstDeferredReason(results)
	}
	r.saveHealth(h)
	return h
}

func firstDeferredReason(results []fileResult) string {
	for _, result := range results {
		if result.deferred && result.reason != "" {
			return result.reason
		}
	}
	return ""
}

func (r *Service) probeFiles(ctx context.Context, c *candidate, names []string, nzb *nzbProber, opts RunOptions) []fileResult {
	results := make([]fileResult, len(names))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(repairFilesPerEntry)
	for i, name := range names {
		g.Go(func() error {
			if gctx.Err() != nil {
				results[i] = fileResult{name: name, reason: "context_cancelled"}
				return nil
			}
			results[i] = r.probeFile(gctx, c, name, nzb, opts)
			return nil
		})
	}
	_ = g.Wait()
	return results
}

func (r *Service) probeFile(ctx context.Context, c *candidate, name string, nzb *nzbProber, opts RunOptions) fileResult {
	res := fileResult{name: name}
	if c == nil || c.item == nil {
		res.reason = "entry_not_found"
		return res
	}
	file := c.item.Files[name]

	if file == nil || file.InfoHash == "" {
		res.reason = "missing_infohash"
		return res
	}
	res.infoHash = file.InfoHash

	entry, err := r.storage.Get(file.InfoHash)
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
		return nzb.probe(ctx, nzbProbeRequest{
			nzbID:         entry.InfoHash,
			fileName:      name,
			verifyContent: opts.VerifyContent != nil && *opts.VerifyContent,
		})
	}
	return r.probeTorrentFile(ctx, entry, file, name, res, opts)
}
