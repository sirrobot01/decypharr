package store

import (
	"context"
	"errors"
	"fmt"
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

func (c *Cache) markAsFailedToReinsert(torrentId string) {
	c.failedToReinsert.Store(torrentId, struct{}{})

	// Remove the torrent from the directory if it has failed to reinsert, max retries are hardcoded to 5
	if torrent, ok := c.torrents.getByID(torrentId); ok {
		torrent.Bad = true
		c.setTorrent(torrent, func(t CachedTorrent) {
			c.RefreshListings(false)
		})
	}
}

func (c *Cache) markAsSuccessfullyReinserted(torrentId string) {
	if _, ok := c.failedToReinsert.Load(torrentId); !ok {
		return
	}
	c.failedToReinsert.Delete(torrentId)
	if torrent, ok := c.torrents.getByID(torrentId); ok {
		torrent.Bad = false
		c.setTorrent(torrent, func(torrent CachedTorrent) {
			c.RefreshListings(false)
		})
	}
}

func (c *Cache) GetBrokenFiles(t *CachedTorrent, filenames []string) []string {
	files := make(map[string]types.File)
	brokenFiles := make([]string, 0)
	if len(filenames) > 0 {
		for name, f := range t.Files {
			if utils.Contains(filenames, name) {
				files[name] = f
			}
		}
	} else {
		files = t.Files
	}
	for _, f := range files {
		// Check if file is missing
		if f.Link == "" {
			// refresh torrent and then break
			if newT := c.refreshTorrent(f.TorrentId); newT != nil {
				t = newT
			} else {
				c.logger.Error().Str("torrentId", t.Torrent.Id).Msg("Failed to refresh torrent")
				return filenames // Return original filenames if refresh fails(torrent is somehow botched)
			}
		}
	}

	if t.Torrent == nil {
		c.logger.Error().Str("torrentId", t.Torrent.Id).Msg("Failed to refresh torrent")
		return filenames // Return original filenames if refresh fails(torrent is somehow botched)
	}

	files = t.Files

	var wg sync.WaitGroup

	wg.Add(len(files))

	for _, f := range files {
		// Check if file link is still missing
		go func(f types.File) {
			defer wg.Done()
			if f.Link == "" {
				brokenFiles = append(brokenFiles, f.Name)
			} else {
				// Check if file.Link not in the downloadLink Cache
				if err := c.client.CheckLink(f.Link); err != nil {
					if errors.Is(err, utils.HosterUnavailableError) {
						brokenFiles = append(brokenFiles, f.Name)
					}
				}
			}
		}(f)
	}

	wg.Wait()

	// Try to reinsert the torrent if it's broken
	if len(brokenFiles) > 0 && t.Torrent != nil {
		// Check if the torrent is already in progress
		if _, err := c.reInsertTorrent(t); err != nil {
			c.logger.Error().Err(err).Str("torrentId", t.Torrent.Id).Msg("Failed to reinsert torrent")
			return brokenFiles // Return broken files if reinsert fails
		}
		return nil // Return nil if the torrent was successfully reinserted
	}

	return brokenFiles
}

func (c *Cache) repairWorker(ctx context.Context) {
	// This watches a channel for torrents to repair and can be cancelled via context
	for {
		select {
		case <-ctx.Done():
			return

		case req, ok := <-c.repairChan:
			// Channel was closed
			if !ok {
				c.logger.Debug().Msg("Repair channel closed, shutting down worker")
				return
			}

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
		c.markAsFailedToReinsert(oldID)
		// Remove the old torrent from the cache and debrid service
		return ct, fmt.Errorf("failed to submit magnet: %w", err)
	}

	// Check if the torrent was submitted
	if newTorrent == nil || newTorrent.Id == "" {
		c.markAsFailedToReinsert(oldID)
		return ct, fmt.Errorf("failed to submit magnet: empty torrent")
	}
	newTorrent.DownloadUncached = false // Set to false, avoid re-downloading
	newTorrent, err = c.client.CheckStatus(newTorrent)
	if err != nil {
		if newTorrent != nil && newTorrent.Id != "" {
			// Delete the torrent if it was not downloaded
			_ = c.client.DeleteTorrent(newTorrent.Id)
		}
		c.markAsFailedToReinsert(oldID)
		return ct, err
	}

	// Update the torrent in the cache
	addedOn, err := time.Parse(time.RFC3339, newTorrent.Added)
	if err != nil {
		addedOn = time.Now()
	}
	for _, f := range newTorrent.GetFiles() {
		if f.Link == "" {
			c.markAsFailedToReinsert(oldID)
			return ct, fmt.Errorf("failed to reinsert torrent: empty link")
		}
	}
	// Set torrent to newTorrent
	newCt := CachedTorrent{
		Torrent:    newTorrent,
		AddedOn:    addedOn,
		IsComplete: len(newTorrent.Files) > 0,
	}
	c.setTorrent(newCt, func(torrent CachedTorrent) {
		c.RefreshListings(true)
	})

	ct = &newCt // Update ct to point to the new torrent

	// We can safely delete the old torrent here
	if oldID != "" {
		if err := c.DeleteTorrent(oldID); err != nil {
			return ct, fmt.Errorf("failed to delete old torrent: %w", err)
		}
	}

	req.Complete(ct, err)
	c.markAsSuccessfullyReinserted(oldID)

	c.logger.Debug().Str("torrentId", torrent.Id).Msg("Torrent successfully reinserted")

	return ct, nil
}

func (c *Cache) resetInvalidLinks(ctx context.Context) {
	c.logger.Debug().Msgf("Resetting accounts")
	c.invalidDownloadLinks = sync.Map{}
	c.client.Accounts().Reset() // Reset the active download keys

	// Refresh the download links
	c.refreshDownloadLinks(ctx)
}
