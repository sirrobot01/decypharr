package qbit

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/wire"
	"io"
	"mime/multipart"
	"strings"
	"time"
)

// All torrent-related helpers goes here
func (q *QBit) addMagnet(ctx context.Context, url string, arr *arr.Arr, debrid string, action string) error {
	magnet, err := utils.GetMagnetFromUrl(url)
	if err != nil {
		return fmt.Errorf("error parsing magnet link: %w", err)
	}
	_store := wire.Get()

	importReq := wire.NewImportRequest(debrid, q.DownloadFolder, magnet, arr, action, false, "", wire.ImportTypeQBitTorrent)

	err = _store.AddTorrent(ctx, importReq)
	if err != nil {
		return fmt.Errorf("failed to process torrent: %w", err)
	}
	return nil
}

func (q *QBit) addTorrent(ctx context.Context, fileHeader *multipart.FileHeader, arr *arr.Arr, debrid string, action string) error {
	file, _ := fileHeader.Open()
	defer file.Close()
	var reader io.Reader = file
	magnet, err := utils.GetMagnetFromFile(reader, fileHeader.Filename)
	if err != nil {
		return fmt.Errorf("error reading file: %s \n %w", fileHeader.Filename, err)
	}
	_store := wire.Get()
	importReq := wire.NewImportRequest(debrid, q.DownloadFolder, magnet, arr, action, false, "", wire.ImportTypeQBitTorrent)
	err = _store.AddTorrent(ctx, importReq)
	if err != nil {
		return fmt.Errorf("failed to process torrent: %w", err)
	}
	return nil
}

func (q *QBit) ResumeTorrent(t *wire.Torrent) bool {
	return true
}

func (q *QBit) PauseTorrent(t *wire.Torrent) bool {
	return true
}

func (q *QBit) RefreshTorrent(t *wire.Torrent) bool {
	return true
}

func (q *QBit) GetTorrentProperties(t *wire.Torrent) *TorrentProperties {
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

func (q *QBit) setTorrentTags(t *wire.Torrent, tags []string) bool {
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
	q.storage.Update(t)
	return true
}

func (q *QBit) removeTorrentTags(t *wire.Torrent, tags []string) bool {
	torrentTags := strings.Split(t.Tags, ",")
	newTorrentTags := utils.RemoveItem(torrentTags, tags...)
	q.Tags = utils.RemoveItem(q.Tags, tags...)
	t.Tags = strings.Join(newTorrentTags, ",")
	q.storage.Update(t)
	return true
}

func (q *QBit) addTags(tags []string) bool {
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
