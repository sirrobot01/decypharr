package manager

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/manager/link"
	"github.com/sirrobot01/decypharr/pkg/notifications"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"github.com/sirrobot01/decypharr/pkg/version"
	"golang.org/x/sync/singleflight"
)

// Manager handles unified torrent management - replaces wire.Store completely
type Manager struct {
	storage      *storage.Storage
	migrator     *Migrator
	repair       RepairManager
	clients      *xsync.Map[string, debrid.Client]
	arr          *arr.Storage
	logger       zerolog.Logger
	ready        chan struct{}
	readyOnce    sync.Once
	streamClient *http.Client

	// Migration jobs tracking
	migrationJobs   *xsync.Map[string, *storage.SwitcherJob]
	refreshInterval time.Duration

	config *config.Config

	// Processing workers
	scheduler    gocron.Scheduler
	cetScheduler gocron.Scheduler
	queue        *Queue

	// downloading
	refreshSG   singleflight.Group
	linkService *link.Service

	// repair
	fixer *Fixer
	ctx   context.Context

	customFolders *CustomFolders
	mountManager  MountManager

	startTime     time.Time
	usenetTimeout time.Duration

	rootInfo       *FileInfo
	entry          *EntryCache
	downloader     *Downloader
	usenet         *usenet.Usenet

	// Debrid speed test results storage
	debridSpeedTestResults *xsync.Map[string, debridTypes.SpeedTestResult]

	// Active streams tracking
	activeStreams *xsync.Map[string, *ActiveStream]

	// NZB processing worker pool (unbounded queue)
	nzbQueue      *nzbJobQueue
	nzbWorkerStop chan struct{} // Signal to stop workers

	// Notifications service
	Notifications *notifications.Service
}

// New creates a new Manager instance
func New() *Manager {
	cfg := config.Get()
	_logger := logger.New("manager")

	strg, err := storage.NewStorage(filepath.Join(config.GetMainPath(), "db"))
	if err != nil {
		panic(fmt.Errorf("failed to create manager storage: %w", err))
	}

	// Initialize debrid registry
	ctx := context.Background()

	// Optimized transport for high-performance streaming
	// DNS resolver with caching
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,  // Fast connection timeout
		KeepAlive: 30 * time.Second, // Keep connections alive
	}

	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
			ClientSessionCache: tls.NewLRUClientSessionCache(200),
		},
		TLSHandshakeTimeout: 20 * time.Second,
		MaxIdleConns:        400,
		MaxIdleConnsPerHost: 200,
		MaxConnsPerHost:     400,
		IdleConnTimeout:     30 * time.Second,
		DisableCompression:  true,
		DialContext:         dialer.DialContext,
		Proxy:               http.ProxyFromEnvironment,
		ForceAttemptHTTP2:   false,
	}

	streamClient := &http.Client{
		Timeout:   0,
		Transport: transport,
	}

	usenetTimeout, err := utils.ParseDuration(cfg.Usenet.ProcessingTimeout)
	if err != nil {
		usenetTimeout = 10 * time.Minute
	}

	instance := &Manager{
		storage:                strg,
		clients:                xsync.NewMap[string, debrid.Client](),
		logger:                 _logger,
		migrationJobs:          xsync.NewMap[string, *storage.SwitcherJob](),
		config:                 cfg,
		arr:                    arr.NewStorage(),
		queue:                  newQueue(ctx, strg, 1000, cfg.RemoveStalledAfter),
		ctx:                    ctx,
		ready:                  make(chan struct{}),
		streamClient:           streamClient,
		usenetTimeout:          usenetTimeout,
		debridSpeedTestResults: xsync.NewMap[string, debridTypes.SpeedTestResult](),
		activeStreams:          xsync.NewMap[string, *ActiveStream](),
	}

	instance.init()

	// Create migrator
	return instance
}

