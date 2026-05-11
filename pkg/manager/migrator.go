package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Migrator handles migration from cache JSON files to unified bbolt system
type Migrator struct {
	storage    *storage.Storage
	cacheDir   string
	backupPath string
	logger     zerolog.Logger
	mu         sync.RWMutex
	cancelFunc context.CancelFunc
	ctx        context.Context
}

// NewMigrator creates a new migrator
func NewMigrator(storage *storage.Storage) *Migrator {
	cacheDir := filepath.Join(config.GetMainPath(), "cache")
	backupPath := filepath.Join(config.GetMainPath(), "backups")

	return &Migrator{
		storage:    storage,
		cacheDir:   cacheDir,
		backupPath: backupPath,
		logger:     logger.New("migrator"),
	}
}

// Start starts the migration process from cache files
func (m *Migrator) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Load cache torrents
	cachedTorrents, err := m.loadCacheTorrents()
	if err != nil {
		return fmt.Errorf("failed to load cache torrents: %w", err)
	}

	// Initialize migration status
	status := &storage.SystemMigrationStatus{
		Running:   true,
		Total:     len(cachedTorrents),
		Completed: 0,
		Errors:    0,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
		ErrorList: []string{},
	}

	if err := m.storage.SaveMigrationStatus(status); err != nil {
		return fmt.Errorf("failed to save migration status: %w", err)
	}

	// Start migration in background
	ctx, cancel := context.WithCancel(context.Background())
	m.ctx = ctx
	m.cancelFunc = cancel

	m.runMigration(ctx, cachedTorrents)

	return nil
}

// Stop stops the migration process
func (m *Migrator) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cancelFunc != nil {
		m.cancelFunc()
		m.cancelFunc = nil
	}

	// Update status
	status, err := m.storage.GetMigrationStatus()
	if err != nil {
		return err
	}

	status.Running = false
	status.UpdatedAt = time.Now()

	return m.storage.SaveMigrationStatus(status)
}

// GetStatus returns the current migration status
func (m *Migrator) GetStatus() (*storage.SystemMigrationStatus, error) {
	return m.storage.GetMigrationStatus()
}

// GetStats returns migration statistics
func (m *Migrator) GetStats() (map[string]interface{}, error) {
	cachedTorrents, err := m.loadCacheTorrents()
	if err != nil {
		return nil, err
	}

	managedCount, err := m.storage.Count()
	if err != nil {
		return nil, err
	}

	// Count total cache files
	totalCacheFiles := 0
	for _, list := range cachedTorrents {
		totalCacheFiles += len(list)
	}

	return map[string]interface{}{
		"cache_torrents":     len(cachedTorrents),
		"cache_files":        totalCacheFiles,
		"managed_count":      managedCount,
		"multi_debrid_count": m.countMultiDebrid(cachedTorrents),
	}, nil
}

// countMultiDebrid counts how many torrents exist on multiple debrids
func (m *Migrator) countMultiDebrid(torrents map[string][]*storage.CachedTorrent) int {
	count := 0
	for _, list := range torrents {
		if len(list) > 1 {
			count++
		}
	}
	return count
}

// runMigration performs the actual migration
func (m *Migrator) runMigration(ctx context.Context, cachedTorrents map[string][]*storage.CachedTorrent) {
	m.logger.Info().Msg("Starting migration from cache files")

	status, _ := m.storage.GetMigrationStatus()

	for infohash, cachedList := range cachedTorrents {
		select {
		case <-ctx.Done():
			m.logger.Info().Msg("Migration stopped by user")
			return
		default:
		}

		// Check if already migrated
		exists, err := m.storage.Exists(infohash)
		if err != nil {
			m.logger.Error().Err(err).Str("infohash", infohash).Msg("Failed to check existence")
			status.Errors++
			continue
		}

		if exists {
			status.Completed++
			status.UpdatedAt = time.Now()
			_ = m.storage.SaveMigrationStatus(status)
			continue
		}

		// Merge cache torrents from multiple debrids
		managed, err := m.mergeCachedTorrents(cachedList)
		if err != nil {
			m.logger.Error().Err(err).
				Str("infohash", infohash).
				Int("count", len(cachedList)).
				Msg("Failed to merge cached torrents")
			status.Errors++
			status.ErrorList = append(status.ErrorList, fmt.Sprintf("Failed to merge %s: %v", infohash, err))
			continue
		}

		// Save to new storage
		if err := m.storage.AddOrUpdate(managed); err != nil {
			m.logger.Error().Err(err).Str("infohash", infohash).Msg("Failed to add managed torrent")
			status.Errors++
			status.ErrorList = append(status.ErrorList, fmt.Sprintf("Failed to add %s: %v", managed.Name, err))
			continue
		}
		status.Completed++
		status.UpdatedAt = time.Now()

		// Update status every 10 torrents
		if status.Completed%10 == 0 {
			if err := m.storage.SaveMigrationStatus(status); err != nil {
				m.logger.Error().Err(err).Msg("Failed to update migration status")
			}
		}
	}

	// Final status update
	status.Running = false
	status.UpdatedAt = time.Now()
	_ = m.storage.SaveMigrationStatus(status)

	m.logger.Info().
		Int("total", status.Total).
		Int("completed", status.Completed).
		Int("errors", status.Errors).
		Msg("Migration completed")
}

