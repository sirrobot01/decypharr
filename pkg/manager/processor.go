package manager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
)

// AddNewTorrent submits a torrent to debrid before entering the active-download queue.
func (m *Manager) AddNewTorrent(ctx context.Context, importReq *ImportRequest) error {
	if importReq == nil || importReq.Magnet == nil {
		return fmt.Errorf("magnet is required")
	}
	if importReq.Arr == nil {
		return fmt.Errorf("arr is required")
	}

	debridTorrent, err := m.SendToDebrid(ctx, importReq)
	if err != nil {
		if isTooManyActiveDownloads(err) {
			m.logger.Warn().Msgf("Too many active downloads, marking as queued: %s", importReq.Magnet.Name)
			return m.queueTorrentRetry(importReq)
		}
		return fmt.Errorf("failed to submit torrent to debrid: %w", err)
	}

	torrent := newTorrentQueueEntry(importReq, debridTypes.TorrentStatusQueued)
	torrent.DownloadUncached = debridTorrent.DownloadUncached
	applyDebridTorrentToEntry(torrent, debridTorrent)

	if err := m.queue.Add(torrent); err != nil {
		return fmt.Errorf("failed to add torrent to queue: %w", err)
	}

	job := NewJob(JobTypeTorrent, importReq)
	job.ID = torrent.InfoHash
	job.Entry = torrent
	job.DebridTorrent = debridTorrent
	if err := m.SubmitJob(job); err != nil {
		torrent.MarkAsError(err)
		_ = m.queue.Update(torrent)
		return fmt.Errorf("failed to queue torrent: %w", err)
	}
	return nil
}

func (m *Manager) processTorrentJob(ctx context.Context, job *Job) error {
	if job == nil || job.Entry == nil {
		return fmt.Errorf("invalid torrent job")
	}
	if _, err := m.queue.GetTorrent(job.Entry.InfoHash); err != nil {
		return nil
	}
	if job.ResumeExisting {
		job.Entry.Status = debridTypes.TorrentStatusDownloading
		job.Entry.IsDownloading = false
		_ = m.queue.Update(job.Entry)
		m.processingEntries.Store(job.Entry.InfoHash, struct{}{})
		m.processQueuedTorrent(job.Entry)
		return nil
	}
	if job.DebridTorrent == nil {
		if job.Request == nil {
			m.waitForDownloadCompletion(ctx, job.Entry)
			return nil
		}
		debridTorrent, err := m.SendToDebrid(ctx, job.Request)
		if err != nil {
			return fmt.Errorf("failed to submit torrent to debrid: %w", err)
		}
		job.DebridTorrent = debridTorrent
	}

	job.Entry.Status = debridTypes.TorrentStatusDownloading
	job.Entry.DownloadUncached = job.DebridTorrent.DownloadUncached
	if job.Request != nil {
		job.Request.Status = "started"
	}
	m.processNewTorrent(job.Entry, job.DebridTorrent)
	return nil
}

func (m *Manager) queueTorrentRetry(importReq *ImportRequest) error {
	torrent := newTorrentQueueEntry(importReq, debridTypes.TorrentStatusQueued)
	if err := m.queue.Add(torrent); err != nil {
		return fmt.Errorf("failed to add torrent to queue: %w", err)
	}

	importReq.Status = "queued"
	importReq.CompletedAt = time.Time{}
	importReq.Error = ""
	job := NewJob(JobTypeTorrent, importReq)
	job.ID = torrent.InfoHash
	job.Entry = torrent
	if err := m.SubmitJob(job); err != nil {
		torrent.MarkAsError(err)
		_ = m.queue.Update(torrent)
		return fmt.Errorf("failed to queue torrent: %w", err)
	}
	return nil
}

