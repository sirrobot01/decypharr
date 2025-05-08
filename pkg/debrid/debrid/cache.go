package debrid

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/goccy/go-json"
	"github.com/puzpuzpuz/xsync/v3"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
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
	AddedOn      time.Time `json:"added_on"`
	IsComplete   bool      `json:"is_complete"`
	DuplicateIds []string  `json:"duplicate_ids"`
}

type downloadLinkCache struct {
	Id        string
	Link      string
	AccountId string
	ExpiresAt time.Time
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

	torrents             *xsync.MapOf[string, string]         // key: torrent.Id, value: {torrent_folder_name}
	torrentsNames        *xsync.MapOf[string, *CachedTorrent] // key: torrent.Name, value: torrent
	listings             atomic.Value
	downloadLinks        *xsync.MapOf[string, downloadLinkCache]
	invalidDownloadLinks *xsync.MapOf[string, string]
	PropfindResp         *PropfindCache
	folderNaming         WebDavFolderNaming

	// monitors
	repairRequest    sync.Map
	failedToReinsert sync.Map

	// repair
	repairChan chan RepairRequest

	// config
	workers                       int
	torrentRefreshInterval        string
	downloadLinksRefreshInterval  string
	autoExpiresLinksAfterDuration time.Duration

	// refresh mutex
	listingRefreshMu       sync.RWMutex // for refreshing torrents
	downloadLinksRefreshMu sync.RWMutex // for refreshing download links
	torrentsRefreshMu      sync.RWMutex // for refreshing torrents

	scheduler gocron.Scheduler

	saveSemaphore chan struct{}
	ctx           context.Context
}

func New(dc config.Debrid, client types.Client) *Cache {
	cfg := config.Get()
	cet, _ := time.LoadLocation("CET")
	s, _ := gocron.NewScheduler(gocron.WithLocation(cet))

	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}
	return &Cache{
		dir:                           filepath.Join(cfg.Path, "cache", dc.Name), // path to save cache files
		torrents:                      xsync.NewMapOf[string, string](),
		torrentsNames:                 xsync.NewMapOf[string, *CachedTorrent](),
		invalidDownloadLinks:          xsync.NewMapOf[string, string](),
		PropfindResp:                  NewPropfindCache(),
		client:                        client,
		logger:                        logger.New(fmt.Sprintf("%s-webdav", client.GetName())),
		workers:                       dc.Workers,
		downloadLinks:                 xsync.NewMapOf[string, downloadLinkCache](),
		torrentRefreshInterval:        dc.TorrentsRefreshInterval,
		downloadLinksRefreshInterval:  dc.DownloadLinksRefreshInterval,
		folderNaming:                  WebDavFolderNaming(dc.FolderNaming),
		autoExpiresLinksAfterDuration: autoExpiresLinksAfter,
		saveSemaphore:                 make(chan struct{}, 50),
		ctx:                           context.Background(),
		scheduler:                     s,
	}
}

func (c *Cache) Start(ctx context.Context) error {
	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}
	c.ctx = ctx

	if err := c.Sync(); err != nil {
		return fmt.Errorf("failed to sync cache: %w", err)
	}

	// initial download links
	go func() {
		c.refreshDownloadLinks()
	}()

	go func() {
		err := c.StartSchedule()
		if err != nil {
			c.logger.Error().Err(err).Msg("Failed to start cache worker")
		}
	}()

	c.repairChan = make(chan RepairRequest, 100)
	go c.repairWorker()

	return nil
}