// loadCacheTorrents loads all torrents from cache directories and groups by infohash
func (m *Migrator) loadCacheTorrents() (map[string][]*storage.CachedTorrent, error) {
	// Map: infohash -> []*CachedTorrent (multiple debrids)
	torrentsByHash := make(map[string][]*storage.CachedTorrent)

	// Check if cache directory exists
	if _, err := os.Stat(m.cacheDir); os.IsNotExist(err) {
		return torrentsByHash, nil
	}

	// Read all debrid subdirectories
	debridDirs, err := os.ReadDir(m.cacheDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read cache directory: %w", err)
	}

	for _, debridDir := range debridDirs {
		if !debridDir.IsDir() {
			continue
		}

		debridName := debridDir.Name()
		debridPath := filepath.Join(m.cacheDir, debridName)

		// Read all JSON files in this debrid directory
		files, err := os.ReadDir(debridPath)
		if err != nil {
			m.logger.Error().Err(err).Str("path", debridPath).Msg("Failed to read debrid directory")
			continue
		}

		for _, file := range files {
			if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
				continue
			}

			filePath := filepath.Join(debridPath, file.Name())

			// Read and parse JSON
			data, err := os.ReadFile(filePath)
			if err != nil {
				m.logger.Error().Err(err).Str("file", filePath).Msg("Failed to read cache file")
				continue
			}

			var cached storage.CachedTorrent
			if err := json.Unmarshal(data, &cached); err != nil {
				m.logger.Error().Err(err).Str("file", filePath).Msg("Failed to unmarshal cache file")
				continue
			}

			// Validate required fields
			if cached.InfoHash == "" {
				m.logger.Warn().Str("file", filePath).Msg("Cache file missing info_hash, skipping")
				continue
			}

			// Ensure debrid field is set
			if cached.Debrid == "" {
				cached.Debrid = debridName
			}

			// Group by infohash
			torrentsByHash[cached.InfoHash] = append(torrentsByHash[cached.InfoHash], &cached)
		}
	}

	return torrentsByHash, nil
}

// mergeCachedTorrents merges multiple cache entries (from different debrids) into a single Entry
func (m *Migrator) mergeCachedTorrents(cachedList []*storage.CachedTorrent) (*storage.Entry, error) {
	if len(cachedList) == 0 {
		return nil, fmt.Errorf("empty cached list")
	}

	// Use first as base
	base := cachedList[0]
	managed := base.ToManagedTorrent()

	// AddOrUpdate placements from other debrids
	for i := 1; i < len(cachedList); i++ {
		other := cachedList[i]

		// Check if placement already exists for this debrid+infohash combo
		if _, exists := managed.Providers[other.Debrid]; exists {
			continue
		}

		// Parse timestamp
		addedAt, err := time.Parse(time.RFC3339, other.AddedOn)
		if err != nil {
			addedAt = time.Now()
		}

		// Determine placement status
		status := debridTypes.TorrentStatusDownloaded
		if other.Bad {
			status = debridTypes.TorrentStatusError
		} else if other.IsComplete {
			status = debridTypes.TorrentStatusDownloaded
		}

		// Create placement
		placement := &storage.ProviderEntry{
			Provider: other.Debrid,
			ID:       other.ID,
			AddedAt:  addedAt,
			Status:   status,
			Progress: other.Progress / 100.0,
			Files:    make(map[string]*storage.ProviderFile),
		}

		// Set downloaded timestamp if complete
		if other.IsComplete && other.Status == "downloaded" {
			downloadedAt := addedAt // Use added time as approximation
			placement.DownloadedAt = &downloadedAt
		}

		managed.Providers[other.Debrid] = placement

		// Merge files - add any files not in the base and populate placement files
		if other.Files != nil {
			for fileName, file := range other.Files {
				// AddOrUpdate to global files if not exists
				if _, exists := managed.Files[fileName]; !exists {
					managed.Files[fileName] = &storage.File{
						Name:      fileName,
						Size:      file.Size,
						ByteRange: file.ByteRange,
						Deleted:   file.Deleted,
						InfoHash:  other.InfoHash, // Track which torrent this file came from
						AddedOn:   addedAt,
					}
				}

				// AddOrUpdate placement-specific file data
				placement.Files[fileName] = &storage.ProviderFile{
					Id:   file.Id,
					Link: file.Link,
					Path: file.Path,
				}
			}
		}

		// Update size if other has larger size
		if other.Bytes > managed.Bytes {
			managed.Bytes = other.Bytes
			managed.Size = other.Bytes
		}
	}

	// Activate the most complete placement
	m.activateBestPlacement(managed)

	return managed, nil
}

// activateBestPlacement finds and activates the first placement that is completed
func (m *Migrator) activateBestPlacement(torrent *storage.Entry) {
	for debrid, placement := range torrent.Providers {
		if placement.Status == debridTypes.TorrentStatusDownloaded {
			torrent.ActiveProvider = debrid
			return
		}
	}
}
