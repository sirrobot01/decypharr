package debrid

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type fileInfo struct {
	id      string
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
	isDir   bool
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() os.FileMode  { return fi.mode }
func (fi *fileInfo) ModTime() time.Time { return fi.modTime }
func (fi *fileInfo) IsDir() bool        { return fi.isDir }
func (fi *fileInfo) ID() string         { return fi.id }
func (fi *fileInfo) Sys() interface{}   { return nil }

func (c *Cache) RefreshListings(refreshRclone bool) {
	// Copy the torrents to a string|time map
	c.torrents.refreshListing() // refresh torrent listings

	if refreshRclone {
		if err := c.refreshRclone(); err != nil {
			c.logger.Error().Err(err).Msg("Failed to refresh rclone") // silent error
		}
	}
}

func (c *Cache) refreshTorrents(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	default:
	}

	if !c.torrentsRefreshMu.TryLock() {
		return
	}
	defer c.torrentsRefreshMu.Unlock()

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
	cachedTorrents := c.torrents.getIdMaps()
	for id := range cachedTorrents {
		if _, exists := currentTorrentIds[id]; !exists {
			deletedTorrents = append(deletedTorrents, id)
		}
	}

	if len(deletedTorrents) > 0 {
		go c.validateAndDeleteTorrents(deletedTorrents)
	}

	newTorrents := make([]*types.Torrent, 0)
	for _, t := range debTorrents {
		if _, exists := cachedTorrents[t.Id]; !exists {
			newTorrents = append(newTorrents, t)
		}
	}

	if len(newTorrents) == 0 {
		return
	}

	c.logger.Trace().Msgf("Found %d new torrents", len(newTorrents))

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

	c.listingDebouncer.Call(false)

	c.logger.Debug().Msgf("Processed %d new torrents", counter)
}

func (c *Cache) refreshRclone() error {
	cfg := c.config

	if cfg.RcUrl == "" {
		return nil
	}

	if cfg.RcUrl == "" {
		return nil
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        10,
			IdleConnTimeout:     30 * time.Second,
			DisableCompression:  false,
			MaxIdleConnsPerHost: 5,
		},
	}
	// Create form data
	data := ""
	dirs := strings.FieldsFunc(cfg.RcRefreshDirs, func(r rune) bool {
		return r == ',' || r == '&'
	})
	if len(dirs) == 0 {
		data = "dir=__all__"
	} else {
		for index, dir := range dirs {
			if dir != "" {
				if index == 0 {
					data += "dir=" + dir
				} else {
					data += "&dir" + fmt.Sprint(index+1) + "=" + dir
				}
			}
		}
	}

	sendRequest := func(endpoint string) error {
		req, err := http.NewRequest("POST", fmt.Sprintf("%s/%s", cfg.RcUrl, endpoint), strings.NewReader(data))
		if err != nil {
			return err
		}

		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		if cfg.RcUser != "" && cfg.RcPass != "" {
			req.SetBasicAuth(cfg.RcUser, cfg.RcPass)
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			return fmt.Errorf("failed to perform %s: %s - %s", endpoint, resp.Status, string(body))
		}

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

	if torrentId == "" {
		c.logger.Error().Msg("Torrent ID is empty")
		return nil
	}

	torrent, err := c.client.GetTorrent(torrentId)
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to get torrent %s", torrentId)
		return nil
	}
	addedOn, err := time.Parse(time.RFC3339, torrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := CachedTorrent{
		Torrent:    torrent,
		AddedOn:    addedOn,
		IsComplete: len(torrent.Files) > 0,
	}
	c.setTorrent(ct, func(torrent CachedTorrent) {
		go c.listingDebouncer.Call(true)
	})

	return &ct
}

func (c *Cache) refreshDownloadLinks(ctx context.Context) {

	select {
	case <-ctx.Done():
		return
	default:
	}

	if !c.downloadLinksRefreshMu.TryLock() {
		return
	}
	defer c.downloadLinksRefreshMu.Unlock()

	downloadLinks, err := c.client.GetDownloads()

	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to get download links")
		return
	}
	for k, v := range downloadLinks {
		// if link is generated in the last 24 hours, add it to cache
		timeSince := time.Since(v.Generated)
		if timeSince < c.autoExpiresLinksAfterDuration {
			c.downloadLinks.Store(k, linkCache{
				Id:        v.Id,
				accountId: v.AccountId,
				link:      v.DownloadLink,
				expiresAt: v.Generated.Add(c.autoExpiresLinksAfterDuration - timeSince),
			})
		} else {
			c.downloadLinks.Delete(k)
		}
	}

	c.logger.Trace().Msgf("Refreshed %d download links", len(downloadLinks))

}
