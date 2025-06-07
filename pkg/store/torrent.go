package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"os"
	"path/filepath"
	"time"
)

func (s *Store) AddTorrent(ctx context.Context, importReq *ImportRequest) error {
	torrent := createTorrentFromMagnet(importReq)
	debridTorrent, err := debridTypes.Process(ctx, s.debrid, importReq.SelectedDebrid, importReq.Magnet, importReq.Arr, importReq.IsSymlink, importReq.DownloadUncached)

	if err != nil {
		var httpErr *utils.HTTPError
		if ok := errors.As(err, &httpErr); ok {
			switch httpErr.Code {
			case "too_many_active_downloads":
				// Handle too much active downloads error
				s.logger.Warn().Msgf("Too many active downloads for %s, adding to queue", importReq.Magnet.Name)
				err := s.addToQueue(importReq)
				if err != nil {
					s.logger.Error().Err(err).Msgf("Failed to add %s to queue", importReq.Magnet.Name)
					return err
				}
				torrent.State = "queued"
			default:
				// Unhandled error, return it, caller logs it
				return err
			}
		} else {
			// Unhandled error, return it, caller logs it
			return err
		}
	}
	torrent = s.partialTorrentUpdate(torrent, debridTorrent)
	s.torrents.AddOrUpdate(torrent)
	go s.processFiles(torrent, debridTorrent, importReq) // We can send async for file processing not to delay the response
	return nil
}

func (s *Store) addToQueue(importReq *ImportRequest) error {
	if importReq.Magnet == nil {
		return fmt.Errorf("magnet is required")
	}

	if importReq.Arr == nil {
		return fmt.Errorf("arr is required")
	}

	importReq.Status = "queued"
	importReq.CompletedAt = time.Time{}
	importReq.Error = nil
	err := s.importsQueue.Push(importReq)
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) processFromQueue(ctx context.Context, selectedDebrid string) error {
	// Pop the next import request from the queue
	importReq, err := s.importsQueue.TryPop(selectedDebrid)
	if err != nil {
		return err
	}
	if importReq == nil {
		return nil
	}
	return s.AddTorrent(ctx, importReq)
}

func (s *Store) StartQueueSchedule(ctx context.Context) error {

	s.trackAvailableSlots(ctx) // Initial tracking of available slots

	ticker := time.NewTicker(time.Minute)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.trackAvailableSlots(ctx)
		}
	}
}

func (s *Store) trackAvailableSlots(ctx context.Context) {
	// This function tracks the available slots for each debrid client
	availableSlots := make(map[string]int)

	for name, deb := range s.debrid.Debrids() {
		slots, err := deb.Client().GetAvailableSlots()
		if err != nil {
			continue
		}
		availableSlots[name] = slots
	}

	for name, slots := range availableSlots {
		if s.importsQueue.Size(name) <= 0 {
			continue
		}
		s.logger.Debug().Msgf("Available slots for %s: %d", name, slots)
		// If slots are available, process the next import request from the queue
		for slots > 0 {
			select {
			case <-ctx.Done():
				return // Exit if context is done
			default:
				if err := s.processFromQueue(ctx, name); err != nil {
					s.logger.Error().Err(err).Msg("Error processing from queue")
					return // Exit on error
				}
				slots-- // Decrease the available slots after processing
			}
		}
	}
}

