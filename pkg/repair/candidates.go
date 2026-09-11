package repair

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type candidate struct {
	name       string
	item       *storage.EntryItem
	arrName    string
	arrKind    storage.ArrKind
	contentMap map[string]arr.ContentFile
}

func (r *Service) enumerateCandidates(ctx context.Context, cfg config.RepairConfig) (map[string]*candidate, error) {
	if cfg.Source == config.RepairSourceManaged {
		return r.enumerateManagedCandidates(ctx)
	}
	return r.enumerateArrCandidates(ctx, cfg)
}

func (r *Service) filterCandidatesByProtocol(candidates map[string]*candidate, scope string) map[string]*candidate {
	if repairProtocolMatches(scope, config.ProtocolAll) {
		return candidates
	}
	filtered := make(map[string]*candidate, len(candidates))
	for name, candidate := range candidates {
		if candidate := r.filterCandidateByProtocol(candidate, scope); candidate != nil {
			filtered[name] = candidate
		}
	}
	return filtered
}

func (r *Service) filterCandidateByProtocol(candidate *candidate, scope string) *candidate {
	if candidate == nil {
		return nil
	}
	if candidate.item == nil {
		item, err := r.storage.GetEntryItem(candidate.name)
		if err != nil || item == nil {
			return nil
		}
		candidate.item = item
	}

	files := make(map[string]*storage.File, len(candidate.item.Files))
	for name, file := range candidate.item.Files {
		if file == nil || file.Deleted || file.InfoHash == "" {
			continue
		}
		entry, err := r.storage.Get(file.InfoHash)
		if err == nil && entry != nil && repairProtocolMatches(scope, entry.Protocol) {
			files[name] = file
		}
	}
	if len(files) == 0 {
		return nil
	}

	item := *candidate.item
	item.Files = files
	filtered := *candidate
	filtered.item = &item
	if candidate.contentMap != nil {
		filtered.contentMap = make(map[string]arr.ContentFile, len(candidate.contentMap))
		for name, content := range candidate.contentMap {
			if _, ok := files[name]; ok {
				filtered.contentMap[name] = content
			}
		}
	}
	return &filtered
}

func (r *Service) enumerateManagedCandidates(ctx context.Context) (map[string]*candidate, error) {
	candidates := make(map[string]*candidate)
	for name := range r.storage.GetEntryItems() {
		select {
		case <-ctx.Done():
			return candidates, ctx.Err()
		default:
		}
		candidates[name] = &candidate{name: name}
	}
	return candidates, nil
}

func (r *Service) filterDueCandidates(candidates map[string]*candidate, opts RunOptions) (map[string]*candidate, int) {
	if opts.IgnoreLastChecked {
		return candidates, 0
	}
	now := time.Now()
	recheck := r.recheckInterval()
	due := make(map[string]*candidate, len(candidates))
	skipped := 0
	for name, candidate := range candidates {
		health, _ := r.storage.GetEntryHealth(name)
		if health == nil || health.IsDue(now, recheck) {
			due[name] = candidate
			continue
		}
		skipped++
	}
	return due, skipped
}

func (r *Service) orderCandidatesByLastChecked(due map[string]*candidate) []string {
	type orderedCandidate struct {
		name        string
		lastChecked time.Time
	}
	ordered := make([]orderedCandidate, 0, len(due))
	for name := range due {
		var lastChecked time.Time
		if health, _ := r.storage.GetEntryHealth(name); health != nil {
			lastChecked = health.LastCheckedAt
		}
		ordered = append(ordered, orderedCandidate{name: name, lastChecked: lastChecked})
	}
	slices.SortFunc(ordered, func(a, b orderedCandidate) int {
		if !a.lastChecked.Equal(b.lastChecked) {
			if a.lastChecked.Before(b.lastChecked) {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.name, b.name)
	})
	names := make([]string, len(ordered))
	for i, candidate := range ordered {
		names[i] = candidate.name
	}
	return names
}

func orderedFilenames(item *storage.EntryItem) []string {
	if item == nil {
		return nil
	}
	names := make([]string, 0, len(item.Files))
	for name, file := range item.Files {
		if file != nil && !file.Deleted {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func topReason(files []storage.BrokenFile) string {
	if len(files) == 0 {
		return ""
	}
	counts := make(map[string]int)
	for _, file := range files {
		if file.Reason != "" {
			counts[file.Reason]++
		}
	}
	best, count := "", 0
	for reason, n := range counts {
		if n > count {
			best, count = reason, n
		}
	}
	if best != "" {
		return best
	}
	return files[0].Reason
}
