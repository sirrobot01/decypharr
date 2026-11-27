package qbit

import (
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func (q *QBit) addMagnet(ctx context.Context, url string, arr arr.Arr, debrid string, action config.DownloadAction, callbackURL string, rmTrackerUrls, skipMultiSeason bool) (string, error) {
	magnet, err := utils.GetMagnetFromUrl(url, rmTrackerUrls)
	if err != nil {
		return "", customerror.NewError(fmt.Errorf("error parsing magnet link: %w", err), http.StatusBadRequest, "invalid_magnet", false, false).Permanent()
	}

	importReq := manager.NewTorrentRequest(debrid, q.downloadFolder, magnet, arr, action, arr.DownloadUncached, callbackURL, manager.ImportTypeQBit, skipMultiSeason)

	err = q.manager.AddNewTorrent(ctx, importReq)
	if err != nil {
		return "", fmt.Errorf("failed to process torrent: %w", err)
	}
	return magnet.InfoHash, nil
}

func (q *QBit) addTorrent(ctx context.Context, fileHeader *multipart.FileHeader, arr arr.Arr, debrid string, action config.DownloadAction, callbackURL string, rmTrackerUrls, skipMultiSeason bool) (string, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return "", customerror.NewError(fmt.Errorf("error opening torrent file: %w", err), http.StatusBadRequest, "invalid_torrent", false, false).Permanent()
	}
	defer file.Close()
	magnet, err := utils.GetMagnetFromFile(file, fileHeader.Filename, rmTrackerUrls)
	if err != nil {
		return "", customerror.NewError(fmt.Errorf("error reading file %s: %w", fileHeader.Filename, err), http.StatusBadRequest, "invalid_torrent", false, false).Permanent()
	}
	importReq := manager.NewTorrentRequest(debrid, q.downloadFolder, magnet, arr, action, arr.DownloadUncached, callbackURL, manager.ImportTypeQBit, skipMultiSeason)
	err = q.manager.AddNewTorrent(ctx, importReq)
	if err != nil {
		return "", fmt.Errorf("failed to process torrent: %w", err)
	}
	return magnet.InfoHash, nil
}

// splitTags turns a comma separated tag list into trimmed, non empty tags.
func splitTags(raw string) []string {
	var tags []string
	for tag := range strings.SplitSeq(raw, ",") {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}

// tagAddedTorrents applies the tags sent with torrents/add to the torrents that call queued.
func (q *QBit) tagAddedTorrents(hashes, tags []string) {
	for _, hash := range hashes {
		if t, err := q.manager.Queue().GetTorrent(hash); err == nil {
			q.setTorrentTags(t, tags)
		}
	}
}

// filterByTag implements the torrents/info tag filter. An empty tag keeps only untagged torrents.
func filterByTag(torrents []*storage.Entry, tag string) []*storage.Entry {
	kept := torrents[:0:0]
	for _, t := range torrents {
		if (tag == "" && len(t.Tags) == 0) || (tag != "" && slices.Contains(t.Tags, tag)) {
			kept = append(kept, t)
		}
	}
	return kept
}

func (q *QBit) ResumeTorrent(t *storage.Entry) bool {
	return true
}

func (q *QBit) PauseTorrent(t *storage.Entry) bool {
	return true
}

func (q *QBit) RefreshTorrent(t *storage.Entry) bool {
	return true
}

func (q *QBit) GetTorrentProperties(t *storage.Entry) *TorrentProperties {
	return &TorrentProperties{
		AdditionDate:       t.AddedOn.Unix(),
		Comment:            "Provider Blackhole <https://github.com/sirrobot01/decypharr>",
		CreatedBy:          "Provider Blackhole <https://github.com/sirrobot01/decypharr>",
		CreationDate:       t.AddedOn.Unix(),
		DlLimit:            -1,
		UpLimit:            -1,
		DlSpeed:            t.Speed,
		UpSpeed:            t.Speed,
		TotalSize:          t.Size,
		TotalUploaded:      t.Bytes,
		TotalDownloaded:    t.Bytes,
		LastSeen:           time.Now().Unix(),
		NbConnectionsLimit: 100,
		Peers:              0,
		PeersTotal:         2,
		SeedingTime:        1,
		Seeds:              100,
		ShareRatio:         100,
	}
}

func (q *QBit) setTorrentTags(t *storage.Entry, tags []string) {
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if !slices.Contains(t.Tags, tag) {
			t.Tags = append(t.Tags, tag)
		}
		if !slices.Contains(q.Tags, tag) {
			q.Tags = append(q.Tags, tag)
		}
	}
	_ = q.manager.Queue().Update(t)
}

func (q *QBit) removeTorrentTags(t *storage.Entry, tags []string) bool {
	newTorrentTags := utils.RemoveItem(t.Tags, tags...)
	q.Tags = utils.RemoveItem(q.Tags, tags...)
	t.Tags = newTorrentTags
	_ = q.manager.Queue().Update(t)
	return true
}

func (q *QBit) addTags(tags []string) bool {
	for _, tag := range tags {
		if tag == "" {
			continue
		}
		if !slices.Contains(q.Tags, tag) {
			q.Tags = append(q.Tags, tag)
		}
	}
	return true
}