func newTorrentQueueEntry(importReq *ImportRequest, status debridTypes.TorrentStatus) *storage.Entry {
	now := time.Now()
	torrent := &storage.Entry{
		InfoHash:         importReq.Magnet.InfoHash,
		Name:             importReq.Magnet.Name,
		OriginalFilename: importReq.Magnet.Name,
		Protocol:         config.ProtocolTorrent,
		Size:             importReq.Magnet.Size,
		Bytes:            importReq.Magnet.Size,
		Magnet:           importReq.Magnet.Link,
		Category:         importReq.Arr.Name,
		SavePath:         filepath.Join(importReq.DownloadFolder, importReq.Arr.Name),
		Status:           status,
		State:            storage.EntryStateDownloading,
		Progress:         0,
		Action:           importReq.Action,
		CallbackURL:      importReq.CallBackUrl,
		SkipMultiSeason:  importReq.SkipMultiSeason,
		CreatedAt:        now,
		UpdatedAt:        now,
		AddedOn:          now,
		Providers:        make(map[string]*storage.ProviderEntry),
		Files:            make(map[string]*storage.File),
		Tags:             []string{},
	}
	torrent.ContentPath = torrent.DownloadPath()
	return torrent
}

func isTooManyActiveDownloads(err error) bool {
	customErr, ok := errors.AsType[*customerror.Error](err)
	return ok && customErr.Code == "too_many_active_downloads"
}

func (m *Manager) processQueuedEntries() {
	queueEntries := m.queue.ListFilter("", config.ProtocolAll, storage.EntryStateDownloading, nil, "", true)
	if len(queueEntries) == 0 {
		return
	}
	for _, entry := range queueEntries {
		// Parse only active downloading torrents
		if entry.State != storage.EntryStateDownloading {
			continue
		}
		if entry.Status == debridTypes.TorrentStatusQueued {
			continue
		}
		// Skip entries that are actively being downloading
		if entry.IsDownloading {
			continue
		}
		// Skip if a previous tick's goroutine hasn't finished yet for this hash.
		if _, loaded := m.processingEntries.LoadOrStore(entry.InfoHash, struct{}{}); loaded {
			continue
		}
		if entry.IsTorrent() {
			if entry.ActiveProvider != "" {
				go m.processQueuedTorrent(entry)
			} else {
				m.processingEntries.Delete(entry.InfoHash)
			}
		} else if entry.IsNZB() {
			go m.processQueuedNZB(entry)
		} else {
			m.processingEntries.Delete(entry.InfoHash)
		}
	}
}

func (m *Manager) processQueuedNZB(entry *storage.Entry) {
	defer m.processingEntries.Delete(entry.InfoHash)
	// Check if the nzb is already processed. Only header fields (status, file
	// list) are needed here; processNZB does not touch the segment map.
	metadata, err := m.usenet.GetNZBHeader(entry.InfoHash)
	if err != nil {
		m.logger.Error().Err(err).Str("name", entry.Name).Msg("Error getting NZB metadata")
		entry.MarkAsError(err)
		_ = m.queue.Update(entry)
		return
	}
	if metadata == nil {
		m.logger.Error().Str("name", entry.Name).Msg("NZB metadata not found")
		entry.MarkAsError(fmt.Errorf("nzb metadata not found"))
		_ = m.queue.Update(entry)
		return
	}
	switch metadata.Status {
	case usenet.NZBStatusFailed:
		m.logger.Error().Str("name", entry.Name).Msg("NZB processing failed")
		entry.MarkAsError(fmt.Errorf("nzb processing failed"))
		_ = m.queue.Update(entry)
		return
	case usenet.NZBStatusParsing, usenet.NZBStatusDownloading:
		// Still processing, skip for now
		return
	case usenet.NZBStatusCompleted:
		if err := m.processNZB(context.Background(), entry, metadata); err != nil {
			m.logger.Error().Err(err).Str("name", entry.Name).Msg("Error processing queued NZB")
			entry.MarkAsError(err)
			_ = m.queue.Update(entry)
			return
		}
	default:
		m.logger.Error().Str("name", entry.Name).Msgf("Unknown NZB status: %s", metadata.Status)
		entry.MarkAsError(fmt.Errorf("unknown nzb status: %s", metadata.Status))
		_ = m.queue.Update(entry)
		return
	}
}

