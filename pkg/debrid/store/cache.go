package store

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"encoding/json"
	"github.com/go-co-op/gocron/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	_ "time/tzdata"
)

type WebDavFolderNaming string

const (
	WebDavUseFileName          WebDavFolderNaming = "filename"
	WebDavUseOriginalName      WebDavFolderNaming = "original"
	WebDavUseFileNameNoExt     WebDavFolderNaming = "filename_no_ext"
	WebDavUseOriginalNameNoExt WebDavFolderNaming = "original_no_ext"
	WebDavUseID                WebDavFolderNaming = "id"
	WebdavUseHash              WebDavFolderNaming = "infohash"
)

type CachedTorrent struct {
	*types.Torrent
	AddedOn    time.Time `json:"added_on"`
	IsComplete bool      `json:"is_complete"`
	Bad        bool      `json:"bad"`
}

func (c CachedTorrent) copy() CachedTorrent {
	return CachedTorrent{
		Torrent:    c.Torrent,
		AddedOn:    c.AddedOn,
		IsComplete: c.IsComplete,
		Bad:        c.Bad,
	}
}

type RepairType string

const (
	RepairTypeReinsert RepairType = "reinsert"
	RepairTypeDelete   RepairType = "delete"
)

type RepairRequest struct {
	Type      RepairType
	TorrentID string
	Priority  int
	FileName  string
}

type Cache struct {
	dir    string
	client types.Client
	logger zerolog.Logger

	torrents             *torrentCache
	downloadLinks        *downloadLinkCache
	invalidDownloadLinks sync.Map
	folderNaming         WebDavFolderNaming

	listingDebouncer *utils.Debouncer[bool]
	// monitors
	repairRequest        sync.Map
	failedToReinsert     sync.Map
	downloadLinkRequests sync.Map

	// repair
	repairChan chan RepairRequest

	// readiness
	ready chan struct{}

	// config
	workers                       int
	torrentRefreshInterval        string
	downloadLinksRefreshInterval  string
	autoExpiresLinksAfterDuration time.Duration

	// refresh mutex
	downloadLinksRefreshMu sync.RWMutex // for refreshing download links
	torrentsRefreshMu      sync.RWMutex // for refreshing torrents

	scheduler    gocron.Scheduler
	cetScheduler gocron.Scheduler

	saveSemaphore chan struct{}

	config        config.Debrid
	customFolders []string
}

func NewDebridCache(dc config.Debrid, client types.Client) *Cache {
	cfg := config.Get()
	cetSc, err := gocron.NewScheduler(gocron.WithLocation(time.UTC))
	if err != nil {
		// If we can't create a CET scheduler, fallback to local time
		cetSc, _ = gocron.NewScheduler(gocron.WithLocation(time.Local))
	}
	scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.Local))
	if err != nil {
		// If we can't create a local scheduler, fallback to CET
		scheduler = cetSc
	}

	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}
	var customFolders []string
	dirFilters := map[string][]directoryFilter{}
	for name, value := range dc.Directories {
		for filterType, v := range value.Filters {
			df := directoryFilter{filterType: filterType, value: v}
			switch filterType {
			case filterByRegex, filterByNotRegex:
				df.regex = regexp.MustCompile(v)
			case filterBySizeGT, filterBySizeLT:
				df.sizeThreshold, _ = config.ParseSize(v)
			case filterBLastAdded:
				df.ageThreshold, _ = time.ParseDuration(v)
			}
			dirFilters[name] = append(dirFilters[name], df)
		}
		customFolders = append(customFolders, name)

	}
	_log := logger.New(fmt.Sprintf("%s-webdav", client.GetName()))
	c := &Cache{
		dir: filepath.Join(cfg.Path, "cache", dc.Name), // path to save cache files

		torrents:                      newTorrentCache(dirFilters),
		client:                        client,
		logger:                        _log,
		workers:                       dc.Workers,
		downloadLinks:                 newDownloadLinkCache(),
		torrentRefreshInterval:        dc.TorrentsRefreshInterval,
		downloadLinksRefreshInterval:  dc.DownloadLinksRefreshInterval,
		folderNaming:                  WebDavFolderNaming(dc.FolderNaming),
		autoExpiresLinksAfterDuration: autoExpiresLinksAfter,
		saveSemaphore:                 make(chan struct{}, 50),
		cetScheduler:                  cetSc,
		scheduler:                     scheduler,

		config:        dc,
		customFolders: customFolders,

		ready: make(chan struct{}),
	}

	c.listingDebouncer = utils.NewDebouncer[bool](100*time.Millisecond, func(refreshRclone bool) {
		c.RefreshListings(refreshRclone)
	})
	return c
}