func (c *Cache) load() (map[string]*CachedTorrent, error) {
	torrents := make(map[string]*CachedTorrent)
	var results sync.Map

	if err := os.MkdirAll(c.dir, 0755); err != nil {
		return torrents, fmt.Errorf("failed to create cache directory: %w", err)
	}

	files, err := os.ReadDir(c.dir)
	if err != nil {
		return torrents, fmt.Errorf("failed to read cache directory: %w", err)
	}

	// Get only json files
	var jsonFiles []os.DirEntry
	for _, file := range files {
		if !file.IsDir() && filepath.Ext(file.Name()) == ".json" {
			jsonFiles = append(jsonFiles, file)
		}
	}

	if len(jsonFiles) == 0 {
		return torrents, nil
	}

	// Create channels with appropriate buffering
	workChan := make(chan os.DirEntry, min(c.workers, len(jsonFiles)))

	// Create a wait group for workers
	var wg sync.WaitGroup

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
				if len(ct.Files) != 0 {
					// Check if all files are valid, if not, delete the file.json and remove from cache.
					fs := make(map[string]types.File, len(ct.Files))
					for _, f := range ct.Files {
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
						results.Store(ct.Id, &ct)
					}
				}
			}
		}()
	}

	// Feed work to workers
	for _, file := range jsonFiles {
		workChan <- file
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	// Convert sync.Map to regular map
	results.Range(func(key, value interface{}) bool {
		id, _ := key.(string)
		torrent, _ := value.(*CachedTorrent)
		torrents[id] = torrent
		return true
	})

	return torrents, nil
}

func (c *Cache) Sync() error {
	defer c.logger.Info().Msg("WebDav server sync complete")
	cachedTorrents, err := c.load()
	if err != nil {
		c.logger.Error().Err(err).Msg("Failed to load cache")
	}

	torrents, err := c.client.GetTorrents()
	if err != nil {
		return fmt.Errorf("failed to sync torrents: %v", err)
	}

	c.logger.Info().Msgf("Got %d torrents from %s", len(torrents), c.client.GetName())

	newTorrents := make([]*types.Torrent, 0)
	idStore := make(map[string]string, len(torrents))
	for _, t := range torrents {
		idStore[t.Id] = t.Added
		if _, ok := cachedTorrents[t.Id]; !ok {
			newTorrents = append(newTorrents, t)
		}
	}

	// Check for deleted torrents
	deletedTorrents := make([]string, 0)
	for _, t := range cachedTorrents {
		t.Added = idStore[t.Id]
		if addedOn, err := time.Parse(time.RFC3339, t.Added); err == nil {
			t.AddedOn = addedOn
		}
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
		go c.RefreshListings(false)
	}) // This is set to false, cos it's likely rclone hs not started yet.
	c.logger.Info().Msgf("Loaded %d torrents from cache", len(cachedTorrents))

	if len(newTorrents) > 0 {
		c.logger.Info().Msgf("Found %d new torrents", len(newTorrents))
		if err := c.sync(newTorrents); err != nil {
			return fmt.Errorf("failed to sync torrents: %v", err)
		}
	}

	return nil
}

func (c *Cache) sync(torrents []*types.Torrent) error {

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

				case <-c.ctx.Done():
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
		case <-c.ctx.Done():
			break // Context cancelled
		}
	}

	// Signal workers that no more work is coming
	close(workChan)

	// Wait for all workers to complete
	wg.Wait()

	c.RefreshListings(true) // final refresh
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

func (c *Cache) setTorrent(t *CachedTorrent, callback func(torrent *CachedTorrent)) {
	torrentKey := c.GetTorrentFolder(t.Torrent)
	c.torrents.Store(t.Id, torrentKey) // Store the torrent id with the folder name(we might change the id after,  hence why it's stored here)
	if o, ok := c.torrentsNames.Load(torrentKey); ok && o.Id != t.Id {
		// If another torrent with the same name exists, merge the files, if the same file exists,
		// keep the one with the most recent added date

		// Save the most recent torrent
		mergedFiles := mergeFiles(t, o) // Useful for merging files across multiple torrents, while keeping the most recent
		if o.AddedOn.After(t.AddedOn) {
			t = o
		}
		t.Files = mergedFiles

	}
	c.torrentsNames.Store(torrentKey, t)
	c.SaveTorrent(t)
	if callback != nil {
		callback(t)
	}
}

