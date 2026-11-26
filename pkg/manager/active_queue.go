package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
)

// orphanStagedNZBAge keeps new staged files that AddNewNZB has not put into
// the queue yet.
const orphanStagedNZBAge = time.Hour

// removeOrphanStagedNZBs removes staged NZB files that no queue entry uses.
// Older releases removed stalled entries but kept their staged files.
func (m *Manager) removeOrphanStagedNZBs() {
	if m.usenet == nil {
		return
	}
	paths, err := m.usenet.StagedNZBs()
	if err != nil || len(paths) == 0 {
		return
	}
	entries, err := m.queue.storage.FilterQueued(func(e *storage.Entry) bool { return e.IsNZB() && e.Magnet != "" })
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to list the queue for staged NZB cleanup")
		return
	}
	used := make(map[string]bool, len(entries))
	for _, e := range entries {
		used[filepath.Base(e.Magnet)] = true
	}
	removed := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || used[filepath.Base(path)] || time.Since(info.ModTime()) < orphanStagedNZBAge {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	if removed > 0 {
		m.logger.Info().Int("removed", removed).Msg("Removed staged NZB files with no queue entry")
	}
}

func (m *Manager) restoreActiveDownloadJobs() {
	entries, err := m.queue.ListFilter("", config.ProtocolAll, storage.EntryStateDownloading, nil, "", false)
	if err != nil {
		m.logger.Error().Err(err).Msg("Failed to restore the download queue")
		return
	}
	slices.SortFunc(entries, func(left, right *storage.Entry) int { return left.AddedOn.Compare(right.AddedOn) })

	// Clear flags left by interrupted local processing. Active downloads remain
	// in storage for processQueuedEntries. Only unfinished imports need workers.
	for _, entry := range entries {
		if m.ctx.Err() != nil {
			return
		}
		if entry.IsDownloading {
			entry.IsDownloading = false
			if err := m.queue.Update(entry); err != nil {
				m.logger.Error().Err(err).Str("entry_id", entry.InfoHash).Msg("Failed to reset restored download")
				continue
			}
		}
		if entry.Status != debridTypes.TorrentStatusQueued && !m.nzbNeedsReprocessing(entry) {
			continue
		}
		job, err := m.rebuildQueuedJob(entry)
		if err != nil {
			if m.ctx.Err() != nil {
				return
			}
			entry.MarkAsError(err)
			_ = m.queue.Update(entry)
			continue
		}
		if job.DebridTorrent == nil && job.NZBMeta == nil {
			entry.Status = debridTypes.TorrentStatusQueued
		}
		_ = m.queue.Update(entry)
		if err := m.SubmitJob(job); err != nil {
			if m.ctx.Err() != nil {
				return
			}
			entry.MarkAsError(err)
			_ = m.queue.Update(entry)
		}
	}
}

func (m *Manager) nzbNeedsReprocessing(entry *storage.Entry) bool {
	if entry == nil || !entry.IsNZB() || m.usenet == nil {
		return false
	}
	meta, err := m.usenet.GetNZBHeader(entry.InfoHash)
	return err == nil && meta != nil && (meta.Status == usenet.NZBStatusParsing || meta.Status == usenet.NZBStatusDownloading)
}

func (m *Manager) rebuildQueuedJob(entry *storage.Entry) (*Job, error) {
	if entry.IsNZB() {
		return m.rebuildQueuedNZBJob(entry)
	}
	return m.rebuildQueuedTorrentJob(entry)
}

func (m *Manager) rebuildQueuedTorrentJob(entry *storage.Entry) (*Job, error) {
	if entry.ActiveProvider != "" && entry.GetActiveProvider() != nil {
		return &Job{
			ID:             entry.InfoHash,
			Type:           JobTypeTorrent,
			Entry:          entry,
			ResumeExisting: true,
		}, nil
	}

	magnet, err := utils.GetMagnetInfo(entry.Magnet, config.Get().AlwaysRmTrackerUrls)
	if err != nil {
		magnet = utils.ConstructMagnet(entry.InfoHash, entry.Name)
	}

	downloadUncached := entry.DownloadUncached
	req := NewTorrentRequest(
		entry.ActiveProvider,
		downloadFolderForEntry(config.Get().DownloadFolder, entry),
		magnet,
		m.arr.GetOrCreate(entry.Category),
		entry.Action,
		&downloadUncached,
		entry.CallbackURL,
		ImportTypeAPI,
		entry.SkipMultiSeason,
	)
	req.Id = entry.InfoHash
	job := NewJob(JobTypeTorrent, req)
	job.ID = entry.InfoHash
	job.Entry = entry
	return job, nil
}

func (m *Manager) rebuildQueuedNZBJob(entry *storage.Entry) (*Job, error) {
	if m.usenet == nil {
		return nil, fmt.Errorf("usenet is not configured")
	}
	sourcePath := entry.Magnet
	if meta, err := m.usenet.GetNZBHeader(entry.InfoHash); err == nil && meta != nil && meta.Path != "" {
		sourcePath = meta.Path
	}
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, err
	}

	name := entry.OriginalFilename
	if name == "" {
		name = entry.Name
	}
	ctx, cancel := context.WithTimeout(m.ctx, m.usenetTimeout)
	defer cancel()
	meta, groups, err := m.usenet.ParseWithID(ctx, entry.InfoHash, name, content, entry.Category)
	if err != nil {
		return nil, fmt.Errorf("usenet parse failed: %w", err)
	}
	if entry.Magnet != "" && sourcePath == entry.Magnet {
		m.usenet.RemoveStagedNZB(entry.Magnet)
	}

	entry.Magnet = ""
	entry.Name = meta.Name
	entry.OriginalFilename = meta.Name
	entry.Size = meta.TotalSize
	entry.Bytes = meta.TotalSize
	entry.Status = debridTypes.TorrentStatusDownloading
	entry.ActiveProvider = "usenet"
	_ = entry.AddUsenetProvider(meta)

	req := NewNZBRequest(
		meta.Name,
		downloadFolderForEntry(config.Get().DownloadFolder, entry),
		content,
		m.arr.GetOrCreate(entry.Category),
		entry.Action,
		entry.CallbackURL,
		ImportTypeSABnzbd,
		entry.SkipMultiSeason,
	)
	req.Id = entry.InfoHash
	job := NewJob(JobTypeNZB, req)
	job.ID = entry.InfoHash
	job.Entry = entry
	job.NZBMeta = meta
	job.NZBGroups = groups
	return job, nil
}

func downloadFolderForEntry(fallback string, entry *storage.Entry) string {
	if entry != nil && entry.SavePath != "" {
		return filepath.Dir(entry.SavePath)
	}
	return fallback
}