func (c *Cache) IsReady() chan struct{} {
	return c.ready
}

func (c *Cache) StreamWithRclone() bool {
	return c.config.ServeFromRclone
}

// Reset clears all internal state so the Cache can be reused without leaks.
// Call this after stopping the old Cache (so no goroutines are holding references),
// and before you discard the instance on a restart.
func (c *Cache) Reset() {

	if err := c.scheduler.StopJobs(); err != nil {
		c.logger.Error().Err(err).Msg("Failed to stop scheduler jobs")
	}

	if err := c.scheduler.Shutdown(); err != nil {
		c.logger.Error().Err(err).Msg("Failed to stop scheduler")
	}

	// Stop the listing debouncer
	c.listingDebouncer.Stop()

	// Close the repair channel
	close(c.repairChan)

	// 1. Reset torrent storage
	c.torrents.reset()

	// 2. Reset download-link cache
	c.downloadLinks.reset()

	// 3. Clear any sync.Maps
	c.invalidDownloadLinks = sync.Map{}
	c.repairRequest = sync.Map{}
	c.failedToReinsert = sync.Map{}
	c.downloadLinkRequests = sync.Map{}

	// 5. Rebuild the listing debouncer
	c.listingDebouncer = utils.NewDebouncer[bool](
		100*time.Millisecond,
		func(refreshRclone bool) {
			c.RefreshListings(refreshRclone)
		},
	)

	// 6. Reset repair channel so the next Start() can spin it up
	c.repairChan = make(chan RepairRequest, 100)
}

func (c *Cache) Start(ctx context.Context) error {
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	if err := c.Sync(ctx); err != nil {
		return fmt.Errorf("failed to sync cache: %w", err)
	}

	// initial download links
	go c.refreshDownloadLinks(ctx)

	if err := c.StartSchedule(ctx); err != nil {
		c.logger.Error().Err(err).Msg("Failed to start cache worker")
	}

	c.repairChan = make(chan RepairRequest, 100)
	go c.repairWorker(ctx)

	// Fire the ready channel
	close(c.ready)
	cfg := config.Get()
	name := c.client.GetName()
	addr := cfg.BindAddress + ":" + cfg.Port + cfg.URLBase + "webdav/" + name + "/"
	c.logger.Info().Msgf("%s WebDav server running at %s", name, addr)

	<-ctx.Done()
	c.logger.Info().Msgf("Stopping %s WebDav server", name)
	c.Reset()

	return nil
}