func (c *Cache) setTorrents(torrents map[string]*CachedTorrent, callback func()) {
	for _, t := range torrents {
		torrentKey := c.GetTorrentFolder(t.Torrent)
		c.torrents.Store(t.Id, torrentKey)
		if o, ok := c.torrentsNames.Load(torrentKey); ok && o.Id != t.Id {
			// Save the most recent torrent
			mergedFiles := mergeFiles(t, o) // Useful for merging files across multiple torrents, while keeping the most recent
			if o.AddedOn.After(t.AddedOn) {
				t = o
			}
			t.Files = mergedFiles
		}
		c.torrentsNames.Store(torrentKey, t)
	}
	c.SaveTorrents()
	if callback != nil {
		callback()
	}
}

func (c *Cache) GetListing() []os.FileInfo {
	if v, ok := c.listings.Load().([]os.FileInfo); ok {
		return v
	}
	return nil
}

func (c *Cache) Close() error {
	return nil
}

func (c *Cache) GetTorrents() map[string]*CachedTorrent {
	torrents := make(map[string]*CachedTorrent)
	c.torrentsNames.Range(func(key string, value *CachedTorrent) bool {
		torrents[key] = value
		return true
	})
	return torrents
}

func (c *Cache) GetTorrentByName(name string) *CachedTorrent {
	if t, ok := c.torrentsNames.Load(name); ok {
		return t
	}
	return nil
}

func (c *Cache) GetTorrent(torrentId string) *CachedTorrent {
	if name, ok := c.torrents.Load(torrentId); ok {
		if t, ok := c.torrentsNames.Load(name); ok {
			return t
		}
		return nil
	}
	return nil
}

func (c *Cache) SaveTorrents() {
	c.torrentsNames.Range(func(key string, value *CachedTorrent) bool {
		c.SaveTorrent(value)
		return true
	})
}

func (c *Cache) SaveTorrent(ct *CachedTorrent) {
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
		ct := &CachedTorrent{
			Torrent:    t,
			IsComplete: len(t.Files) > 0,
			AddedOn:    addedOn,
		}
		c.setTorrent(ct, nil)
	}
	return nil
}

func (c *Cache) AddTorrent(t *types.Torrent) error {
	if len(t.Files) == 0 {
		if err := c.client.UpdateTorrent(t); err != nil {
			return fmt.Errorf("failed to update torrent: %w", err)
		}
	}
	addedOn, err := time.Parse(time.RFC3339, t.Added)
	if err != nil {
		addedOn = time.Now()
	}
	ct := &CachedTorrent{
		Torrent:    t,
		IsComplete: len(t.Files) > 0,
		AddedOn:    addedOn,
	}
	c.setTorrent(ct, func(tor *CachedTorrent) {
		go c.RefreshListings(true)
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
		c.RefreshListings(true)
		c.logger.Trace().Msgf("Torrent %s deleted successfully", id)
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
	c.RefreshListings(true)
}

// deleteTorrent deletes the torrent from the cache and debrid service
// It also handles torrents with the same name but different IDs
func (c *Cache) deleteTorrent(id string, removeFromDebrid bool) bool {

	if torrentName, ok := c.torrents.Load(id); ok {
		c.torrents.Delete(id) // Delete id from cache
		defer func() {
			c.removeFromDB(id)
			if removeFromDebrid {
				_ = c.client.DeleteTorrent(id) // Skip error handling, we don't care if it fails
			}
		}() // defer delete from debrid

		if t, ok := c.torrentsNames.Load(torrentName); ok {

			newFiles := map[string]types.File{}
			newId := ""
			for _, file := range t.Files {
				if file.TorrentId != "" && file.TorrentId != id {
					if newId == "" && file.TorrentId != "" {
						newId = file.TorrentId
					}
					newFiles[file.Name] = file
				}
			}
			if len(newFiles) == 0 {
				// Delete the torrent since no files are left
				c.torrentsNames.Delete(torrentName)
			} else {
				t.Files = newFiles
				newId = cmp.Or(newId, t.Id)
				t.Id = newId
				c.setTorrent(t, nil)
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
	c.RefreshListings(true)
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

func (c *Cache) GetLogger() zerolog.Logger {
	return c.logger
}
