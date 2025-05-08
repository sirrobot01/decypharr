package debrid

import (
	"errors"
	"fmt"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"sync"
	"time"
)

type reInsertRequest struct {
	result *CachedTorrent
	err    error
	done   chan struct{}
}

func newReInsertRequest() *reInsertRequest {
	return &reInsertRequest{
		done: make(chan struct{}),
	}
}

func (r *reInsertRequest) Complete(result *CachedTorrent, err error) {
	r.result = result
	r.err = err
	close(r.done)
}

func (r *reInsertRequest) Wait() (*CachedTorrent, error) {
	<-r.done
	return r.result, r.err
}

func (c *Cache) IsTorrentBroken(t *CachedTorrent, filenames []string) bool {
	// Check torrent files

	isBroken := false
	files := make(map[string]types.File)
	if len(filenames) > 0 {
		for name, f := range t.Files {
			if utils.Contains(filenames, name) {
				files[name] = f
			}
		}
	} else {
		files = t.Files
	}

	// Check empty links
	for _, f := range files {
		// Check if file is missing
		if f.Link == "" {
			// refresh torrent and then break
			if newT := c.refreshTorrent(f.TorrentId); newT != nil {
				t = newT
			} else {
				c.logger.Error().Str("torrentId", t.Torrent.Id).Msg("Failed to refresh torrent")
				return true
			}
		}
	}

	if t.Torrent == nil {
		c.logger.Error().Str("torrentId", t.Torrent.Id).Msg("Failed to refresh torrent")
		return true
	}

	files = t.Files

	for _, f := range files {
		// Check if file link is still missing
		if f.Link == "" {
			isBroken = true
			break
		} else {
			// Check if file.Link not in the downloadLink Cache
			if err := c.client.CheckLink(f.Link); err != nil {
				if errors.Is(err, request.HosterUnavailableError) {
					isBroken = true
					break
				}
			}
		}
	}
	cfg := config.Get()
	// Try to reinsert the torrent if it's broken
	if cfg.Repair.ReInsert && isBroken && t.Torrent != nil {
		// Check if the torrent is already in progress
		if _, err := c.reInsertTorrent(t); err != nil {
			c.logger.Error().Err(err).Str("torrentId", t.Torrent.Id).Msg("Failed to reinsert torrent")
			return true
		}
		return false
	}

	return isBroken
}

func (c *Cache) repairWorker() {
	// This watches a channel for torrents to repair
	for req := range c.repairChan {
		torrentId := req.TorrentID
		c.logger.Debug().Str("torrentId", req.TorrentID).Msg("Received repair request")

		// Get the torrent from the cache
		cachedTorrent := c.GetTorrent(torrentId)
		if cachedTorrent == nil {
			c.logger.Warn().Str("torrentId", torrentId).Msg("Torrent not found in cache")
			continue
		}

		switch req.Type {
		case RepairTypeReinsert:
			c.logger.Debug().Str("torrentId", torrentId).Msg("Reinserting torrent")
			if _, err := c.reInsertTorrent(cachedTorrent); err != nil {
				c.logger.Error().Err(err).Str("torrentId", cachedTorrent.Id).Msg("Failed to reinsert torrent")
				continue
			}
		case RepairTypeDelete:
			c.logger.Debug().Str("torrentId", torrentId).Msg("Deleting torrent")
			if err := c.DeleteTorrent(torrentId); err != nil {
				c.logger.Error().Err(err).Str("torrentId", torrentId).Msg("Failed to delete torrent")
				continue
			}
		}
	}
}

func (c *Cache) reInsertTorrent(ct *CachedTorrent) (*CachedTorrent, error) {
	// Check if Magnet is not empty, if empty, reconstruct the magnet
	torrent := ct.Torrent
	oldID := torrent.Id // Store the old ID
	if _, ok := c.failedToReinsert.Load(oldID); ok {
		return ct, fmt.Errorf("can't retry re-insert for %s", torrent.Id)
	}
	if reqI, inFlight := c.repairRequest.Load(oldID); inFlight {
		req := reqI.(*reInsertRequest)
		c.logger.Debug().Msgf("Waiting for existing reinsert request to complete for torrent %s", oldID)
		return req.Wait()
	}
	req := newReInsertRequest()
	c.repairRequest.Store(oldID, req)

	// Make sure we clean up even if there's a panic
	defer func() {
		c.repairRequest.Delete(oldID)
	}()

	// Submit the magnet to the debrid service
	newTorrent := &types.Torrent{
		Name:     torrent.Name,
		Magnet:   utils.ConstructMagnet(torrent.InfoHash, torrent.Name),
		InfoHash: torrent.InfoHash,
		Size:     torrent.Size,
		Files:    make(map[string]types.File),
		Arr:      torrent.Arr,
	}
	var err error
	newTorrent, err = c.client.SubmitMagnet(newTorrent)
	if err != nil {
		c.failedToReinsert.Store(oldID, struct{}{})
		// Remove the old torrent from the cache and debrid service
		return ct, fmt.Errorf("failed to submit magnet: %w", err)
	}

	// Check if the torrent was submitted
	if newTorrent == nil || newTorrent.Id == "" {
		c.failedToReinsert.Store(oldID, struct{}{})
		return ct, fmt.Errorf("failed to submit magnet: empty torrent")
	}
	newTorrent.DownloadUncached = false // Set to false, avoid re-downloading
	newTorrent, err = c.client.CheckStatus(newTorrent, true)
	if err != nil {
		if newTorrent != nil && newTorrent.Id != "" {
			// Delete the torrent if it was not downloaded
			_ = c.client.DeleteTorrent(newTorrent.Id)
		}
		c.failedToReinsert.Store(oldID, struct{}{})
		return ct, err
	}

	// Update the torrent in the cache
	addedOn, err := time.Parse(time.RFC3339, torrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	for _, f := range newTorrent.Files {
		if f.Link == "" {
			c.failedToReinsert.Store(oldID, struct{}{})
			return ct, fmt.Errorf("failed to reinsert torrent: empty link")
		}
	}
	// Set torrent to newTorrent
	ct = &CachedTorrent{
		Torrent:    newTorrent,
		AddedOn:    addedOn,
		IsComplete: len(newTorrent.Files) > 0,
	}
	c.setTorrent(ct, func(torrent *CachedTorrent) {
		go c.RefreshListings(true)
	})

	// We can safely delete the old torrent here
	if oldID != "" {
		if err := c.DeleteTorrent(oldID); err != nil {
			return ct, fmt.Errorf("failed to delete old torrent: %w", err)
		}
	}

	req.Complete(ct, err)
	c.failedToReinsert.Delete(oldID) // Delete the old torrent from the failed list

	return ct, nil
}

func (c *Cache) resetInvalidLinks() {
	c.invalidDownloadLinks = xsync.NewMapOf[string, string]()
	c.client.ResetActiveDownloadKeys() // Reset the active download keys
	c.failedToReinsert = sync.Map{}    // Reset the failed to reinsert map
}