func (c *Cache) load(ctx context.Context) (map[string]CachedTorrent, error) {
	mu := sync.Mutex{}

	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	files, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	// Get only json files
	var jsonFiles []os.DirEntry
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			jsonFiles = append(jsonFiles, file)
		}
	}

	if len(jsonFiles) == 0 {
		return nil, nil
	}

	// Create channels with appropriate buffering
	workChan := make(chan os.DirEntry, min(c.workers, len(jsonFiles)))

	// Create a wait group for workers
	var wg sync.WaitGroup

	torrents := make(map[string]CachedTorrent, len(jsonFiles))

	// Start workers
	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			for {
				file, ok := <-workChan
				if !ok {
					return // Channel closed, exit goroutine
				}

				fileName := file.Name()
				filePath := filepath.Join(c.dir, fileName)
				data, err := os.ReadFile(filePath)
				if err != nil {
					c.logger.Error().Err(err).Msgf("Failed to read file: %s", filePath)
					continue
				}

				var ct CachedTorrent
				if err := json.Unmarshal(data, &ct); err != nil {
					c.logger.Error().Err(err).Msgf("Failed to unmarshal file: %s", filePath)
					continue
				}

				isComplete := true
				if len(ct.GetFiles()) != 0 {
					// Check if all files are valid, if not, delete the file.json and remove from cache.
					fs := make(map[string]types.File, len(ct.GetFiles()))
					for _, f := range ct.GetFiles() {
						if f.Link == "" {
							isComplete = false
							break
						}
						f.TorrentId = ct.Id
						fs[f.Name] = f
					}

					if isComplete {

						if addedOn, err := time.Parse(time.RFC3339, ct.Added); err == nil {
							ct.AddedOn = addedOn
						}
						ct.IsComplete = true
						ct.Files = fs
						ct.Name = path.Clean(ct.Name)
						mu.Lock()
						torrents[ct.Id] = ct
						mu.Unlock()
					}
				}
			}
		}()
	}

	// Feed work to workers
	for _, file := range jsonFiles {
		select {
		case <-ctx.Done():
			break // Context cancelled
		default:
			workChan <- file
		}
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	return torrents, nil
}

func (c *Cache) Sync(ctx context.Context) error {
	cachedTorrents, err := c.load(ctx)
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to load cache")
	}

	torrents, err := c.client.GetTorrents()
	if err != nil {
		return fmt.Errorf("failed to sync torrents: %v", err)
	}

	totalTorrents := len(torrents)

	c.logger.Info().Msgf("%d torrents found from %s", totalTorrents, c.client.GetName())

	newTorrents := make([]*types.Torrent, 0)
	idStore := make(map[string]struct{}, totalTorrents)
	for _, t := range torrents {
		idStore[t.Id] = struct{}{}
		if _, ok := cachedTorrents[t.Id]; !ok {
			newTorrents = append(newTorrents, t)
		}
	}

	// Check for deleted torrents
	deletedTorrents := make([]string, 0)
	for _, t := range cachedTorrents {
		if _, ok := idStore[t.Id]; !ok {
			deletedTorrents = append(deletedTorrents, t.Id)
		}
	}

	if len(deletedTorrents) > 0 {
		c.logger.Info().Msgf("Found %d deleted torrents", len(deletedTorrents))
		for _, id := range deletedTorrents {
			if _, ok := cachedTorrents[id]; ok {
				c.deleteTorrent(id, false) // delete from cache
			}
		}
	}

	// Write these torrents to the cache
	c.setTorrents(cachedTorrents, func() {
		c.listingDebouncer.Call(false)
	}) // Initial calls
	c.logger.Info().Msgf("Loaded %d torrents from cache", len(cachedTorrents))

	if len(newTorrents) > 0 {
		c.logger.Info().Msgf("Found %d new torrents", len(newTorrents))
		if err := c.sync(ctx, newTorrents); err != nil {
			return fmt.Errorf("failed to sync torrents: %v", err)
		}
	}

	return nil
}

func (c *Cache) sync(ctx context.Context, torrents []*types.Torrent) error {

	// Create channels with appropriate buffering
	workChan := make(chan *types.Torrent, min(c.workers, len(torrents)))

	// Use an atomic counter for progress tracking
	var processed int64
	var errorCount int64

	// Create a wait group for workers
	var wg sync.WaitGroup

	// Start workers
	for i := 0; i < c.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case t, ok := <-workChan:
					if !ok {
						return // Channel closed, exit goroutine
					}

					if err := c.ProcessTorrent(t); err != nil {
						c.logger.Error().Err(err).Str("torrent", t.Name).Msg("sync error")
						atomic.AddInt64(&errorCount, 1)
					}

					count := atomic.AddInt64(&processed, 1)
					if count%1000 == 0 {
						c.logger.Info().Msgf("Progress: %d/%d torrents processed", count, len(torrents))
					}

				case <-ctx.Done():
					return // Context cancelled, exit goroutine
				}
			}
		}()
	}

	// Feed work to workers
	for _, t := range torrents {
		select {
		case workChan <- t:
			// Work sent successfully
		case <-ctx.Done():
			break // Context cancelled
		}
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	c.listingDebouncer.Call(false) // final refresh
	c.logger.Info().Msgf("Sync complete: %d torrents processed, %d errors", len(torrents), errorCount)
	return nil
}

