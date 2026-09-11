package repair

import (
	"cmp"
	"context"
	"errors"
	"slices"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func (r *Service) probeTorrentFile(ctx context.Context, entry *storage.Entry, file *storage.File, name string, result fileResult, opts RunOptions) fileResult {
	client := r.backend.ProviderClient(entry.ActiveProvider)
	if client == nil {
		result.reason = "provider_client_not_found"
		return result
	}
	if opts.UnrestrictLink {
		return r.probeTorrentFileByUnrestrict(entry, file, name, result, client)
	}
	if !client.SupportsCheck() {
		result.reason = "provider_check_unsupported"
		return result
	}
	link := linkOf(entry, name)
	if link == "" {
		result.broken = true
		result.reason = "missing_provider_link"
		return result
	}
	if err := client.CheckFile(ctx, file.InfoHash, link); err == nil {
		result.healthy = true
		r.hearsay.ObserveTorrent(client.Config().Provider, file.InfoHash, true)
	} else if errors.Is(err, customerror.HosterUnavailableError) {
		result.broken = true
		result.reason = "hoster_unavailable"
		r.hearsay.ObserveTorrent(client.Config().Provider, file.InfoHash, false)
	} else {
		result.reason = "provider_probe_error"
	}
	return result
}

func (r *Service) probeTorrentFileByUnrestrict(entry *storage.Entry, file *storage.File, name string, result fileResult, client debrid.Client) fileResult {
	placement := entry.GetActiveProvider()
	if placement == nil {
		result.reason = "placement_not_found"
		return result
	}
	placementFile := placement.Files[name]
	if placementFile == nil {
		result.reason = "placement_file_not_found"
		return result
	}
	if placementFile.Link == "" && placementFile.Id == "" {
		result.broken = true
		result.reason = "missing_provider_link"
		return result
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
		result.healthy = true
		r.hearsay.ObserveTorrent(client.Config().Provider, file.InfoHash, true)
		return result
	}
	if err == nil || errors.Is(err, debridTypes.EmptyDownloadLinkError) || errors.Is(err, customerror.HosterUnavailableError) {
		result.broken = true
		if errors.Is(err, customerror.HosterUnavailableError) {
			result.reason = "hoster_unavailable"
		} else {
			result.reason = "empty_download_link"
		}
		r.hearsay.ObserveTorrent(client.Config().Provider, file.InfoHash, false)
		return result
	}
	result.reason = "unrestrict_link_error"
	return result
}

func (r *Service) autoHealResults(ctx context.Context, results []fileResult, heal *errorCache) {
	byHash := make(map[string][]int)
	for i, result := range results {
		if result.broken && result.protocol == config.ProtocolTorrent && result.infoHash != "" {
			byHash[result.infoHash] = append(byHash[result.infoHash], i)
		}
	}
	for infoHash, indices := range byHash {
		entry, err := r.storage.Get(infoHash)
		if err != nil || entry == nil {
			continue
		}
		if err := heal.do(infoHash, func() error { return r.backend.ReinsertEntry(ctx, entry) }); err != nil {
			continue
		}
		for _, i := range indices {
			results[i].broken = false
			results[i].healthy = true
			results[i].reason = "repaired"
		}
	}
}

func (r *Service) brokenFiles(candidate *candidate, results []fileResult) []storage.BrokenFile {
	broken := make([]storage.BrokenFile, 0)
	for _, result := range results {
		if !result.broken {
			continue
		}
		file := storage.BrokenFile{
			EntryName: candidate.name,
			FileName:  result.name,
			InfoHash:  result.infoHash,
			Protocol:  result.protocol,
			Reason:    result.reason,
		}
		if managed, ok := candidate.item.Files[result.name]; ok && managed != nil {
			file.Size = managed.Size
			if file.InfoHash == "" {
				file.InfoHash = managed.InfoHash
			}
		}
		if content, ok := candidate.contentMap[result.name]; ok {
			file.ArrName = candidate.arrName
			file.ArrKind = candidate.arrKind
			file.MediaID = content.Id
			file.EpisodeID = content.EpisodeId
			file.ArrFileID = content.FileId
			file.TargetPath = content.TargetPath
			file.SourcePath = content.Path
			if file.Size == 0 {
				file.Size = content.Size
			}
		}
		broken = append(broken, file)
	}
	slices.SortFunc(broken, func(a, b storage.BrokenFile) int {
		return cmp.Compare(a.FileName, b.FileName)
	})
	return broken
}

func rollupStatus(results []fileResult) storage.HealthStatus {
	if len(results) == 0 {
		return storage.HealthUnknown
	}
	healthy := false
	deferred := false
	for _, result := range results {
		if result.broken {
			return storage.HealthBroken
		}
		healthy = healthy || result.healthy
		deferred = deferred || result.deferred
	}
	if deferred {
		return storage.HealthUnknown
	}
	if healthy {
		return storage.HealthHealthy
	}
	return storage.HealthUnknown
}

func firstProtocol(results []fileResult) config.Protocol {
	for _, result := range results {
		if result.protocol != "" {
			return result.protocol
		}
	}
	return ""
}

func linkOf(entry *storage.Entry, name string) string {
	provider := entry.GetActiveProvider()
	if provider == nil || provider.Files == nil {
		return ""
	}
	file := provider.Files[name]
	if file == nil {
		return ""
	}
	return cmp.Or(file.Link, file.Id)
}