func (m *Manager) processQueuedTorrent(entry *storage.Entry) {
	defer m.processingEntries.Delete(entry.InfoHash)
	placement := entry.GetActiveProvider()
	if placement == nil {
		m.logger.Error().Str("name", entry.Name).Msg("No active placement found for queued entry")
		entry.MarkAsError(fmt.Errorf("no active placement found"))
		_ = m.queue.Update(entry)
		return
	}

	client := m.ProviderClient(entry.ActiveProvider)
	if client == nil {
		m.logger.Error().Str("debrid", entry.ActiveProvider).Msg("Provider client not found")
		entry.MarkAsError(fmt.Errorf("debrid client not found: %s", entry.ActiveProvider))
		_ = m.queue.Update(entry)
		return
	}

	magnet, err := utils.GetMagnetInfo(entry.Magnet, m.config.AlwaysRmTrackerUrls)
	if err != nil {
		magnet = utils.ConstructMagnet(entry.InfoHash, entry.Name)
	}

	arr := m.arr.GetOrCreate(entry.Category)

	debridTorrent := &debridTypes.Torrent{
		Id:               placement.ID,
		InfoHash:         entry.InfoHash,
		Magnet:           magnet,
		Name:             magnet.Name,
		Arr:              arr,
		Size:             entry.Size,
		Files:            make(map[string]debridTypes.File),
		DownloadUncached: entry.DownloadUncached,
	}

	dbT, err := client.CheckStatus(debridTorrent)
	if err != nil {
		m.logger.Error().Err(err).Str("name", entry.Name).Msg("Error checking status")
		entry.MarkAsError(err)
		_ = m.queue.Update(entry)

		// Delete from debrid on error
		go func() {
			if dbT != nil && dbT.Id != "" {
				_ = client.DeleteTorrent(dbT.Id)
			}
		}()
		return
	}

	debridTorrent = dbT

	if debridTorrent == nil {
		m.logger.Error().Str("name", entry.Name).Msg("Provider entry not found")
		entry.MarkAsError(fmt.Errorf("debrid entry not found"))
		_ = m.queue.Update(entry)
		return
	}

	if debridTorrent.Status == debridTypes.TorrentStatusError {
		m.logger.Error().
			Str("debrid", debridTorrent.Debrid).
			Str("name", debridTorrent.Name).
			Str("status", string(debridTorrent.Status)).
			Msg("Entry in error state")
		entry.MarkAsError(fmt.Errorf("entry in error state on debrid: %s", debridTorrent.Debrid))
		_ = m.queue.Update(entry)
		return
	}

	// Update entry progress
	entry.Progress = debridTorrent.Progress / 100.0
	entry.Speed = debridTorrent.Speed
	entry.Size = debridTorrent.GetSize()
	entry.Seeders = debridTorrent.Seeders
	entry.UpdatedAt = time.Now()

	// Update placement progress
	if placement := entry.GetActiveProvider(); placement != nil {
		placement.Progress = entry.Progress
	}

	_ = m.queue.Update(entry)
	// Check if done or failed
	if debridTorrent.Status == debridTypes.TorrentStatusDownloaded {
		go m.processAction(entry)
	}
}

func (m *Manager) processAction(entry *storage.Entry) {
	entry.Status = debridTypes.TorrentStatusDownloaded
	entry.UpdatedAt = time.Now()
	_ = m.queue.Update(entry)
	m.logger.Info().
		Str("name", entry.Name).
		Str("action", string(entry.Action)).
		Msg("Download completed, processing action")

	// Merge with existing entry if same infohash already exists (e.g., same
	// torrent on a different provider). The queue entry only knows about the
	// provider it was queued for, so we need to preserve other placements.
	if existing, err := m.storage.Get(entry.InfoHash); err == nil && existing != nil {
		entry = storage.HandleExistingEntryMerge(existing, entry)
	}

	// Now add entry to the main storage
	if err := m.AddOrUpdate(entry, func(t *storage.Entry) {
		m.RefreshEntries(true)
	}); err != nil {
		m.logger.Error().Err(err).Str("name", entry.Name).Msg("Failed to persist completed download")
		entry.MarkAsError(err)
		_ = m.queue.Update(entry)
		return
	}
	err := m.downloader.download(entry)
	if err != nil {
		m.logger.Error().
			Err(err).
			Str("name", entry.Name).
			Msg("Error running post-download action")
		entry.MarkAsError(err)
		_ = m.queue.Update(entry)
		return
	}
}

