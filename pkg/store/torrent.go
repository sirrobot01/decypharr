package store

import (
	"cmp"
	"context"
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
	debridTorrent, err := debridTypes.ProcessTorrent(ctx, s.debrid, importReq.Debrid, importReq.Magnet, importReq.Arr, importReq.IsSymlink, importReq.DownloadUncached)
	if err != nil || debridTorrent == nil {
		if err == nil {
			err = fmt.Errorf("failed to process torrent")
		}
		// This error is returned immediately to the user(no need for callback)
		return err
	}
	torrent = s.UpdateTorrentMin(torrent, debridTorrent)
	s.torrents.AddOrUpdate(torrent)
	go s.processFiles(torrent, debridTorrent, importReq) // We can send async for file processing not to delay the response
	return nil
}

func (s *Store) processFiles(torrent *Torrent, debridTorrent *types.Torrent, importReq *ImportRequest) {
	client := s.debrid.GetClient(debridTorrent.Debrid)
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
		torrent = s.UpdateTorrentMin(torrent, debridTorrent)

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
		caches := s.debrid.GetCaches()
		cache, useWebdav := caches[debridTorrent.Debrid]
		if useWebdav {
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
			torrentSymlinkPath, err = s.ProcessSymlink(torrent) // /mnt/symlinks/{category}/MyTVShow/
		}
	} else {
		torrentSymlinkPath, err = s.ProcessManualFile(torrent)
	}
	if err != nil {
		s.markTorrentAsFailed(torrent)
		go func() {
			_ = client.DeleteTorrent(debridTorrent.Id)
		}()
		s.logger.Info().Msgf("Error: %v", err)
		importReq.markAsFailed(err, torrent, debridTorrent)
		return
	}
	torrent.TorrentPath = torrentSymlinkPath
	s.UpdateTorrent(torrent, debridTorrent)
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

func (s *Store) UpdateTorrentMin(t *Torrent, debridTorrent *types.Torrent) *Torrent {
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

func (s *Store) UpdateTorrent(t *Torrent, debridTorrent *types.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	if debridClient := s.debrid.GetClients()[debridTorrent.Debrid]; debridClient != nil {
		if debridTorrent.Status != "downloaded" {
			_ = debridClient.UpdateTorrent(debridTorrent)
		}
	}
	t = s.UpdateTorrentMin(t, debridTorrent)
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
			updatedT := s.UpdateTorrent(t, debridTorrent)
			t = updatedT

		case <-time.After(10 * time.Minute): // Add a timeout
			return t
		}
	}
}