func (c *Cache) GetTorrentFolder(torrent *types.Torrent) string {
	switch c.folderNaming {
	case WebDavUseFileName:
		return path.Clean(torrent.Filename)
	case WebDavUseOriginalName:
		return path.Clean(torrent.OriginalFilename)
	case WebDavUseFileNameNoExt:
		return path.Clean(utils.RemoveExtension(torrent.Filename))
	case WebDavUseOriginalNameNoExt:
		return path.Clean(utils.RemoveExtension(torrent.OriginalFilename))
	case WebDavUseID:
		return torrent.Id
	case WebdavUseHash:
		return strings.ToLower(torrent.InfoHash)
	default:
		return path.Clean(torrent.Filename)
	}
}

func (c *Cache) setTorrent(t CachedTorrent, callback func(torrent CachedTorrent)) {
	torrentName := c.GetTorrentFolder(t.Torrent)
	updatedTorrent := t.copy()
	if o, ok := c.torrents.getByName(torrentName); ok && o.Id != t.Id {
		// If another torrent with the same name exists, merge the files, if the same file exists,
		// keep the one with the most recent added date

		// Save the most recent torrent
		mergedFiles := mergeFiles(o, updatedTorrent) // Useful for merging files across multiple torrents, while keeping the most recent
		updatedTorrent.Files = mergedFiles
	}
	c.torrents.set(torrentName, t, updatedTorrent)
	c.SaveTorrent(t)
	if callback != nil {
		callback(updatedTorrent)
	}
}

func (c *Cache) setTorrents(torrents map[string]CachedTorrent, callback func()) {
	for _, t := range torrents {
		torrentName := c.GetTorrentFolder(t.Torrent)
		updatedTorrent := t.copy()
		if o, ok := c.torrents.getByName(torrentName); ok && o.Id != t.Id {
			// Save the most recent torrent
			mergedFiles := mergeFiles(o, updatedTorrent)
			updatedTorrent.Files = mergedFiles
		}
		c.torrents.set(torrentName, t, updatedTorrent)
	}
	c.SaveTorrents()
	if callback != nil {
		callback()
	}
}

// GetListing returns a sorted list of torrents(READ-ONLY)
func (c *Cache) GetListing(folder string) []os.FileInfo {
	switch folder {
	case "__all__", "torrents":
		return c.torrents.getListing()
	default:
		return c.torrents.getFolderListing(folder)
	}
}

func (c *Cache) GetCustomFolders() []string {
	return c.customFolders
}

func (c *Cache) Close() error {
	return nil
}

func (c *Cache) GetTorrents() map[string]CachedTorrent {
	return c.torrents.getAll()
}

func (c *Cache) GetTorrentByName(name string) *CachedTorrent {
	if torrent, ok := c.torrents.getByName(name); ok {
		return &torrent
	}
	return nil
}

func (c *Cache) GetTorrent(torrentId string) *CachedTorrent {
	if torrent, ok := c.torrents.getByID(torrentId); ok {
		return &torrent
	}
	return nil
}

func (c *Cache) SaveTorrents() {
	torrents := c.torrents.getAll()
	for _, torrent := range torrents {
		c.SaveTorrent(torrent)
	}
}