// processTorrent handles the complete torrent lifecycle
func (m *Manager) processNewTorrent(torrent *storage.Entry, debridTorrent *debridTypes.Torrent) {
	// Update status to submitting
	torrent.UpdatedAt = time.Now()
	applyDebridTorrentToEntry(torrent, debridTorrent)
	_ = m.queue.Update(torrent)

	if debridTorrent.Status != debridTypes.TorrentStatusDownloaded {
		m.logger.Info().
			Str("debrid", debridTorrent.Debrid).
			Str("name", debridTorrent.Name).
			Msg("Started downloading torrent")
		return
	}

	// Parse post-download action
	go m.processAction(torrent)
}

func applyDebridTorrentToEntry(torrent *storage.Entry, debridTorrent *debridTypes.Torrent) {
	_ = torrent.AddTorrentProvider(debridTorrent)
	torrent.ActiveProvider = debridTorrent.Debrid
	torrent.Bytes = debridTorrent.GetSize()
	torrent.Size = debridTorrent.GetSize()
	torrent.Name = debridTorrent.Name
	torrent.OriginalFilename = debridTorrent.OriginalFilename
	torrent.UpdatedAt = time.Now()

	for _, file := range debridTorrent.Files {
		tFile := &storage.File{
			Name:      file.Name,
			Size:      file.Size,
			ByteRange: file.ByteRange,
			Deleted:   file.Deleted,
			InfoHash:  torrent.InfoHash,
			AddedOn:   torrent.AddedOn,
		}
		torrent.Files[file.Name] = tFile
	}

	if debridTorrent.Status != debridTypes.TorrentStatusDownloaded {
		return
	}
	if placement := torrent.GetActiveProvider(); placement != nil {
		now := time.Now()
		placement.DownloadedAt = &now
		placement.Progress = 1.0
	}
}

// SendToDebrid submits a magnet to debrid service(s) - replaces debrid.Parse
func (m *Manager) SendToDebrid(ctx context.Context, importRequest *ImportRequest) (*debridTypes.Torrent, error) {
	debridTorrent := &debridTypes.Torrent{
		InfoHash: importRequest.Magnet.InfoHash,
		Magnet:   importRequest.Magnet,
		Name:     importRequest.Magnet.Name,
		Arr:      importRequest.Arr,
		Size:     importRequest.Magnet.Size,
		Files:    make(map[string]debridTypes.File),
	}

	clients := m.FilterDebrid(func(c common.Client) bool {
		if importRequest.SelectedDebrid != "" && c.Config().Name != importRequest.SelectedDebrid {
			return false
		}
		return true
	})

	if len(clients) == 0 {
		return nil, fmt.Errorf("no debrid clients available")
	}

	errs := make([]error, 0, len(clients))

	for _, db := range clients {
		overrideDownloadUncached := false

		if importRequest.DownloadUncached != nil {
			overrideDownloadUncached = *importRequest.DownloadUncached
		} else {
			overrideDownloadUncached = db.Config().DownloadUncached
		}
		debridTorrent.DownloadUncached = overrideDownloadUncached
		_logger := db.Logger()
		_logger.Info().
			Str("Provider", db.Config().Name).
			Str("Arr", importRequest.Arr.Name).
			Str("Hash", debridTorrent.InfoHash).
			Str("Name", debridTorrent.Name).
			Str("Action", string(importRequest.Action)).
			Msg("Processing torrent")

		dbt, err := db.SubmitMagnet(debridTorrent)
		if err != nil || dbt == nil || dbt.Id == "" {
			errs = append(errs, err)
			continue
		}
		dbt.Arr = importRequest.Arr
		_logger.Info().Str("id", dbt.Id).Msgf("Entry: %s submitted to %s", dbt.Name, db.Config().Name)

		torrent, err := db.CheckStatus(dbt)
		if err != nil && torrent != nil && torrent.Id != "" {
			// Delete the torrent if it was not downloaded
			go func(id string) {
				_ = db.DeleteTorrent(id)
			}(torrent.Id)
		}
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if torrent == nil {
			errs = append(errs, fmt.Errorf("torrent %s returned nil after checking status", dbt.Name))
			continue
		}
		return torrent, nil
	}
	if len(errs) == 0 {
		return nil, fmt.Errorf("failed to process torrent: no clients available")
	}
	joinedErrors := errors.Join(errs...)
	return nil, fmt.Errorf("failed to process torrent: %w", joinedErrors)
}
