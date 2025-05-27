package qbit

import (
	"cmp"
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/service"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// All torrent related helpers goes here

func (q *QBit) AddMagnet(ctx context.Context, url, category string) error {
	magnet, err := utils.GetMagnetFromUrl(url)
	if err != nil {
		return fmt.Errorf("error parsing magnet link: %w", err)
	}
	err = q.Process(ctx, magnet, category)
	if err != nil {
		return fmt.Errorf("failed to process torrent: %w", err)
	}
	return nil
}

func (q *QBit) AddTorrent(ctx context.Context, fileHeader *multipart.FileHeader, category string) error {
	file, _ := fileHeader.Open()
	defer file.Close()
	var reader io.Reader = file
	magnet, err := utils.GetMagnetFromFile(reader, fileHeader.Filename)
	if err != nil {
		return fmt.Errorf("error reading file: %s \n %w", fileHeader.Filename, err)
	}
	err = q.Process(ctx, magnet, category)
	if err != nil {
		return fmt.Errorf("failed to process torrent: %w", err)
	}
	return nil
}

func (q *QBit) Process(ctx context.Context, magnet *utils.Magnet, category string) error {
	svc := service.GetService()
	torrent := createTorrentFromMagnet(magnet, category, "auto")
	a, ok := ctx.Value("arr").(*arr.Arr)
	if !ok {
		return fmt.Errorf("arr not found in context")
	}
	isSymlink := ctx.Value("isSymlink").(bool)
	debridTorrent, err := debrid.ProcessTorrent(svc.Debrid, magnet, a, isSymlink, false)
	if err != nil || debridTorrent == nil {
		if err == nil {
			err = fmt.Errorf("failed to process torrent")
		}
		return err
	}
	torrent = q.UpdateTorrentMin(torrent, debridTorrent)
	q.Storage.AddOrUpdate(torrent)
	go q.ProcessFiles(torrent, debridTorrent, a, isSymlink) // We can send async for file processing not to delay the response
	return nil
}

func (q *QBit) ProcessFiles(torrent *Torrent, debridTorrent *debridTypes.Torrent, arr *arr.Arr, isSymlink bool) {
	svc := service.GetService()
	client := svc.Debrid.GetClient(debridTorrent.Debrid)
	downloadingStatuses := client.GetDownloadingStatus()
	for debridTorrent.Status != "downloaded" {
		q.logger.Debug().Msgf("%s <- (%s) Download Progress: %.2f%%", debridTorrent.Debrid, debridTorrent.Name, debridTorrent.Progress)
		dbT, err := client.CheckStatus(debridTorrent, isSymlink)
		if err != nil {
			if dbT != nil && dbT.Id != "" {
				// Delete the torrent if it was not downloaded
				go func() {
					_ = client.DeleteTorrent(dbT.Id)
				}()
			}
			q.logger.Error().Msgf("Error checking status: %v", err)
			q.MarkAsFailed(torrent)
			go func() {
				if err := arr.Refresh(); err != nil {
					q.logger.Error().Msgf("Error refreshing arr: %v", err)
				}
			}()
			return
		}

		debridTorrent = dbT
		torrent = q.UpdateTorrentMin(torrent, debridTorrent)

		// Exit the loop for downloading statuses to prevent memory buildup
		if debridTorrent.Status == "downloaded" || !utils.Contains(downloadingStatuses, debridTorrent.Status) {
			break
		}
		if !utils.Contains(client.GetDownloadingStatus(), debridTorrent.Status) {
			break
		}
		time.Sleep(time.Duration(q.RefreshInterval) * time.Second)
	}
	var torrentSymlinkPath string
	var err error
	debridTorrent.Arr = arr

	// Check if debrid supports webdav by checking cache
	timer := time.Now()
	if isSymlink {
		cache, useWebdav := svc.Debrid.Caches[debridTorrent.Debrid]
		if useWebdav {
			q.logger.Info().Msgf("Using internal webdav for %s", debridTorrent.Debrid)

			// Use webdav to download the file

			if err := cache.AddTorrent(debridTorrent); err != nil {
				q.logger.Error().Msgf("Error adding torrent to cache: %v", err)
				q.MarkAsFailed(torrent)
				return
			}

			rclonePath := filepath.Join(debridTorrent.MountPath, cache.GetTorrentFolder(debridTorrent)) // /mnt/remote/realdebrid/MyTVShow
			torrentFolderNoExt := utils.RemoveExtension(debridTorrent.Name)
			torrentSymlinkPath, err = q.createSymlinksWebdav(debridTorrent, rclonePath, torrentFolderNoExt) // /mnt/symlinks/{category}/MyTVShow/

		} else {
			// User is using either zurg or debrid webdav
			torrentSymlinkPath, err = q.ProcessSymlink(torrent) // /mnt/symlinks/{category}/MyTVShow/
		}
	} else {
		torrentSymlinkPath, err = q.ProcessManualFile(torrent)
	}
	if err != nil {
		q.MarkAsFailed(torrent)
		go func() {
			_ = client.DeleteTorrent(debridTorrent.Id)
		}()
		q.logger.Info().Msgf("Error: %v", err)
		return
	}
	torrent.TorrentPath = torrentSymlinkPath
	q.UpdateTorrent(torrent, debridTorrent)
	q.logger.Info().Msgf("Adding %s took %s", debridTorrent.Name, time.Since(timer))
	go func() {
		if err := request.SendDiscordMessage("download_complete", "success", torrent.discordContext()); err != nil {
			q.logger.Error().Msgf("Error sending discord message: %v", err)
		}
	}()
	if err := arr.Refresh(); err != nil {
		q.logger.Error().Msgf("Error refreshing arr: %v", err)
	}
}

func (q *QBit) MarkAsFailed(t *Torrent) *Torrent {
	t.State = "error"
	q.Storage.AddOrUpdate(t)
	go func() {
		if err := request.SendDiscordMessage("download_failed", "error", t.discordContext()); err != nil {
			q.logger.Error().Msgf("Error sending discord message: %v", err)
		}
	}()
	return t
}

func (q *QBit) UpdateTorrentMin(t *Torrent, debridTorrent *debridTypes.Torrent) *Torrent {
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
	t.SavePath = filepath.Join(q.DownloadFolder, t.Category) + string(os.PathSeparator)
	t.ContentPath = filepath.Join(t.SavePath, t.Name) + string(os.PathSeparator)
	return t
}

func (q *QBit) UpdateTorrent(t *Torrent, debridTorrent *debridTypes.Torrent) *Torrent {
	if debridTorrent == nil {
		return t
	}

	if debridClient := service.GetDebrid().GetClient(debridTorrent.Debrid); debridClient != nil {
		if debridTorrent.Status != "downloaded" {
			_ = debridClient.UpdateTorrent(debridTorrent)
		}
	}
	t = q.UpdateTorrentMin(t, debridTorrent)
	t.ContentPath = t.TorrentPath + string(os.PathSeparator)

	if t.IsReady() {
		t.State = "pausedUP"
		q.Storage.Update(t)
		return t
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if t.IsReady() {
				t.State = "pausedUP"
				q.Storage.Update(t)
				return t
			}
			updatedT := q.UpdateTorrent(t, debridTorrent)
			t = updatedT

		case <-time.After(10 * time.Minute): // Add a timeout
			return t
		}
	}
}

