package store

import (
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const (
	MaxLinkFailures = 10
)

func (c *Cache) GetDownloadLink(torrentName, filename, fileLink string) (types.DownloadLink, error) {
	// Check
	counter, ok := c.failedLinksCounter.Load(fileLink)
	if ok && counter.Load() >= MaxLinkFailures {
		return types.DownloadLink{}, fmt.Errorf("file link %s has failed %d times, not retrying", fileLink, counter.Load())
	}

	// Use singleflight to deduplicate concurrent requests
	v, err, _ := c.downloadSG.Do(fileLink, func() (interface{}, error) {
		// Double-check cache inside singleflight (another goroutine might have filled it)
		if dl, err := c.checkDownloadLink(fileLink); err == nil && !dl.Empty() {
			return dl, nil
		}

		// Fetch the download link
		dl, err := c.fetchDownloadLink(torrentName, filename, fileLink)
		if err != nil {
			c.downloadSG.Forget(fileLink)
			return types.DownloadLink{}, err
		}

		if dl.Empty() {
			c.downloadSG.Forget(fileLink)
			err = fmt.Errorf("download link is empty for %s in torrent %s", filename, torrentName)
			return types.DownloadLink{}, err
		}

		return dl, nil
	})

	if err != nil {
		return types.DownloadLink{}, err
	}
	return v.(types.DownloadLink), nil
}

func (c *Cache) fetchDownloadLink(torrentName, filename, fileLink string) (types.DownloadLink, error) {
	emptyDownloadLink := types.DownloadLink{}
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return emptyDownloadLink, fmt.Errorf("torrent not found")
	}
	file, ok := ct.GetFile(filename)
	if !ok {
		return emptyDownloadLink, fmt.Errorf("file %s not found in torrent %s", filename, torrentName)
	}

	if file.Link == "" {
		// file link is empty, refresh the torrent to get restricted links
		ct = c.refreshTorrent(file.TorrentId) // Refresh the torrent from the debrid
		if ct == nil {
			return emptyDownloadLink, fmt.Errorf("failed to refresh torrent")
		} else {
			file, ok = ct.GetFile(filename)
			if !ok {
				return emptyDownloadLink, fmt.Errorf("file %s not found in refreshed torrent %s", filename, torrentName)
			}
		}
	}

	// If file.Link is still empty, return
	if file.Link == "" {
		// Try to reinsert the torrent?
		newCt, err := c.reInsertTorrent(ct)
		if err != nil {
			return emptyDownloadLink, fmt.Errorf("failed to reinsert torrent. %w", err)
		}
		ct = newCt
		file, ok = ct.GetFile(filename)
		if !ok {
			return emptyDownloadLink, fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
		}
	}

	c.logger.Trace().Msgf("Getting download link for %s(%s)", filename, file.Link)
	downloadLink, err := c.client.GetDownloadLink(ct.Torrent, &file)
	if err != nil {
		if errors.Is(err, utils.HosterUnavailableError) {
			c.logger.Trace().
				Str("token", utils.Mask(downloadLink.Token)).
				Str("filename", filename).
				Str("torrent_id", ct.Id).
				Msg("Hoster unavailable, attempting to reinsert torrent")

			newCt, err := c.reInsertTorrent(ct)
			if err != nil {
				return emptyDownloadLink, fmt.Errorf("failed to reinsert torrent: %w", err)
			}
			ct = newCt
			file, ok = ct.GetFile(filename)
			if !ok {
				return emptyDownloadLink, fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
			}
			// Retry getting the download link
			downloadLink, err = c.client.GetDownloadLink(ct.Torrent, &file)
			if err != nil {
				return emptyDownloadLink, fmt.Errorf("retry failed to get download link: %w", err)
			}
			if downloadLink.Empty() {
				return emptyDownloadLink, fmt.Errorf("download link is empty after retry")
			}
			return emptyDownloadLink, fmt.Errorf("download link is empty after retry")
		} else if errors.Is(err, utils.TrafficExceededError) {
			// This is likely a fair usage limit error
			return emptyDownloadLink, err
		} else {
			return emptyDownloadLink, fmt.Errorf("failed to get download link: %w", err)
		}
	}
	if downloadLink.Empty() {
		return emptyDownloadLink, fmt.Errorf("download link is empty")
	}
	return downloadLink, nil
}

func (c *Cache) GetFileDownloadLinks(t CachedTorrent) {
	if err := c.client.GetFileDownloadLinks(t.Torrent); err != nil {
		c.logger.Error().Err(err).Str("torrent", t.Name).Msg("Failed to generate download links")
		return
	}
}

func (c *Cache) checkDownloadLink(link string) (types.DownloadLink, error) {
	dl, err := c.client.AccountManager().GetDownloadLink(link)
	if err != nil {
		return dl, err
	}
	if !c.downloadLinkIsInvalid(dl.DownloadLink) {
		return dl, nil
	}
	return types.DownloadLink{}, fmt.Errorf("download link not found for %s", link)
}

func (c *Cache) IncrementFailedLinkCounter(link string) int32 {
	counter, _ := c.failedLinksCounter.LoadOrCompute(link, func() (atomic.Int32, bool) {
		return atomic.Int32{}, true
	})
	return counter.Add(1)
}

func (c *Cache) MarkLinkAsInvalid(downloadLink types.DownloadLink, reason string) {
	// Increment file link error counter
	c.IncrementFailedLinkCounter(downloadLink.Link)

	c.invalidDownloadLinks.Store(downloadLink.DownloadLink, reason)
	// Remove the download api key from active
	if reason == "bandwidth_exceeded" {
		// Disable the account
		accountManager := c.client.AccountManager()
		account, err := accountManager.GetAccount(downloadLink.Token)
		if err != nil {
			c.logger.Error().Err(err).Str("token", utils.Mask(downloadLink.Token)).Msg("Failed to get account to disable")
			return
		}
		if account == nil {
			c.logger.Error().Str("token", utils.Mask(downloadLink.Token)).Msg("Account not found to disable")
			return
		}
		accountManager.Disable(account)
	}
}

func (c *Cache) downloadLinkIsInvalid(downloadLink string) bool {
	if _, ok := c.invalidDownloadLinks.Load(downloadLink); ok {
		return true
	}
	return false
}

func (c *Cache) GetDownloadByteRange(torrentName, filename string) (*[2]int64, error) {
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return nil, fmt.Errorf("torrent not found")
	}
	file := ct.Files[filename]
	return file.ByteRange, nil
}

func (c *Cache) GetTotalActiveDownloadLinks() int {
	total := 0
	allAccounts := c.client.AccountManager().Active()
	for _, acc := range allAccounts {
		total += acc.DownloadLinksCount()
	}
	return total
}