func (c *Cache) SaveTorrent(ct CachedTorrent) {
	marshaled, err := json.MarshalIndent(ct, "", "  ")
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to marshal torrent: %s", ct.Id)
		return
	}

	// Store just the essential info needed for the file operation
	saveInfo := struct {
		id       string
		jsonData []byte
	}{
		id:       ct.Torrent.Id,
		jsonData: marshaled,
	}

	// Try to acquire semaphore without blocking
	select {
	case c.saveSemaphore <- struct{}{}:
		go func() {
			defer func() { <-c.saveSemaphore }()
			c.saveTorrent(saveInfo.id, saveInfo.jsonData)
		}()
	default:
		c.saveTorrent(saveInfo.id, saveInfo.jsonData)
	}
}

func (c *Cache) saveTorrent(id string, data []byte) {

	fileName := id + ".json"
	filePath := filepath.Join(c.dir, fileName)

	// Use a unique temporary filename for concurrent safety
	tmpFile := filePath + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)

	f, err := os.Create(tmpFile)
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to create file: %s", tmpFile)
		return
	}

	// Track if we've closed the file
	fileClosed := false
	defer func() {
		// Only close if not already closed
		if !fileClosed {
			_ = f.Close()
		}
		// Clean up the temp file if it still exists and rename failed
		_ = os.Remove(tmpFile)
	}()

	w := bufio.NewWriter(f)
	if _, err := w.Write(data); err != nil {
		c.logger.Error().Err(err).Msgf("Failed to write data: %s", tmpFile)
		return
	}

	if err := w.Flush(); err != nil {
		c.logger.Error().Err(err).Msgf("Failed to flush data: %s", tmpFile)
		return
	}

	// Close the file before renaming
	_ = f.Close()
	fileClosed = true

	if err := os.Rename(tmpFile, filePath); err != nil {
		c.logger.Error().Err(err).Msgf("Failed to rename file: %s", tmpFile)
		return
	}
}

func (c *Cache) ProcessTorrent(t *types.Torrent) error {

	isComplete := func(files map[string]types.File) bool {
		_complete := len(files) > 0
		for _, file := range files {
			if file.Link == "" {
				_complete = false
				break
			}
		}
		return _complete
	}

	if !isComplete(t.Files) {
		if err := c.client.UpdateTorrent(t); err != nil {
			return fmt.Errorf("failed to update torrent: %w", err)
		}
	}

	if !isComplete(t.Files) {
		c.logger.Debug().Msgf("Torrent %s is still not complete. Triggering a reinsert(disabled)", t.Id)
	} else {
		addedOn, err := time.Parse(time.RFC3339, t.Added)
		if err != nil {
			addedOn = time.Now()
		}
		ct := CachedTorrent{
			Torrent:    t,
			IsComplete: len(t.Files) > 0,
			AddedOn:    addedOn,
		}
		c.setTorrent(ct, func(tor CachedTorrent) {
			c.listingDebouncer.Call(false)
		})
	}
	return nil
}

func (c *Cache) Add(t *types.Torrent) error {
	if len(t.Files) == 0 {
		if err := c.client.UpdateTorrent(t); err != nil {
			return fmt.Errorf("failed to update torrent: %w", err)
		}
	}
	addedOn, err := time.Parse(time.RFC3339, t.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := CachedTorrent{
		Torrent:    t,
		IsComplete: len(t.Files) > 0,
		AddedOn:    addedOn,
	}
	c.setTorrent(ct, func(tor CachedTorrent) {
		c.RefreshListings(true)
	})
	go c.GenerateDownloadLinks(ct)
	return nil

}

func (c *Cache) GetClient() types.Client {
	return c.client
}

func (c *Cache) DeleteTorrent(id string) error {
	c.torrentsRefreshMu.Lock()
	defer c.torrentsRefreshMu.Unlock()

	if c.deleteTorrent(id, true) {
		go c.RefreshListings(true)
		return nil
	}
	return nil
}

func (c *Cache) validateAndDeleteTorrents(torrents []string) {
	wg := sync.WaitGroup{}
	for _, torrent := range torrents {
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			// Check if torrent is truly deleted
			if _, err := c.client.GetTorrent(t); err != nil {
				c.deleteTorrent(t, false) // Since it's removed from debrid already
			}
		}(torrent)
	}
	wg.Wait()
	c.listingDebouncer.Call(true)
}