func (m *Manager) init() {
	cfg := config.Get()
	scheduler, err := gocron.NewScheduler(gocron.WithLocation(time.Local), gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-manager")))
	if err != nil {
		scheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-manager")))
	}

	// Create CET scheduler for time-specific jobs
	cetLocation, err := time.LoadLocation("CET")
	if err != nil {
		cetLocation = time.UTC
	}
	cetScheduler, err := gocron.NewScheduler(gocron.WithLocation(cetLocation), gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-cet")))
	if err != nil {
		cetScheduler, _ = gocron.NewScheduler(gocron.WithGlobalJobOptions(gocron.WithTags("decypharr-cet")))
	}

	m.config = cfg

	// Recreate queue with new config
	m.queue = newQueue(m.ctx, m.storage, 1000, cfg.RemoveStalledAfter)

	// Clear debrid clients so they get recreated with new config
	m.clients = xsync.NewMap[string, debrid.Client]()

	// Reset ready channel and syncTorrents.Once for the next start
	m.ready = make(chan struct{})
	m.readyOnce = sync.Once{}

	m.scheduler = scheduler
	m.cetScheduler = cetScheduler
	m.migrator = NewMigrator(m.storage)
	m.downloader = NewDownloadManager(m)

	// Initialize HTTP pool for streaming
	// Note: We can't create a single pool for all files because the LinkRefresh callback
	// needs torrent+filename context. Instead, manager.Stream will create a pool per request
	// and cache it. This is actually better because different files may have different
	// download links from different CDNs.

	refreshInterval, err := utils.ParseDuration(cfg.RefreshInterval)
	if err != nil {
		refreshInterval = 15 * time.Minute
	}
	m.refreshInterval = refreshInterval

	// initialize debrid clients
	m.initDebridClients()

	// Initialize usenet client
	m.initUsenet()

	// Initialize link service
	m.initLinkService()

	// Init custom folders
	m.initCustomFolders()

	// Initialize fixer
	m.fixer = NewFixer(m)

	// Set mount paths
	m.setMountPaths()

	m.initEntryCache()

	// Initialize notifications service
	m.Notifications = notifications.New(&m.config.Notifications, m.logger)
}

func (m *Manager) initUsenet() {
	usenetClient, err := usenet.New()
	if err != nil {
		m.logger.Warn().Msg("Usenet client not configured")
		m.usenet = nil
		return
	}
	m.usenet = usenetClient

	// Initialize NZB processing worker pool
	maxConcurrentNZB := m.config.Usenet.MaxConcurrentNZB
	if maxConcurrentNZB <= 0 {
		maxConcurrentNZB = 2
	}

	// Create unbounded job queue
	m.nzbQueue = newNzbJobQueue()
	m.nzbWorkerStop = make(chan struct{})

	// Start worker goroutines
	for i := 0; i < maxConcurrentNZB; i++ {
		go m.nzbWorker(i)
	}
}

// initLinkService initializes the link service
func (m *Manager) initLinkService() {
	m.linkService = link.New(
		m.clients,
		m.refreshTorrent,
		m.ReinsertEntry,
		m.streamClient,
		m.config.Retries,
		logger.New("link"),
	)
}

// nzbWorker processes NZB jobs from the queue
func (m *Manager) nzbWorker(id int) {
	for {
		// Check for stop signal
		select {
		case <-m.nzbWorkerStop:
			m.logger.Debug().Int("worker_id", id).Msg("NZB worker stopped")
			return
		default:
		}

		// Pop blocks until a job is available or queue is closed
		job, ok := m.nzbQueue.Pop()
		if !ok {
			m.logger.Debug().Int("worker_id", id).Msg("NZB worker exiting (queue closed)")
			return
		}

		m.logger.Debug().
			Int("worker_id", id).
			Str("name", job.entry.Name).
			Int("queued", m.nzbQueue.Len()).
			Msg("Processing NZB job")

		if err := m.processNewNzb(job.entry, job.meta, job.groups); err != nil {
			m.logger.Error().
				Err(err).
				Int("worker_id", id).
				Str("name", job.entry.Name).
				Msg("Error processing NZB")
			job.entry.MarkAsError(err)
			_ = m.queue.Update(job.entry)
		}
	}
}

func (m *Manager) migrate() {
	// Check if migration has already been done
	status, err := m.migrator.GetStatus()
	if err == nil && !status.Running && status.Completed > 0 {
		m.logger.Info().
			Int("completed", status.Completed).
			Int("errors", status.Errors).
			Msg("Migration already completed previously")
		return
	}

	// GetReader migration stats to see if there are cache files
	stats, err := m.migrator.GetStats()
	if err != nil {
		m.logger.Warn().Err(err).Msg("Failed to get migration stats")
		return
	}

	cacheFiles, ok := stats["cache_files"].(int)
	if !ok || cacheFiles == 0 {
		return
	}

	cacheTorrents, ok := stats["cache_torrents"].(int)
	if !ok {
		cacheTorrents = 0
	}

	m.logger.Info().
		Int("cache_files", cacheFiles).
		Int("unique_torrents", cacheTorrents).
		Msg("Found cache files, starting automatic migration...")

	// Start migration with backup
	if err := m.migrator.Start(); err != nil {
		m.logger.Error().Err(err).Msg("Failed to start automatic migration")
		return
	}

	m.logger.Info().Msg("Automatic migration started successfully")
}

// Start starts the manager and all its components
func (m *Manager) Start(ctx context.Context) error {
	m.startTime = time.Now()
	m.logger.Info().
		Str("version", version.GetInfo().String()).
		Str("mount_type", string(m.config.Mount.Type)).
		Str("notifications", fmt.Sprintf("%v", m.Notifications.IsEnabled())).
		Str("mount_path", m.config.Mount.MountPath).
		Msg("Starting manager")

	// run the migration process
	m.migrate()

	go func() {
		m.syncTorrents(ctx)
		// Sync NZBs
		if err := m.syncNZBs(ctx); err != nil {
			m.logger.Error().Err(err).Msg("Failed to perform initial NZB syncTorrents")
		}
		if fixNZB := os.Getenv("DECYPHARR_FIX_NZB_SIZES"); fixNZB == "1" {
			m.logger.Info().Msg("Starting NZB file size correction as requested by environment variable")
			m.fixNZBFileSizes(ctx)
		}
	}()

	// Start workers
	if err := m.StartWorker(ctx); err != nil {
		return fmt.Errorf("failed to start manager worker: %w", err)
	}

	// Close ready channel once, safe for multiple calls
	m.readyOnce.Do(func() {
		close(m.ready)
	})

	// Start the mount manager if set
	// This also start thr mounting process
	if m.mountManager != nil {
		if err := m.mountManager.Start(ctx); err != nil {
			// If mount manager fails to start, we log the error but continue running the manager
			m.logger.Error().Err(err).Msg("Failed to start mount manager, continuing without mounting")
			return nil
		}
	}

	return nil
}

// Stop stops the manager and cleans up all resources
func (m *Manager) Stop() error {
	m.logger.Info().Msg("Stopping manager")

	// Stop mount manager first
	if m.mountManager != nil {
		m.logger.Info().Msg("Stopping mount manager")
		if err := m.mountManager.Stop(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to stop mount manager")
		}
	}

	// Stop schedulers
	if m.scheduler != nil {
		if err := m.scheduler.Shutdown(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to shutdown scheduler")
		}
	}
	if m.cetScheduler != nil {
		if err := m.cetScheduler.Shutdown(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to shutdown CET scheduler")
		}
	}

	// Close usenet connection manager if active
	if m.usenet != nil {
		m.logger.Info().Msg("Closing usenet connections")
		if err := m.usenet.Close(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to close usenet")
		}
	}

	if m.repair != nil {
		m.repair.Stop()
	}

	// Close storage
	if m.storage != nil {
		m.logger.Info().Msg("Closing storage database")
		if err := m.storage.Close(); err != nil {
			m.logger.Warn().Err(err).Msg("Failed to close storage")
		}
	}

	m.logger.Info().Msg("Manager stopped successfully")
	return nil
}

// Reset resets the manager with the new configuration
// This is called after config changes (e.g., setup wizard) to apply new settings
func (m *Manager) Reset() error {
	m.logger.Info().Msg("Resetting manager with new configuration")

	// Stop resources before resetting
	if err := m.Stop(); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to stop manager during reset")
	}

	// Reopen storage database (it was closed by Stop)
	strg, err := storage.NewStorage(filepath.Join(config.GetMainPath(), "db"))
	if err != nil {
		return fmt.Errorf("failed to reopen storage after reset: %w", err)
	}
	m.storage = strg

	// Reload configuration
	m.init()
	m.logger.Info().Msg("Manager reset complete")
	return nil
}

func (m *Manager) GetStats() (map[string]interface{}, error) {
	count, err := m.storage.Count()
	if err != nil {
		return nil, err
	}

	diskSize := m.storage.DiskSize()
	activeJobs := 0
	completedJobs := 0
	failedJobs := 0
	m.migrationJobs.Range(func(_ string, job *storage.SwitcherJob) bool {
		switch job.Status {
		case storage.SwitcherStatusPending, storage.SwitcherStatusInProgress:
			activeJobs++
		case storage.SwitcherStatusCompleted:
			completedJobs++
		case storage.SwitcherStatusFailed, storage.SwitcherStatusCancelled:
			failedJobs++
		}
		return true
	})

	return map[string]interface{}{
		"total_torrents": count,
		"storage_stats":  map[string]interface{}{"total_size": diskSize},
		"active_jobs":    activeJobs,
		"completed_jobs": completedJobs,
		"failed_jobs":    failedJobs,
	}, nil
}

func (m *Manager) IsReady() chan struct{} {
	return m.ready
}

func (m *Manager) Uptime() time.Duration {
	return time.Since(m.startTime)
}

func (m *Manager) StartTime() time.Time {
	return m.startTime
}

// CRUD operations

func (m *Manager) GetEntryItem(torrentName string) (*storage.EntryItem, error) {
	return m.storage.GetEntryItem(torrentName)
}

func (m *Manager) GetEntryByName(torrentName, filename string) (*storage.Entry, error) {
	// First get entry
	entry, err := m.storage.GetEntryItem(torrentName)
	if err != nil {
		return nil, err
	}

	// Find the file in the entry
	file, err := entry.GetFile(filename)
	if err != nil {
		return nil, err
	}
	return m.GetEntry(file.InfoHash)
}

func (m *Manager) AddOrUpdate(entry *storage.Entry, callback func(t *storage.Entry)) error {
	entry.UpdatedAt = time.Now()
	if err := m.storage.AddOrUpdate(entry); err != nil {
		return err
	}
	if callback != nil {
		go callback(entry)
	}
	return nil
}

// GetEntry gets a torrent by name
func (m *Manager) GetEntry(infohash string) (*storage.Entry, error) {
	return m.storage.Get(infohash)
}

func (m *Manager) EntryExists(infohash string) (bool, error) {
	return m.storage.Exists(infohash)
}

func (m *Manager) GetTorrents(filter func(*storage.Entry) bool) ([]*storage.Entry, error) {
	// Use streaming to avoid loading all torrents into memory at once
	var torrents []*storage.Entry
	err := m.storage.ForEach(func(t *storage.Entry) error {
		if filter == nil || filter(t) {
			torrents = append(torrents, t)
		}
		return nil
	})
	return torrents, err
}

func (m *Manager) GetTorrentsCount() (int, error) {
	return m.storage.Count()
}

// DeleteEntry deletes a torrent by infohash
func (m *Manager) DeleteEntry(infohash string, removePlacements bool) error {
	torr, err := m.GetEntry(infohash)
	if err != nil {
		return err
	}
	// Delete active placements from debrid clients
	if removePlacements {
		go m.RemoveTorrentPlacements(torr)
	}

	if err := m.storage.Delete(infohash); err != nil {
		return err
	}
	// Refresh entry cache
	m.RefreshEntries(true)
	return nil
}

func (m *Manager) DeleteTorrents(infohashes []string, removeFromDebrid bool) error {
	for _, infohash := range infohashes {
		if err := m.DeleteEntry(infohash, removeFromDebrid); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) GetMigrationJob(jobID string) (*storage.SwitcherJob, error) {
	job, exists := m.migrationJobs.Load(jobID)
	if !exists {
		return nil, fmt.Errorf("migration job not found: %s", jobID)
	}
	return job, nil
}

// === Queue ===

func (m *Manager) trackAvailableSlots(ctx context.Context) {
	// This function tracks the available slots for each debrid client
	availableSlots := make(map[string]int)

	m.clients.Range(func(name string, client debrid.Client) bool {
		slots, err := client.GetAvailableSlots()
		if err != nil {
			return true
		}
		availableSlots[name] = slots
		return true
	})

	if len(availableSlots) == 0 {
		return // No debrid clients or slots available, nothing to process
	}

	if m.queue.RequestsSize() <= 0 {
		// Queue is empty, no need to process
		return
	}

	for name, slots := range availableSlots {
		m.logger.Debug().Msgf("Available slots for %s: %d", name, slots)
		// If slots are available, process the next import request from the queue
		for slots > 0 {
			select {
			case <-ctx.Done():
				return // Exit if context is done
			default:
				if err := m.processFromQueue(ctx); err != nil {
					m.logger.Error().Err(err).Msg("Error processing from queue")
					return // Exit on error
				}
				slots-- // Decrease the available slots after processing
			}
		}
	}
}

func (m *Manager) processFromQueue(ctx context.Context) error {
	// Pop the next import request from the queue
	importReq, err := m.queue.PopRequest()
	if err != nil {
		return err
	}
	if importReq == nil {
		return nil
	}
	return m.AddNewTorrent(ctx, importReq)
}