func (s *Store) processFiles(torrent *Torrent, debridTorrent *types.Torrent, importReq *ImportRequest) {

	if debridTorrent == nil {
		// Early return if debridTorrent is nil
		return
	}

	deb := s.debrid.Debrid(debridTorrent.Debrid)
	client := deb.Client()
	downloadingStatuses := client.GetDownloadingStatus()
	_arr := importReq.Arr
	for debridTorrent.Status != "downloaded" {
		s.logger.Debug().Msgf("%s <- (%s) Download Progress: %.2f%%", debridTorrent.Debrid, debridTorrent.Name, debridTorrent.Progress)
		dbT, err := client.CheckStatus(debridTorrent, importReq.IsSymlink)
		if err != nil {
			if dbT != nil && dbT.Id != "" {
				// Delete the torrent if it was not downloaded
				go func() {
					_ = client.DeleteTorrent(dbT.Id)
				}()
			}
			s.logger.Error().Msgf("Error checking status: %v", err)
			s.markTorrentAsFailed(torrent)
			go func() {
				_arr.Refresh()
			}()
			importReq.markAsFailed(err, torrent, debridTorrent)
			return
		}

		debridTorrent = dbT
		torrent = s.partialTorrentUpdate(torrent, debridTorrent)

		// Exit the loop for downloading statuses to prevent memory buildup
		if debridTorrent.Status == "downloaded" || !utils.Contains(downloadingStatuses, debridTorrent.Status) {
			break
		}
		if !utils.Contains(client.GetDownloadingStatus(), debridTorrent.Status) {
			break
		}
		time.Sleep(s.refreshInterval)
	}
	var torrentSymlinkPath string
	var err error
	debridTorrent.Arr = _arr

	// Check if debrid supports webdav by checking cache
	timer := time.Now()
	if importReq.IsSymlink {
		cache := deb.Cache()
		if cache != nil {
			s.logger.Info().Msgf("Using internal webdav for %s", debridTorrent.Debrid)

			// Use webdav to download the file

			if err := cache.Add(debridTorrent); err != nil {
				s.logger.Error().Msgf("Error adding torrent to cache: %v", err)
				s.markTorrentAsFailed(torrent)
				importReq.markAsFailed(err, torrent, debridTorrent)
				return
			}

			rclonePath := filepath.Join(debridTorrent.MountPath, cache.GetTorrentFolder(debridTorrent)) // /mnt/remote/realdebrid/MyTVShow
			torrentFolderNoExt := utils.RemoveExtension(debridTorrent.Name)
			torrentSymlinkPath, err = s.createSymlinksWebdav(torrent, debridTorrent, rclonePath, torrentFolderNoExt) // /mnt/symlinks/{category}/MyTVShow/

		} else {
			// User is using either zurg or debrid webdav
			torrentSymlinkPath, err = s.processSymlink(torrent) // /mnt/symlinks/{category}/MyTVShow/
		}
	} else {
		torrentSymlinkPath, err = s.processDownload(torrent)
	}
	if err != nil {
		s.markTorrentAsFailed(torrent)
		go func() {
			_ = client.DeleteTorrent(debridTorrent.Id)
		}()
		s.logger.Error().Err(err).Msgf("Error occured while processing torrent %s", debridTorrent.Name)
		importReq.markAsFailed(err, torrent, debridTorrent)
		return
	}
	torrent.TorrentPath = torrentSymlinkPath
	s.updateTorrent(torrent, debridTorrent)
	s.logger.Info().Msgf("Adding %s took %s", debridTorrent.Name, time.Since(timer))

	go importReq.markAsCompleted(torrent, debridTorrent) // Mark the import request as completed, send callback if needed
	go func() {
		if err := request.SendDiscordMessage("download_complete", "success", torrent.discordContext()); err != nil {
			s.logger.Error().Msgf("Error sending discord message: %v", err)
		}
	}()
	_arr.Refresh()
}

func (s *Store) markTorrentAsFailed(t *Torrent) *Torrent {
	t.State = "error"
	s.torrents.AddOrUpdate(t)
	go func() {
		if err := request.SendDiscordMessage("download_failed", "error", t.discordContext()); err != nil {
			s.logger.Error().Msgf("Error sending discord message: %v", err)
		}
	}()
	return t
}

func (s *Store) partialTorrentUpdate(t *Torrent, debridTorrent *types.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	addedOn, err := time.Parse(time.RFC3339, debridTorrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	totalSize := debridTorrent.Bytes
	progress := (cmp.Or(debridTorrent.Progress, 0.0)) / 100.0
	sizeCompleted := int64(float64(totalSize) * progress)

	var speed int64
	if debridTorrent.Speed != 0 {
		speed = debridTorrent.Speed
	}
	var eta int
	if speed != 0 {
		eta = int((totalSize - sizeCompleted) / speed)
	}
	t.ID = debridTorrent.Id
	t.Name = debridTorrent.Name
	t.AddedOn = addedOn.Unix()
	t.DebridTorrent = debridTorrent
	t.Debrid = debridTorrent.Debrid
	t.Size = totalSize
	t.Completed = sizeCompleted
	t.Downloaded = sizeCompleted
	t.DownloadedSession = sizeCompleted
	t.Uploaded = sizeCompleted
	t.UploadedSession = sizeCompleted
	t.AmountLeft = totalSize - sizeCompleted
	t.Progress = progress
	t.Eta = eta
	t.Dlspeed = speed
	t.Upspeed = speed
	t.ContentPath = filepath.Join(t.SavePath, t.Name) + string(os.PathSeparator)
	return t
}

func (s *Store) updateTorrent(t *Torrent, debridTorrent *types.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	if debridClient := s.debrid.Clients()[debridTorrent.Debrid]; debridClient != nil {
		if debridTorrent.Status != "downloaded" {
			_ = debridClient.UpdateTorrent(debridTorrent)
		}
	}
	t = s.partialTorrentUpdate(t, debridTorrent)
	t.ContentPath = t.TorrentPath + string(os.PathSeparator)

	if t.IsReady() {
		t.State = "pausedUP"
		s.torrents.Update(t)
		return t
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if t.IsReady() {
				t.State = "pausedUP"
				s.torrents.Update(t)
				return t
			}
			updatedT := s.updateTorrent(t, debridTorrent)
			t = updatedT

		case <-time.After(10 * time.Minute): // Add a timeout
			return t
		}
	}
}