// deleteTorrent deletes the torrent from the cache and debrid service
// It also handles torrents with the same name but different IDs
func (c *Cache) deleteTorrent(id string, removeFromDebrid bool) bool {

	if torrent, ok := c.torrents.getByID(id); ok {
		c.torrents.removeId(id) // Delete id from cache
		defer func() {
			c.removeFromDB(id)
			if removeFromDebrid {
				_ = c.client.DeleteTorrent(id) // Skip error handling, we don't care if it fails
			}
		}() // defer delete from debrid

		torrentName := torrent.Name

		if t, ok := c.torrents.getByName(torrentName); ok {

			newFiles := map[string]types.File{}
			newId := ""
			for _, file := range t.GetFiles() {
				if file.TorrentId != "" && file.TorrentId != id {
					if newId == "" && file.TorrentId != "" {
						newId = file.TorrentId
					}
					newFiles[file.Name] = file
				}
			}
			if len(newFiles) == 0 {
				// Delete the torrent since no files are left
				c.torrents.remove(torrentName)
			} else {
				t.Files = newFiles
				newId = cmp.Or(newId, t.Id)
				t.Id = newId
				c.setTorrent(t, nil) // This gets called after calling deleteTorrent
			}
		}
		return true
	}
	return false
}

func (c *Cache) DeleteTorrents(ids []string) {
	c.logger.Info().Msgf("Deleting %d torrents", len(ids))
	for _, id := range ids {
		_ = c.deleteTorrent(id, true)
	}
	c.listingDebouncer.Call(true)
}

func (c *Cache) removeFromDB(torrentId string) {
	// Moves the torrent file to the trash
	filePath := filepath.Join(c.dir, torrentId+".json")

	// Check if the file exists
	if _, err := os.Stat(filePath); errors.Is(err, os.ErrNotExist) {
		return
	}

	// Move the file to the trash
	trashPath := filepath.Join(c.dir, "trash", torrentId+".json")
	if err := os.MkdirAll(filepath.Dir(trashPath), 0755); err != nil {
		return
	}
	if err := os.Rename(filePath, trashPath); err != nil {
		return
	}
}

func (c *Cache) OnRemove(torrentId string) {
	c.logger.Debug().Msgf("OnRemove triggered for %s", torrentId)
	err := c.DeleteTorrent(torrentId)
	if err != nil {
		c.logger.Error().Err(err).Msgf("Failed to delete torrent: %s", torrentId)
		return
	}
}

// RemoveFile removes a file from the torrent cache
// TODO sends a re-insert that removes the file from debrid
func (c *Cache) RemoveFile(torrentId string, filename string) error {
	c.logger.Debug().Str("torrent_id", torrentId).Msgf("Removing file %s", filename)
	torrent, ok := c.torrents.getByID(torrentId)
	if !ok {
		return fmt.Errorf("torrent %s not found", torrentId)
	}
	file, ok := torrent.GetFile(filename)
	if !ok {
		return fmt.Errorf("file %s not found in torrent %s", filename, torrentId)
	}
	file.Deleted = true
	torrent.Files[filename] = file

	// If the torrent has no files left, delete it
	if len(torrent.GetFiles()) == 0 {
		c.logger.Debug().Msgf("Torrent %s has no files left, deleting it", torrentId)
		if err := c.DeleteTorrent(torrentId); err != nil {
			return fmt.Errorf("failed to delete torrent %s: %w", torrentId, err)
		}
		return nil
	}

	c.setTorrent(torrent, func(torrent CachedTorrent) {
		c.listingDebouncer.Call(true)
	}) // Update the torrent in the cache
	return nil
}

func (c *Cache) GetLogger() zerolog.Logger {
	return c.logger
}
