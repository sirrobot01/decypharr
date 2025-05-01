package debrid

import (
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"time"
)

type downloadLinkRequest struct {
	result string
	err    error
	done   chan struct{}
}

func newDownloadLinkRequest() *downloadLinkRequest {
	return &downloadLinkRequest{
		done: make(chan struct{}),
	}
}

func (r *downloadLinkRequest) Complete(result string, err error) {
	r.result = result
	r.err = err
	close(r.done)
}

func (r *downloadLinkRequest) Wait() (string, error) {
	<-r.done
	return r.result, r.err
}

func (c *Cache) GetDownloadLink(torrentName, filename, fileLink string) (string, error) {
	// Check link cache
	if dl := c.checkDownloadLink(fileLink); dl != "" {
		return dl, nil
	}

	if req, inFlight := c.downloadLinkRequests.Load(fileLink); inFlight {
		// Wait for the other request to complete and use its result
		result := req.(*downloadLinkRequest)
		return result.Wait()
	}

	// Create a new request object
	req := newDownloadLinkRequest()
	c.downloadLinkRequests.Store(fileLink, req)

	downloadLink, err := c.fetchDownloadLink(torrentName, filename, fileLink)

	// Complete the request and remove it from the map
	req.Complete(downloadLink, err)
	c.downloadLinkRequests.Delete(fileLink)

	return downloadLink, err
}

func (c *Cache) fetchDownloadLink(torrentName, filename, fileLink string) (string, error) {
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return "", fmt.Errorf("torrent not found")
	}
	file := ct.Files[filename]

	if file.Link == "" {
		// file link is empty, refresh the torrent to get restricted links
		ct = c.refreshTorrent(file.TorrentId) // Refresh the torrent from the debrid
		if ct == nil {
			return "", fmt.Errorf("failed to refresh torrent")
		} else {
			file = ct.Files[filename]
		}
	}

	// If file.Link is still empty, return
	if file.Link == "" {
		// Try to reinsert the torrent?
		newCt, err := c.reInsertTorrent(ct)
		if err != nil {
			return "", fmt.Errorf("failed to reinsert torrent. %w", err)
		}
		ct = newCt
		file = ct.Files[filename]
		c.logger.Debug().Str("name", ct.Name).Str("id", ct.Id).Msgf("Reinserted torrent")
	}

	c.logger.Trace().Msgf("Getting download link for %s(%s)", filename, file.Link)
	downloadLink, err := c.client.GetDownloadLink(ct.Torrent, &file)
	if err != nil {
		if errors.Is(err, request.HosterUnavailableError) {
			newCt, err := c.reInsertTorrent(ct)
			if err != nil {
				return "", fmt.Errorf("failed to reinsert torrent: %w", err)
			}
			ct = newCt
			file = ct.Files[filename]
			c.logger.Debug().Str("name", ct.Name).Str("id", ct.Id).Msgf("Reinserted torrent")
			// Retry getting the download link
			downloadLink, err = c.client.GetDownloadLink(ct.Torrent, &file)
			if err != nil {
				return "", err
			}
			if downloadLink == nil {
				return "", fmt.Errorf("download link is empty for")
			}
			c.updateDownloadLink(downloadLink)
			return "", nil
		} else if errors.Is(err, request.TrafficExceededError) {
			// This is likely a fair usage limit error
			return "", err
		} else {
			return "", fmt.Errorf("failed to get download link: %w", err)
		}
	}
	if downloadLink == nil {
		return "", fmt.Errorf("download link is empty")
	}
	c.updateDownloadLink(downloadLink)
	return downloadLink.DownloadLink, nil
}

func (c *Cache) GenerateDownloadLinks(t *CachedTorrent) {
	if err := c.client.GenerateDownloadLinks(t.Torrent); err != nil {
		c.logger.Error().Err(err).Msg("Failed to generate download links")
		return
	}
	for _, file := range t.Files {
		if file.DownloadLink != nil {
			c.updateDownloadLink(file.DownloadLink)
		}

	}
	c.SaveTorrent(t)
}

func (c *Cache) updateDownloadLink(dl *types.DownloadLink) {
	c.downloadLinks.Store(dl.Link, downloadLinkCache{
		Id:        dl.Id,
		Link:      dl.DownloadLink,
		ExpiresAt: time.Now().Add(c.autoExpiresLinksAfterDuration),
		AccountId: dl.AccountId,
	})
}

func (c *Cache) checkDownloadLink(link string) string {
	if dl, ok := c.downloadLinks.Load(link); ok {
		if dl.ExpiresAt.After(time.Now()) && !c.IsDownloadLinkInvalid(dl.Link) {
			return dl.Link
		}
	}
	return ""
}

func (c *Cache) MarkDownloadLinkAsInvalid(link, downloadLink, reason string) {
	c.invalidDownloadLinks.Store(downloadLink, reason)
	// Remove the download api key from active
	if reason == "bandwidth_exceeded" {
		if dl, ok := c.downloadLinks.Load(link); ok {
			if dl.AccountId != "" && dl.Link == downloadLink {
				c.client.DisableAccount(dl.AccountId)
			}
		}
	}
	c.removeDownloadLink(link)
}

func (c *Cache) removeDownloadLink(link string) {
	if dl, ok := c.downloadLinks.Load(link); ok {
		// Delete dl from cache
		c.downloadLinks.Delete(link)
		// Delete dl from debrid
		if dl.Id != "" {
			_ = c.client.DeleteDownloadLink(dl.Id)
		}
	}
}

func (c *Cache) IsDownloadLinkInvalid(downloadLink string) bool {
	if reason, ok := c.invalidDownloadLinks.Load(downloadLink); ok {
		c.logger.Debug().Msgf("Download link %s is invalid: %s", downloadLink, reason)
		return true
	}
	return false
}

func (c *Cache) GetDownloadByteRange(torrentName, filename string) (string, error) {
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return "", fmt.Errorf("torrent not found")
	}
	file := ct.Files[filename]
	if file.ByteRange == "" {
		return "", fmt.Errorf("file %s does not have a byte range", filename)
	}
	return file.ByteRange, nil
}