func (q *QBit) ResumeTorrent(t *Torrent) bool {
	return true
}

func (q *QBit) PauseTorrent(t *Torrent) bool {
	return true
}

func (q *QBit) RefreshTorrent(t *Torrent) bool {
	return true
}

func (q *QBit) GetTorrentProperties(t *Torrent) *TorrentProperties {
	return &TorrentProperties{
		AdditionDate:           t.AddedOn,
		Comment:                "Debrid Blackhole <https://github.com/sirrobot01/decypharr>",
		CreatedBy:              "Debrid Blackhole <https://github.com/sirrobot01/decypharr>",
		CreationDate:           t.AddedOn,
		DlLimit:                -1,
		UpLimit:                -1,
		DlSpeed:                t.Dlspeed,
		UpSpeed:                t.Upspeed,
		TotalSize:              t.Size,
		TotalUploaded:          t.Uploaded,
		TotalDownloaded:        t.Downloaded,
		TotalUploadedSession:   t.UploadedSession,
		TotalDownloadedSession: t.DownloadedSession,
		LastSeen:               time.Now().Unix(),
		NbConnectionsLimit:     100,
		Peers:                  0,
		PeersTotal:             2,
		SeedingTime:            1,
		Seeds:                  100,
		ShareRatio:             100,
	}
}

func (q *QBit) GetTorrentFiles(t *Torrent) []*TorrentFile {
	files := make([]*TorrentFile, 0)
	if t.DebridTorrent == nil {
		return files
	}
	for _, file := range t.DebridTorrent.GetFiles() {
		files = append(files, &TorrentFile{
			Name: file.Path,
			Size: file.Size,
		})
	}
	return files
}

func (q *QBit) SetTorrentTags(t *Torrent, tags []string) bool {
	torrentTags := strings.Split(t.Tags, ",")
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if !utils.Contains(torrentTags, tag) {
			torrentTags = append(torrentTags, tag)
		}
		if !utils.Contains(q.Tags, tag) {
			q.Tags = append(q.Tags, tag)
		}
	}
	t.Tags = strings.Join(torrentTags, ",")
	q.Storage.Update(t)
	return true
}

func (q *QBit) RemoveTorrentTags(t *Torrent, tags []string) bool {
	torrentTags := strings.Split(t.Tags, ",")
	newTorrentTags := utils.RemoveItem(torrentTags, tags...)
	q.Tags = utils.RemoveItem(q.Tags, tags...)
	t.Tags = strings.Join(newTorrentTags, ",")
	q.Storage.Update(t)
	return true
}

func (q *QBit) AddTags(tags []string) bool {
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if !utils.Contains(q.Tags, tag) {
			q.Tags = append(q.Tags, tag)
		}
	}
	return true
}

func (q *QBit) RemoveTags(tags []string) bool {
	q.Tags = utils.RemoveItem(q.Tags, tags...)
	return true
}
