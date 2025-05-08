package debrid

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type fileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *fileInfo) Name() string       { return utils.EscapePath(fi.name) }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.isDir }
func (fi *fileInfo) Sys() interface{}   { return nil }

func (c *Cache) RefreshListings(refreshRclone bool) {
	if c.listingRefreshMu.TryLock() {
		defer c.listingRefreshMu.Unlock()
	} else {
		return
	}
	// Copy the torrents to a string|time map
	torrentsTime := make(map[string]time.Time, c.torrentsNames.Size())
	torrents := make([]string, 0, c.torrentsNames.Size())
	c.torrentsNames.Range(func(name string, value *CachedTorrent) bool {
		torrentsTime[name] = value.AddedOn
		torrents = append(torrents, name)
		return true
	})

	// Sort the torrents by name
	sort.Strings(torrents)

	files := make([]os.FileInfo, 0, len(torrents))
	for _, t := range torrents {
		files = append(files, &fileInfo{
			name:    t,
			size:    0,
			mode:    0755 | os.ModeDir,
			modTime: torrentsTime[t],
			isDir:   true,
		})
	}
	// Atomic store of the complete ready-to-use slice
	c.listings.Store(files)
	if err := c.refreshParentXml(); err != nil {
		c.logger.Debug().Err(err).Msg("Failed to refresh XML")
	}

	if refreshRclone {
		if err := c.refreshRclone(); err != nil {
			c.logger.Trace().Err(err).Msg("Failed to refresh rclone") // silent error
		}
	}
}

func (c *Cache) refreshTorrents() {
	// Use a mutex to prevent concurrent refreshes
	if c.torrentsRefreshMu.TryLock() {
		defer c.torrentsRefreshMu.Unlock()
	} else {
		return
	}

	// Get all torrents from the debrid service
	debTorrents, err := c.client.GetTorrents()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get torrents")
		return
	}

	if len(debTorrents) == 0 {
		// Maybe an error occurred
		return
	}

	currentTorrentIds := make(map[string]struct{}, len(debTorrents))
	for _, t := range debTorrents {
		currentTorrentIds[t.Id] = struct{}{}
	}

	// Let's implement deleting torrents removed from debrid
	deletedTorrents := make([]string, 0)
	c.torrents.Range(func(key string, _ string) bool {
		if _, exists := currentTorrentIds[key]; !exists {
			deletedTorrents = append(deletedTorrents, key)
		}
		return true
	})

	// Validate the torrents are truly deleted, then remove them from the cache too
	go c.validateAndDeleteTorrents(deletedTorrents)

	newTorrents := make([]*types.Torrent, 0)
	for _, t := range debTorrents {
		if _, exists := c.torrents.Load(t.Id); !exists {
			newTorrents = append(newTorrents, t)
		}
	}

	if len(newTorrents) == 0 {
		return
	}

	workChan := make(chan *types.Torrent, min(100, len(newTorrents)))
	errChan := make(chan error, len(newTorrents))
	var wg sync.WaitGroup
	counter := 0

	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range workChan {
				if err := c.ProcessTorrent(t); err != nil {
					c.logger.Error().Err(err).Msgf("Failed to process new torrent %s", t.Id)
					errChan <- err
				}
				counter++
			}
		}()
	}

	for _, t := range newTorrents {
		workChan <- t
	}
	close(workChan)
	wg.Wait()

	c.RefreshListings(true)

	c.logger.Debug().Msgf("Processed %d new torrents", counter)
}

func (c *Cache) refreshRclone() error {
	cfg := config.Get().WebDav

	if cfg.RcUrl == "" {
		return nil
	}

	if cfg.RcUrl == "" {
		return nil
	}

	// Create an optimized HTTP client
	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  false,
			MaxIdleConnsPerHost: 5,
		},
	}
	// Create form data
	data := "dir=__all__&dir2=torrents"

	sendRequest := func(endpoint string) error {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", cfg.RcUrl, endpoint), strings.NewReader(data))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		if cfg.RcUser != "" && cfg.RcPass != "" {
			req.SetBasicAuth(cfg.RcUser, cfg.RcPass)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req = req.WithContext(ctx)
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			// Only read a limited amount of the body on error
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return fmt.Errorf("failed to perform %s: %s - %s", endpoint, resp.Status, string(body))
		}

		// Discard response body to reuse connection
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}

	if err := sendRequest("vfs/forget"); err != nil {
		return err
	}
	if err := sendRequest("vfs/refresh"); err != nil {
		return err
	}

	return nil
}

func (c *Cache) refreshTorrent(torrentId string) *CachedTorrent {
	torrent, err := c.client.GetTorrent(torrentId)
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to get torrent %s", torrentId)
		return nil
	}
	addedOn, err := time.Parse(time.RFC3339, torrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := &CachedTorrent{
		Torrent:    torrent,
		AddedOn:    addedOn,
		IsComplete: len(torrent.Files) > 0,
	}
	c.setTorrent(ct, func(torrent *CachedTorrent) {
		go c.RefreshListings(false)
	})

	return ct
}

func (c *Cache) refreshDownloadLinks() {
	if c.downloadLinksRefreshMu.TryLock() {
		defer c.downloadLinksRefreshMu.Unlock()
	} else {
		return
	}

	downloadLinks, err := c.client.GetDownloads()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get download links")
	}
	for k, v := range downloadLinks {
		// if link is generated in the last 24 hours, add it to cache
		timeSince := time.Since(v.Generated)
		if timeSince < c.autoExpiresLinksAfterDuration {
			c.downloadLinks.Store(k, downloadLinkCache{
				Id:        v.Id,
				AccountId: v.AccountId,
				Link:      v.DownloadLink,
				ExpiresAt: v.Generated.Add(c.autoExpiresLinksAfterDuration - timeSince),
			})
		} else {
			c.downloadLinks.Delete(k)
		}
	}

	c.logger.Trace().Msgf("Refreshed %d download links", len(downloadLinks))

}
