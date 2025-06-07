package store

import (
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"

	"sync"
	"time"
)

type linkCache struct {
	Id        string
	link      string
	accountId string
	expiresAt time.Time
}

type downloadLinkCache struct {
	data map[string]linkCache
	mu   sync.Mutex
}

func newDownloadLinkCache() *downloadLinkCache {
	return &downloadLinkCache{
		data: make(map[string]linkCache),
	}
}

func (c *downloadLinkCache) reset() {
	c.mu.Lock()
	c.data = make(map[string]linkCache)
	c.mu.Unlock()
}

func (c *downloadLinkCache) Load(key string) (linkCache, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dl, ok := c.data[key]
	return dl, ok
}
func (c *downloadLinkCache) Store(key string, value linkCache) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.data[key] = value
}
func (c *downloadLinkCache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.data, key)
}

func (c *downloadLinkCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}

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
	file, ok := ct.GetFile(filename)
	if !ok {
		return "", fmt.Errorf("file %s not found in torrent %s", filename, torrentName)
	}

	if file.Link == "" {
		// file link is empty, refresh the torrent to get restricted links
		ct = c.refreshTorrent(file.TorrentId) // Refresh the torrent from the debrid
		if ct == nil {
			return "", fmt.Errorf("failed to refresh torrent")
		} else {
			file, ok = ct.GetFile(filename)
			if !ok {
				return "", fmt.Errorf("file %s not found in refreshed torrent %s", filename, torrentName)
			}
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
		file, ok = ct.GetFile(filename)
		if !ok {
			return "", fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
		}
	}

	c.logger.Trace().Msgf("Getting download link for %s(%s)", filename, file.Link)
	downloadLink, err := c.client.GetDownloadLink(ct.Torrent, &file)
	if err != nil {
		if errors.Is(err, utils.HosterUnavailableError) {
			newCt, err := c.reInsertTorrent(ct)
			if err != nil {
				return "", fmt.Errorf("failed to reinsert torrent: %w", err)
			}
			ct = newCt
			file, ok = ct.GetFile(filename)
			if !ok {
				return "", fmt.Errorf("file %s not found in reinserted torrent %s", filename, torrentName)
			}
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
		} else if errors.Is(err, utils.TrafficExceededError) {
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

func (c *Cache) GenerateDownloadLinks(t CachedTorrent) {
	if err := c.client.GenerateDownloadLinks(t.Torrent); err != nil {
		c.logger.Error().Err(err).Str("torrent", t.Name).Msg("Failed to generate download links")
		return
	}
	for _, file := range t.GetFiles() {
		if file.DownloadLink != nil {
			c.updateDownloadLink(file.DownloadLink)
		}
	}
	c.setTorrent(t, nil)
}

func (c *Cache) updateDownloadLink(dl *types.DownloadLink) {
	c.downloadLinks.Store(dl.Link, linkCache{
		Id:        dl.Id,
		link:      dl.DownloadLink,
		expiresAt: time.Now().Add(c.autoExpiresLinksAfterDuration),
		accountId: dl.AccountId,
	})
}

func (c *Cache) checkDownloadLink(link string) string {
	if dl, ok := c.downloadLinks.Load(link); ok {
		if dl.expiresAt.After(time.Now()) && !c.IsDownloadLinkInvalid(dl.link) {
			return dl.link
		}
	}
	return ""
}

func (c *Cache) MarkDownloadLinkAsInvalid(link, downloadLink, reason string) {
	c.invalidDownloadLinks.Store(downloadLink, reason)
	// Remove the download api key from active
	if reason == "bandwidth_exceeded" {
		if dl, ok := c.downloadLinks.Load(link); ok {
			if dl.accountId != "" && dl.link == downloadLink {
				c.client.DisableAccount(dl.accountId)
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

func (c *Cache) GetDownloadByteRange(torrentName, filename string) (*[2]int64, error) {
	ct := c.GetTorrentByName(torrentName)
	if ct == nil {
		return nil, fmt.Errorf("torrent not found")
	}
	file := ct.Files[filename]
	return file.ByteRange, nil
}

func (c *Cache) GetTotalActiveDownloadLinks() int {
	return c.downloadLinks.Len()
}
