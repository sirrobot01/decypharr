package vfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	dfsconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"golang.org/x/sync/singleflight"
)

const (
	metaFlushInterval = 2 * time.Second
	// metaFlushDebounce is the minimum spacing between signal-driven
	// metadata flushes; crash exposure stays bounded by metaFlushInterval.
	metaFlushDebounce = 500 * time.Millisecond

	// How long to keep unused cache items around before removing(no delete on disk, just remove from map and close file. Cleanup loop will remove from disk eventually.
	itemIdleTimeout = 1 * time.Minute

	// cacheEvictThreshold is the percentage of max cache size at which eviction starts.
	cacheEvictThreshold = 0.90

	// speedSampleInterval is how often the background goroutine updates downloadSpeed.
	speedSampleInterval = 1 * time.Second
)

// Cache manages sparse cache files for streaming
type Cache struct {
	config *dfsconfig.FuseConfig
	logger zerolog.Logger

	items     *xsync.Map[string, *CacheItem]
	totalSize atomic.Int64
	itemCount atomic.Int64
	diskItems atomic.Int64

	// pool is the process-wide DFS buffer pool: it owns the shared RAM budget
	// and the disk limit (CacheDiskSize) that bounds total on-disk cache even
	// for a single huge open stream, by punching holes behind the read head.
	pool *buffer.Pool

	manager Backend

	ctx    context.Context
	cancel context.CancelFunc

	createGroup  singleflight.Group
	threshold    int64
	streamMemory int64
	cleanupMu    sync.Mutex

	// Stats counters
	cacheHits       atomic.Int64
	cacheMisses     atomic.Int64
	activeDownloads atomic.Int32
	totalDownloaded atomic.Int64
	downloadSpeed   atomic.Int64 // bytes per second, updated periodically
	lastSpeedBytes  atomic.Int64 // bytes at last speed sample
	lastSpeedTime   atomic.Int64 // unix nano at last speed sample
	circuitBreakers atomic.Int32 // count of items with open circuit breakers
}

type candidateEntry struct {
	key        string
	path       string // entry directory (for cleanup of empty dirs)
	dataPath   string // path to data file
	metaPath   string // path to metadata .json file
	atime      time.Time
	mtime      time.Time
	cachedSize int64 // Actual bytes on disk (from ranges)
	opens      int32
	inMap      bool // Whether this item is loaded in the cache map
}

type diskScanResult struct {
	candidates            []candidateEntry
	totalSize             int64
	emptyDirsRemoved      int
	orphanMetadataRemoved int
	errors                int
}

type cleanupRunSummary struct {
	scan              diskScanResult
	scanPasses        int
	closedIdleItems   int
	forcedClosedItems int
	removedDiskItems  int
	sizeBefore        int64
	sizeAfter         int64
	freedBytes        int64
	evictionSkipped   bool
	status            string
	result            string
}

type purgeRunSummary struct {
	scan             diskScanResult
	forcedClosed     int
	removedDiskItems int
	skippedBusyItems int
	sizeBefore       int64
	sizeAfter        int64
	freedBytes       int64
	status           string
	result           string
}

// NewCache creates a new sparse file cache
func NewCache(ctx context.Context, mgr Backend, config *dfsconfig.FuseConfig) (*Cache, error) {
	if err := os.MkdirAll(config.CacheDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cache dir: %w", err)
	}

	ctx, cancel := context.WithCancel(ctx)

	maxSize := config.CacheDiskSize
	threshold := int64(0)
	if maxSize > 0 {
		threshold = int64(float64(maxSize) * cacheEvictThreshold)
		if threshold <= 0 {
			threshold = maxSize
		}
	}
	// History gets what's left of a stream's disk share after its read-ahead:
	// the downloader produces those bytes regardless, so sizing history
	// independently is what put the pool permanently over its limit.
	backWindow := int64(256 << 20)
	if maxSize > 0 {
		share := maxSize/dfsconfig.StreamDiskShare - config.ReadAheadSize
		backWindow = min(backWindow, share)
		backWindow = max(backWindow, 32<<20)
	}
	c := &Cache{
		config: config,
		// Per-stream RAM ask: the read-ahead the downloader produces plus as
		// much history behind it. The pool divides its budget across the open
		// streams if they collectively ask for more.
		streamMemory: max(2*config.ReadAheadSize, 32<<20),
		logger:       logger.New("dfs"),
		items:        xsync.NewMap[string, *CacheItem](),
		manager:      mgr,
		ctx:          ctx,
		cancel:       cancel,
		threshold:    threshold,
	}

	// Account persistent cache files before accepting any reads. Otherwise a
	// restart begins at zero and can admit a full second cache before cleanup
	// notices the bytes already on disk.
	initialScan := c.scanDiskCandidates()
	c.totalSize.Store(initialScan.totalSize)
	c.storeDiskStats(initialScan.candidates, nil)
	c.pool = buffer.NewPool(buffer.PoolConfig{
		Name:             "dfs",
		MemoryBudget:     config.BufferMemory,
		DiskLimit:        maxSize,
		InitialDiskUsage: initialScan.totalSize,
		ReclaimDisk:      c.reclaimClosedDisk,
		BackWindow:       backWindow,
	})
	c.evict()
	go c.evictLoop()
	go c.speedSampleLoop()
	return c, nil
}

// GetItem returns or creates a cache item for the given file
func (c *Cache) GetItem(entryName, filename string, fileSize int64) (*CacheItem, error) {
	key := buildCacheKey(entryName, filename)

	// Fast path: already exists and isn't being torn down by the janitor.
	if item, ok := c.items.Load(key); ok && !item.isClaimed() {
		item.touch()
		return item, nil
	}

	// Slow path: create with singleflight to avoid global lock
	val, err, _ := c.createGroup.Do(key, func() (any, error) {
		// A claimed item is about to be deleted from the map by the janitor
		// (claim and delete are adjacent under cleanupMu); wait the removal
		// out so we create a fresh item instead of handing back a dying one.
		for {
			item, ok := c.items.Load(key)
			if !ok {
				break
			}
			if !item.isClaimed() {
				item.touch()
				return item, nil
			}
			runtime.Gosched()
		}
		// Creation shares the cleanup fence so a scan cannot remove the item
		// directory between MkdirAll and opening its data file.
		c.cleanupMu.Lock()
		defer c.cleanupMu.Unlock()
		item, err := c.newItem(key, entryName, filename, fileSize)
		if err != nil {
			return nil, err
		}
		c.items.Store(key, item)
		c.itemCount.Add(1)
		return item, nil
	})
	if err != nil {
		return nil, err
	}
	item := val.(*CacheItem)
	item.touch()
	return item, nil
}

func (c *Cache) scanDiskCandidates() diskScanResult {
	var result diskScanResult
	topEntries, err := os.ReadDir(c.config.CacheDir)
	if err != nil {
		c.logger.Warn().Err(err).Msg("failed to read cache directory")
		result.errors++
		return result
	}

	for _, topEntry := range topEntries {
		if !topEntry.IsDir() {
			continue
		}

		entryName := topEntry.Name()
		entryDir := filepath.Join(c.config.CacheDir, entryName)

		subEntries, err := os.ReadDir(entryDir)
		if err != nil {
			c.logger.Warn().Err(err).Str("path", entryDir).Msg("failed to read cache entry directory")
			result.errors++
			continue
		}

		// Remove empty directories
		if len(subEntries) == 0 {
			if err := os.RemoveAll(entryDir); err != nil && !os.IsNotExist(err) {
				c.logger.Warn().Err(err).Str("path", entryDir).Msg("failed to remove empty cache directory")
				result.errors++
			} else {
				result.emptyDirsRemoved++
			}
			continue
		}

		// Find data/meta pairs by .json suffix
		for _, sub := range subEntries {
			if sub.IsDir() || !strings.HasSuffix(sub.Name(), ".json") {
				continue
			}

			// Derive the data filename from the meta filename
			filename := strings.TrimSuffix(sub.Name(), ".json")
			metaPath := filepath.Join(entryDir, sub.Name())
			dataPath := filepath.Join(entryDir, filename)
			key := buildCacheKey(entryName, filename)

			var opens int32
			var inMap bool
			if item, ok := c.items.Load(key); ok {
				opens = item.opens.Load()
				inMap = true
			}

			// Read and parse metadata
			var info ItemInfo
			if err := decodeJSONFile(metaPath, &info); err != nil {
				c.logger.Warn().Err(err).Str("path", metaPath).Msg("failed to read or parse cache metadata")
				result.errors++
				continue
			}

			// Verify data file exists
			dataStat, err := os.Stat(dataPath)
			if err != nil {
				if os.IsNotExist(err) && !inMap && opens == 0 && info.Rs.Size() > 0 {
					if rmErr := os.Remove(metaPath); rmErr != nil && !os.IsNotExist(rmErr) {
						c.logger.Warn().
							Err(rmErr).
							Str("path", metaPath).
							Msg("failed to remove orphan cache metadata")
						result.errors++
					} else {
						c.logger.Warn().
							Err(err).
							Str("path", dataPath).
							Str("metadata", metaPath).
							Msg("removed orphan cache metadata for missing data file")
						result.orphanMetadataRemoved++
					}
				} else {
					c.logger.Warn().Err(err).Str("path", dataPath).Msg("cache data file missing")
					result.errors++
				}
				continue
			}

			cachedSize := info.Rs.Size()

			// Set default times if missing
			atime := info.ATime
			mtime := info.ModTime
			if atime.IsZero() {
				atime = mtime
			}
			if mtime.IsZero() {
				mtime = dataStat.ModTime()
				if atime.IsZero() {
					atime = mtime
				}
			}
			result.candidates = append(result.candidates, candidateEntry{
				key:        key,
				path:       entryDir,
				dataPath:   dataPath,
				metaPath:   metaPath,
				atime:      atime,
				mtime:      mtime,
				cachedSize: cachedSize,
				opens:      opens,
				inMap:      inMap,
			})
			result.totalSize += cachedSize
		}
	}

	return result
}

func (c *Cache) evictCandidates(now time.Time, candidates []candidateEntry, totalSize int64, thresholdOverride int64) (int64, int, int, map[string]struct{}) {
	threshold := c.threshold
	if thresholdOverride > 0 {
		threshold = thresholdOverride
	}

	removed := make(map[string]struct{})
	removalErrors := 0
	// removeCandidate reports whether the candidate was actually removed —
	// a failed os.Remove must NOT be counted as freed space (matching
	// purgeCandidates), or totalSize/diskItems undercount and eviction stops
	// early while the bytes are still on disk.
	removeCandidate := func(candidate candidateEntry) bool {
		if _, skip := removed[candidate.key]; skip {
			return false
		}
		// Never remove items that are in the map or have open handles
		if candidate.inMap || candidate.opens > 0 {
			return false
		}
		hadError := false
		// Remove only the specific data + meta files, not the entire entry directory
		if candidate.dataPath != "" {
			if err := os.Remove(candidate.dataPath); err != nil && !os.IsNotExist(err) {
				c.logger.Warn().Err(err).Str("path", candidate.dataPath).Msg("failed to remove cache data file")
				removalErrors++
				hadError = true
			}
		}
		if candidate.metaPath != "" {
			if err := os.Remove(candidate.metaPath); err != nil && !os.IsNotExist(err) {
				c.logger.Warn().Err(err).Str("path", candidate.metaPath).Msg("failed to remove cache meta file")
				removalErrors++
				hadError = true
			}
		}
		if hadError {
			return false
		}
		removed[candidate.key] = struct{}{}
		return true
	}

	// Phase 1: Remove expired entries (only if not in map)
	if c.config.CacheExpiry > 0 {
		for _, candidate := range candidates {
			if !candidate.inMap && candidate.opens == 0 && now.Sub(candidate.atime) > c.config.CacheExpiry {
				if removeCandidate(candidate) {
					totalSize -= candidate.cachedSize
				}
			}
		}
	}

	// Phase 2: If still over threshold, remove oldest entries (only if not in map)
	if threshold > 0 && totalSize > threshold {
		// Sort by access time, then modification time (oldest first)
		slices.SortFunc(candidates, func(a, b candidateEntry) int {
			if order := a.atime.Compare(b.atime); order != 0 {
				return order
			}
			return a.mtime.Compare(b.mtime)
		})

		for _, candidate := range candidates {
			if totalSize <= threshold {
				break
			}
			if removeCandidate(candidate) {
				totalSize -= candidate.cachedSize
			}
		}
	}

	return totalSize, len(removed), removalErrors, removed
}

// reclaimClosedDisk is the Pool's synchronous pressure hook. It removes only
// files no longer represented by an open CacheItem; active buffers reclaim
// their own safe history through the Pool worker. TryLock avoids a lock cycle
// with periodic cleanup closing a Buffer whose final flush needs a reservation.
func (c *Cache) reclaimClosedDisk(needed int64) int64 {
	if needed <= 0 || !c.cleanupMu.TryLock() {
		return 0
	}
	defer c.cleanupMu.Unlock()

	scan := c.scanDiskCandidates()
	sizeBefore := scan.totalSize
	target := max(sizeBefore-needed, int64(1))
	totalSize, removedCount, removalErrors, removedKeys := c.evictCandidates(
		utils.Now(),
		scan.candidates,
		sizeBefore,
		target,
	)
	scan.errors += removalErrors
	c.totalSize.Store(totalSize)
	c.storeDiskStats(scan.candidates, removedKeys)

	freed := max(sizeBefore-totalSize, 0)
	if freed > 0 {
		c.logger.Debug().
			Int64("freed_bytes", freed).
			Int("removed_items", removedCount).
			Msg("reclaimed closed DFS cache files for disk admission")
	}
	return freed
}

func (c *Cache) purgeCandidates(candidates []candidateEntry, totalSize int64) (int64, int, int, int, map[string]struct{}) {
	removed := make(map[string]struct{})
	removalErrors := 0
	skippedBusy := 0

	for _, candidate := range candidates {
		if candidate.inMap || candidate.opens > 0 {
			skippedBusy++
			continue
		}
		if _, skip := removed[candidate.key]; skip {
			continue
		}

		hadError := false
		if candidate.dataPath != "" {
			if err := os.Remove(candidate.dataPath); err != nil && !os.IsNotExist(err) {
				c.logger.Warn().Err(err).Str("path", candidate.dataPath).Msg("failed to purge cache data file")
				removalErrors++
				hadError = true
			}
		}
		if candidate.metaPath != "" {
			if err := os.Remove(candidate.metaPath); err != nil && !os.IsNotExist(err) {
				c.logger.Warn().Err(err).Str("path", candidate.metaPath).Msg("failed to purge cache meta file")
				removalErrors++
				hadError = true
			}
		}

		if hadError {
			continue
		}
		removed[candidate.key] = struct{}{}
		totalSize -= candidate.cachedSize
	}

	if totalSize < 0 {
		totalSize = 0
	}
	return totalSize, len(removed), removalErrors, skippedBusy, removed
}

func (c *Cache) storeDiskStats(candidates []candidateEntry, removed map[string]struct{}) {
	count := int64(0)

	for _, candidate := range candidates {
		if _, skip := removed[candidate.key]; skip {
			continue
		}

		count++
	}

	c.diskItems.Store(count)
}

// newItem creates a new cache item. The underlying byte storage is a
// buffer.Buffer over a sparse file at <CacheDir>/<entryName>/<filename>;
// the buffer is seeded with any ranges from previously-persisted metadata
// so a re-opened item can serve its cached bytes immediately without
// re-downloading.
func (c *Cache) newItem(key, entryName, filename string, fileSize int64) (*CacheItem, error) {
	entry, err := c.manager.GetEntryByName(entryName, filename)
	if err != nil {
		return nil, fmt.Errorf("failed to get storage entry: %w", err)
	}
	_logger := c.logger.With().Str("entry", entryName).Str("filename", filename).Logger()
	log := logger.NewRateLimitedLogger(logger.WithLogger(_logger))

	itemDir := filepath.Join(c.config.CacheDir, entryName)
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create item dir: %w", err)
	}

	cachePath := filepath.Join(itemDir, filename)
	metaPath := filepath.Join(itemDir, filename+".json")

	// Load existing metadata before constructing the buffer so its range
	// tracker is seeded with anything the prior session persisted.
	var info ItemInfo
	if err := decodeJSONFile(metaPath, &info); err != nil && !os.IsNotExist(err) {
		c.logger.Warn().Err(err).Str("key", key).Msg("corrupt metadata, resetting")
		info = ItemInfo{}
	}

	// Defend against a directory accidentally sitting at cachePath
	// (interrupted prior run, leftover state).
	if stat, err := os.Stat(cachePath); err == nil && stat.IsDir() {
		if err := os.RemoveAll(cachePath); err != nil {
			return nil, fmt.Errorf("failed to remove directory at cache path: %w", err)
		}
	}

	// Translate persisted ranges into the buffer's seed format.
	seed := make([]buffer.Range, 0, len(info.Rs))
	for _, r := range info.Rs {
		if r.Size > 0 {
			seed = append(seed, buffer.Range{Off: r.Pos, Size: r.Size})
		}
	}

	var item *CacheItem
	buf, err := c.pool.NewBuffer(buffer.Config{
		// RAM window in front of a sparse file: reads during playback are
		// served from memory, and blocks leaving the window are flushed to the
		// file so the cache still survives a restart (InitialRanges reseeds it).
		MemorySize:                c.streamMemory,
		DiskPath:                  cachePath,
		TotalSize:                 fileSize,
		InitialRanges:             seed,
		InitialRangesPreaccounted: true,
		PersistentDisk:            true,
		OnPersistChange: func() {
			if item != nil {
				item.markMetadataDirty()
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create buffer: %w", err)
	}

	info.Size = fileSize
	info.ModTime = utils.Now()
	info.ATime = utils.Now()

	item = &CacheItem{
		cache:    c,
		key:      key,
		entry:    entry,
		filename: filename,
		buf:      buf,
		metaPath: metaPath,
		info:     info,
		logger:   log.Rate(key),
	}

	item.downloaders.Store(NewDownloaders(c.ctx, c.manager, item, c.config))
	item.startMetaWriter()
	item.markMetadataDirty()
	return item, nil
}

// evictLoop runs periodic evict
func (c *Cache) evictLoop() {
	ticker := time.NewTicker(c.config.CacheCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.evict()
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Cache) cleanupItems(now time.Time, forceZeroOpen bool) int {
	evicted := 0
	c.items.Range(func(key string, item *CacheItem) bool {
		if item.opens.Load() > 0 {
			return true // Still open, keep in map
		}

		item.metaMu.RLock()
		lastAccess := item.info.ATime
		item.metaMu.RUnlock()

		if !forceZeroOpen && now.Sub(lastAccess) <= itemIdleTimeout {
			return true
		}

		// Claim before touching anything: the CAS fences out a concurrent
		// GetItem/Open that already loaded this item from the map (its Open
		// fails and it fetches a fresh item instead). Previously the close
		// happened after an unfenced opens check, so a handle opening in that
		// window would read from an item whose buffer was being torn down.
		// Delete from the map before the (potentially slow) Close so waiting
		// creators aren't stalled. xsync.Map supports modification during Range.
		if !item.claimForClose() {
			return true
		}
		c.items.Delete(key)
		_ = item.Close()
		c.itemCount.Add(-1)
		evicted++
		return true
	})

	return evicted
}

func combineDiskScanResults(first, second diskScanResult) diskScanResult {
	second.emptyDirsRemoved += first.emptyDirsRemoved
	second.orphanMetadataRemoved += first.orphanMetadataRemoved
	second.errors += first.errors
	return second
}

func countBusyCandidates(candidates []candidateEntry) int {
	var busy int
	for _, candidate := range candidates {
		if candidate.inMap || candidate.opens > 0 {
			busy++
		}
	}
	return busy
}

func cacheUsageText(size, maxSize int64) string {
	if maxSize <= 0 {
		return fmt.Sprintf("%s / unlimited", utils.FormatSize(size))
	}
	utilization := float64(size) / float64(maxSize) * 100
	return fmt.Sprintf("%s / %s (%.1f%%)", utils.FormatSize(size), utils.FormatSize(maxSize), utilization)
}

func cleanupActionText(closedIdleItems, forcedClosedItems, removedDiskItems, removedEmptyDirs, removedOrphanMetadata int) string {
	if closedIdleItems == 0 && forcedClosedItems == 0 && removedDiskItems == 0 && removedEmptyDirs == 0 && removedOrphanMetadata == 0 {
		return "no cleanup needed"
	}
	return fmt.Sprintf(
		"closed %d idle, force-closed %d, removed %d disk items, %d empty dirs, %d orphan metadata files",
		closedIdleItems,
		forcedClosedItems,
		removedDiskItems,
		removedEmptyDirs,
		removedOrphanMetadata,
	)
}

func cleanupResultText(errors int, evictionSkipped bool) string {
	if errors > 0 {
		return fmt.Sprintf("%d warning(s); check nearby warn logs for details", errors)
	}
	if evictionSkipped {
		return "Cache is within limit"
	}
	return "Completed successfully"
}

func (c *Cache) logCleanupSummary(summary cleanupRunSummary) {
	if summary.freedBytes <= 0 {
		return
	}
	c.logger.Debug().Msgf(
		"DFS cache cleanup: %s. Scanned %d cache item(s), %d busy, across %d scan pass(es). Cleanup: %s; freed %s. Result: %s.",
		cacheUsageText(summary.sizeAfter, c.config.CacheDiskSize),
		len(summary.scan.candidates),
		countBusyCandidates(summary.scan.candidates),
		summary.scanPasses,
		cleanupActionText(summary.closedIdleItems, summary.forcedClosedItems, summary.removedDiskItems, summary.scan.emptyDirsRemoved, summary.scan.orphanMetadataRemoved),
		utils.FormatSize(summary.freedBytes),
		summary.result,
	)
}

func (c *Cache) logPurgeSummary(summary purgeRunSummary) {
	if summary.freedBytes == 0 {
		return
	}
	c.logger.Info().Msgf(
		"DFS cache purge: %s. Scanned %d cache item(s), skipped %d busy, force-closed %d idle item(s), removed %d disk item(s), freed %s. Result: %s.",
		cacheUsageText(summary.sizeAfter, c.config.CacheDiskSize),
		len(summary.scan.candidates),
		summary.skippedBusyItems,
		summary.forcedClosed,
		summary.removedDiskItems,
		utils.FormatSize(summary.freedBytes),
		summary.result,
	)
}

func (c *Cache) finalizeCleanupSummary(summary cleanupRunSummary) cleanupRunSummary {
	summary.freedBytes = max(summary.sizeBefore-summary.sizeAfter, 0)
	summary.result = cleanupResultText(summary.scan.errors, summary.evictionSkipped)
	if summary.scan.errors > 0 {
		summary.status = "warning"
	} else {
		summary.status = "healthy"
	}

	return summary
}

func cleanupResultStats(summary cleanupRunSummary) map[string]any {
	return map[string]any{
		"cleanup_status":              summary.status,
		"cleanup_result":              summary.result,
		"cleanup_warning_count":       int64(summary.scan.errors),
		"cleanup_freed_bytes":         summary.freedBytes,
		"cleanup_removed_items":       int64(summary.removedDiskItems),
		"cleanup_force_closed_items":  int64(summary.forcedClosedItems),
		"cleanup_closed_idle_items":   int64(summary.closedIdleItems),
		"cleanup_empty_dirs_removed":  int64(summary.scan.emptyDirsRemoved),
		"cleanup_orphan_meta_removed": int64(summary.scan.orphanMetadataRemoved),
	}
}

// evict removes old and excess cache items
func (c *Cache) evict() cleanupRunSummary {
	c.cleanupMu.Lock()
	defer c.cleanupMu.Unlock()

	now := utils.Now()

	closedIdleItems := c.cleanupItems(now, false)

	scanPasses := 1
	scan := c.scanDiskCandidates()
	candidates := scan.candidates
	totalSize := scan.totalSize

	// WinFsp clients tend to speculatively open files. If we're already over
	// budget, close zero-open cache items immediately so they become evictable
	// on the same pass instead of waiting for the idle timeout.
	forcedClosedItems := 0
	if runtime.GOOS == "windows" && c.threshold > 0 && totalSize > c.threshold {
		forcedClosedItems = c.cleanupItems(now, true)
		if forcedClosedItems > 0 {
			rescan := c.scanDiskCandidates()
			scanPasses++
			scan = combineDiskScanResults(scan, rescan)
			candidates = scan.candidates
			totalSize = scan.totalSize
		}
	}

	sizeBefore := totalSize
	removedCount := 0
	evictionSkipped := false
	removedKeys := map[string]struct{}{}

	// If cache expiry is disabled and we're under threshold, skip disk scan.
	if c.config.CacheExpiry <= 0 && (c.threshold <= 0 || totalSize <= c.threshold) {
		evictionSkipped = true
	} else {
		var removalErrors int
		totalSize, removedCount, removalErrors, removedKeys = c.evictCandidates(now, candidates, totalSize, 0)
		scan.errors += removalErrors
	}

	c.totalSize.Store(totalSize)
	c.storeDiskStats(scan.candidates, removedKeys)
	if c.pool != nil {
		c.pool.ReleaseDisk(max(sizeBefore-totalSize, 0))
	}

	summary := c.finalizeCleanupSummary(cleanupRunSummary{
		scan:              scan,
		scanPasses:        scanPasses,
		closedIdleItems:   closedIdleItems,
		forcedClosedItems: forcedClosedItems,
		removedDiskItems:  removedCount,
		sizeBefore:        sizeBefore,
		sizeAfter:         totalSize,
		evictionSkipped:   evictionSkipped,
	})
	c.logCleanupSummary(summary)
	return summary
}

// RunCleanup executes the same cache cleanup path used by the background loop
// and returns this run's maintenance result for API callers.
func (c *Cache) RunCleanup() map[string]any {
	return cleanupResultStats(c.evict())
}

// PurgeCache removes all cached disk items that are not currently in use.
func (c *Cache) PurgeCache() map[string]any {
	c.cleanupMu.Lock()
	defer c.cleanupMu.Unlock()

	now := utils.Now()
	forcedClosed := c.cleanupItems(now, true)
	scan := c.scanDiskCandidates()
	sizeBefore := scan.totalSize

	totalSize, removedCount, removalErrors, skippedBusy, removedKeys := c.purgeCandidates(scan.candidates, scan.totalSize)
	scan.errors += removalErrors

	c.totalSize.Store(totalSize)
	c.storeDiskStats(scan.candidates, removedKeys)
	if c.pool != nil {
		c.pool.ReleaseDisk(max(sizeBefore-totalSize, 0))
	}

	freedBytes := max(sizeBefore-totalSize, 0)
	status := "healthy"
	result := "Purged cache"
	if scan.errors > 0 {
		status = "warning"
		result = fmt.Sprintf("%d warning(s); check nearby warn logs for details", scan.errors)
	}

	summary := purgeRunSummary{
		scan:             scan,
		forcedClosed:     forcedClosed,
		removedDiskItems: removedCount,
		skippedBusyItems: skippedBusy,
		sizeBefore:       sizeBefore,
		sizeAfter:        totalSize,
		freedBytes:       freedBytes,
		status:           status,
		result:           result,
	}
	c.logPurgeSummary(summary)

	return map[string]any{
		"purge_status":              summary.status,
		"purge_result":              summary.result,
		"purge_warning_count":       int64(summary.scan.errors),
		"purge_freed_bytes":         summary.freedBytes,
		"purge_removed_items":       int64(summary.removedDiskItems),
		"purge_skipped_busy_items":  int64(summary.skippedBusyItems),
		"purge_force_closed_items":  int64(summary.forcedClosed),
		"purge_cache_size_before":   summary.sizeBefore,
		"purge_cache_size_after":    summary.sizeAfter,
		"purge_empty_dirs_removed":  int64(summary.scan.emptyDirsRemoved),
		"purge_orphan_meta_removed": int64(summary.scan.orphanMetadataRemoved),
	}
}

// Close shuts down the cache
func (c *Cache) Close() error {
	c.cancel()

	c.items.Range(func(key string, item *CacheItem) bool {
		item.Close()
		return true
	})
	c.items.Clear()
	c.itemCount.Store(0)
	c.diskItems.Store(0)

	// Stop the pool's disk-eviction worker after all items (and their buffers)
	// are closed.
	if c.pool != nil {
		_ = c.pool.Close()
	}

	return nil
}

// RecordCacheHit increments the cache hit counter.
func (c *Cache) RecordCacheHit() {
	c.cacheHits.Add(1)
}

// RecordCacheMiss increments the cache miss counter.
func (c *Cache) RecordCacheMiss() {
	c.cacheMisses.Add(1)
}

// AddDownloadedBytes adds to the total downloaded byte counter.
func (c *Cache) AddDownloadedBytes(n int64) {
	c.totalDownloaded.Add(n)
}

// updateSpeed samples the current download speed. It is called only from
// speedSampleLoop (a single goroutine); the two Swaps are NOT atomic as a
// pair, so adding a second caller would need real synchronization here.
func (c *Cache) updateSpeed() {
	now := time.Now().UnixNano()
	currentBytes := c.totalDownloaded.Load()

	lastTime := c.lastSpeedTime.Swap(now)
	lastBytes := c.lastSpeedBytes.Swap(currentBytes)

	if lastTime == 0 {
		// First sample — just record the baseline, no speed yet.
		return
	}
	elapsed := now - lastTime
	if elapsed <= 0 {
		return
	}
	bps := max(((currentBytes-lastBytes)*int64(time.Second))/elapsed, 0)
	c.downloadSpeed.Store(bps)
}

// speedSampleLoop updates the download speed on a fixed 1-second cadence,
// independent of GetStats calls so the reported speed is always fresh and
// the sample window is predictable regardless of polling frequency.
func (c *Cache) speedSampleLoop() {
	ticker := time.NewTicker(speedSampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.updateSpeed()
		case <-c.ctx.Done():
			return
		}
	}
}

// GetStats returns cache statistics
func (c *Cache) GetStats() map[string]any {
	maxSize := c.config.CacheDiskSize
	totalSize := c.totalSize.Load()
	if c.pool != nil {
		totalSize = c.pool.Stats().DiskInUse
	}
	utilization := 0.0
	if maxSize > 0 {
		utilization = float64(totalSize) / float64(maxSize)
	}

	hits := c.cacheHits.Load()
	misses := c.cacheMisses.Load()
	hitRate := 0.0
	if total := hits + misses; total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	stats := map[string]any{
		"type":              "vfs",
		"total_size":        totalSize,
		"max_size":          c.config.CacheDiskSize,
		"item_count":        c.diskItems.Load(),
		"active_item_count": c.itemCount.Load(),
		"utilization":       utilization,
		"cache_hits":        hits,
		"cache_misses":      misses,
		"cache_hit_rate":    hitRate,
		"active_downloads":  c.activeDownloads.Load(),
		"total_downloaded":  c.totalDownloaded.Load(),
		"download_speed":    c.downloadSpeed.Load(),
		"circuit_breakers":  c.circuitBreakers.Load(),
	}

	return stats
}

// CacheItem represents a single cached file. Byte storage is delegated to
// a buffer.Buffer — sparse disk file plus an LRU-managed in-RAM block
// cache — so this struct only carries the per-item *policy* state
// (downloaders coordinator, pin/refcounts, metadata persistence).
type CacheItem struct {
	cache    *Cache
	key      string
	entry    *storage.Entry
	filename string

	buf      *buffer.Buffer
	metaPath string

	info ItemInfo

	opens  atomic.Int32 // Number of open handles (prevents eviction)
	logger *logger.RateLimitedEvent
	// downloaders is the download coordinator: set once at construction,
	// swapped to nil by Close, loaded per read.
	downloaders atomic.Pointer[Downloaders]

	metaMu sync.RWMutex

	metaDirty   atomic.Bool
	metaStateMu sync.RWMutex
	metaFlushCh chan struct{}
	metaStopCh  chan struct{}
	metaWG      sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

func (item *CacheItem) startMetaWriter() {
	flushCh := make(chan struct{}, 1)
	stopCh := make(chan struct{})
	item.metaStateMu.Lock()
	item.metaFlushCh = flushCh
	item.metaStopCh = stopCh
	item.metaWG.Add(1)
	item.metaStateMu.Unlock()
	go item.metaWriterLoop(flushCh, stopCh)
}

func (item *CacheItem) stopMetaWriter() {
	item.metaStateMu.Lock()
	stopCh := item.metaStopCh
	if stopCh == nil {
		item.metaStateMu.Unlock()
		return
	}
	item.metaStopCh = nil
	item.metaFlushCh = nil
	item.metaStateMu.Unlock()
	close(stopCh)
	item.metaWG.Wait()
}

func (item *CacheItem) metaWriterLoop(flushCh <-chan struct{}, stopCh <-chan struct{}) {
	defer item.metaWG.Done()
	ticker := time.NewTicker(metaFlushInterval)
	defer ticker.Stop()
	var lastFlush time.Time
	for {
		select {
		case <-ticker.C:
			item.flushMetadata(false)
			lastFlush = time.Now()
		case <-flushCh:
			// The signal fires per write; debounce the flush. The dirty
			// flag stays set, so the ticker picks up whatever the debounce
			// skipped.
			if time.Since(lastFlush) < metaFlushDebounce {
				continue
			}
			item.flushMetadata(false)
			lastFlush = time.Now()
		case <-stopCh:
			item.flushMetadata(true)
			return
		}
	}
}

func (item *CacheItem) markMetadataDirty() {
	item.metaDirty.Store(true)
	item.metaStateMu.RLock()
	if item.metaFlushCh != nil {
		select {
		case item.metaFlushCh <- struct{}{}:
		default:
		}
	}
	item.metaStateMu.RUnlock()
}

func (item *CacheItem) flushMetadata(force bool) {
	if item.buf == nil {
		return
	}
	if !force && !item.metaDirty.Load() {
		return
	}
	// Clear before snapshotting so a concurrent update re-arms the writer.
	item.metaDirty.Store(false)
	item.metaMu.RLock()
	info := item.info
	item.metaMu.RUnlock()
	info.Rs = nil
	for _, r := range item.buf.PersistedRanges(0, info.Size) {
		info.Rs = append(info.Rs, ranges.Range{Pos: r.Off, Size: r.Size})
	}

	data, err := json.Marshal(info)
	if err != nil {
		item.cache.logger.Warn().Err(err).Str("key", item.key).Msg("failed to marshal cache metadata")
		return
	}
	// Confirm directory exists before writing metadata (in case it was deleted by cleanup)
	if err := os.MkdirAll(filepath.Dir(item.metaPath), 0755); err != nil {
		item.cache.logger.Warn().Err(err).Str("key", item.key).Msg("failed to create cache directory for metadata")
		item.metaDirty.Store(true) // retry on the next tick
		return
	}
	// Atomic write: write to temp file then rename to avoid corrupt reads
	// from scanDiskCandidates racing with this write.
	tmpPath := item.metaPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		item.cache.logger.Warn().Err(err).Str("key", item.key).Msg("failed to write cache metadata")
		item.metaDirty.Store(true) // retry on the next tick
		return
	}
	if err := os.Rename(tmpPath, item.metaPath); err != nil {
		item.cache.logger.Warn().Err(err).Str("key", item.key).Msg("failed to rename cache metadata")
		_ = os.Remove(tmpPath)
		item.metaDirty.Store(true) // retry on the next tick
		return
	}
}

// ItemInfo is persisted to disk
type ItemInfo struct {
	Size    int64         `json:"size"`
	Rs      ranges.Ranges `json:"ranges"` // Downloaded regions
	ModTime time.Time     `json:"mod_time"`
	ATime   time.Time     `json:"atime"`
}

// touch updates access time
func (item *CacheItem) touch() {
	item.metaMu.Lock()
	item.info.ATime = utils.Now()
	item.metaMu.Unlock()
	item.markMetadataDirty()
}

// cacheItemClaimed marks an item claimed for teardown by the cache janitor.
// Once opens holds this value no new handle can Open the item, which is what
// makes cleanupItems' close safe against a concurrent GetItem/GetFile that
// already loaded the item from the map.
const cacheItemClaimed = int32(-1 << 30)

// Open takes an open reference (prevents eviction). It returns false if the
// item has been claimed for teardown — the caller must fetch a fresh item.
// The CAS loop (rather than a blind Add) is what closes the race where the
// janitor decided to close an idle item in the same instant a handle opened it.
func (item *CacheItem) Open() bool {
	for {
		n := item.opens.Load()
		if n < 0 {
			return false
		}
		if item.opens.CompareAndSwap(n, n+1) {
			item.touch()
			return true
		}
	}
}

// claimForClose atomically claims an idle (opens == 0) item for teardown,
// fencing out any future Open. Only the cache janitor calls this.
func (item *CacheItem) claimForClose() bool {
	return item.opens.CompareAndSwap(0, cacheItemClaimed)
}

// isClaimed reports whether the janitor has claimed this item for teardown.
func (item *CacheItem) isClaimed() bool {
	return item.opens.Load() < 0
}

// Release decrements the open count
func (item *CacheItem) Release() {
	newCount := item.opens.Add(-1)
	if newCount > 0 {
		return
	}
	if newCount < 0 {
		// Unbalanced release. Undo rather than Store(0): a blind store could
		// stomp a concurrent Open's increment, and — worse — would erase a
		// janitor claim, resurrecting an item that is being closed.
		item.opens.Add(1)
		return
	}
	// Last handle closed: stop in-flight downloads so we don't keep stale
	// downloader goroutines active after the file is no longer in use.
	item.StopDownloaders()
}

// StopDownloaders stops active downloads but keeps the cache item alive
// for potential cache reuse. This is called when all file handles are closed.
func (item *CacheItem) StopDownloaders() {
	if dls := item.downloaders.Load(); dls != nil {
		dls.StopAll()
	}
}

// ReadAt reads from the sparse file, downloading if needed.
// Uses context.Background() — prefer ReadAtContext when a caller context is available.
func (item *CacheItem) ReadAt(p []byte, off int64) (int, error) {
	return item.ReadAtContext(context.Background(), p, off)
}

// ReadAtContext reads from the sparse file, downloading if needed.
// Respects ctx cancellation so callers (e.g. FUSE handles with a read timeout)
// are not left blocked indefinitely when the client disconnects.
func (item *CacheItem) ReadAtContext(ctx context.Context, p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= item.info.Size {
		return 0, io.EOF
	}
	if int64(len(p)) > item.info.Size-off {
		p = p[:item.info.Size-off]
	}
	readSize := int64(len(p))

	dls := item.downloaders.Load()
	if dls == nil {
		return 0, errors.New("downloaders closed")
	}

	// Publish before reading so a seek-back protects its new working set from
	// the pool's disk backstop.
	item.buf.SetReadHead(off)

	if item.buf.HasRange(off, readSize) {
		// Warm: serve from the buffer without touching the coordinator lock,
		// and extend the download frontier only every readAheadPokeInterval.
		item.cache.RecordCacheHit()
		dls.keepAhead(off, readSize)
	} else {
		item.cache.RecordCacheMiss()
		dls.lastPoke.Store(-1)
		// Prioritize media-probe-style near-EOF reads so they don't queue behind
		// bulk prefetch, and retry transient failures a few times before surfacing
		// EIO — ffprobe treats a single read error as fatal.
		priority := isProbeRead(off, readSize, item.info.Size)
		if _, err := dls.DownloadWithRetry(ctx, ranges.Range{Pos: off, Size: readSize}, priority); err != nil {
			return 0, fmt.Errorf("download failed: %w", err)
		}
	}

	// We do NOT fadvise(DONTNEED) the range just read — that defeats kernel
	// readahead and hurts prefetch. DropBehindMargin drops well behind the read
	// head instead, keeping the bytes on disk for a longer seek-back.
	n, err := item.buf.ReadAt(p, off)
	if err == nil || errors.Is(err, io.EOF) {
		item.buf.SetReadHead(off + int64(n))
		if margin := item.cache.config.DropBehindMargin; margin > 0 {
			item.buf.DropBehind(off+int64(n), margin)
		}
	}
	if errors.Is(err, buffer.ErrNotPresent) {
		return n, fmt.Errorf("buffer reported missing range at %d+%d: %w", off, len(p), err)
	}
	return n, err
}

// WriteAtNoOverwrite writes only the bytes in p that aren't already cached.
// Returns total p length as n (for io.Writer contract) and the count of
// bytes skipped because they were already present.
func (item *CacheItem) WriteAtNoOverwrite(p []byte, off int64) (n, skipped int, err error) {
	skipped, err = item.buf.WriteMissing(p, off)
	item.markMetadataDirty()
	return len(p), skipped, err
}

// HasRange reports whether the entire range is cached.
func (item *CacheItem) HasRange(r ranges.Range) bool {
	return item.buf.HasRange(r.Pos, r.Size)
}

// FindMissing returns the portion of r not yet downloaded.
func (item *CacheItem) FindMissing(r ranges.Range) ranges.Range {
	if r.End() > item.info.Size {
		r.Size = item.info.Size - r.Pos
	}
	if r.Size <= 0 {
		return ranges.Range{}
	}
	m := item.buf.FindMissing(r.Pos, r.Size)
	return ranges.Range{Pos: m.Off, Size: m.Size}
}

// Close closes the cache item and saves metadata. The underlying buffer's
// disk file is NOT removed (DFS persistence across runs is part of the
// design — the user expects re-opens of a previously-cached file to hit
// disk, not re-download).
func (item *CacheItem) Close() error {
	item.closeOnce.Do(func() {
		dls := item.downloaders.Swap(nil)

		if dls != nil {
			if err := dls.Close(nil); err != nil && item.closeErr == nil {
				item.closeErr = err
			}
		}

		item.stopMetaWriter()

		if item.buf != nil {
			if err := item.buf.Close(); err != nil && item.closeErr == nil {
				item.closeErr = err
			}
			item.flushMetadata(true)
		}
	})
	return item.closeErr
}

func buildCacheKey(entryName, filename string) string {
	return fmt.Sprintf("%s/%s", entryName, filename)
}

// decodeJSONFile stream-decodes a JSON file into v, avoiding the intermediate
// []byte slurp of os.ReadFile + json.Unmarshal. Keeps allocation proportional
// to the decoded object rather than 2× the file size.
func decodeJSONFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewDecoder(f).Decode(v); err != nil && err != io.EOF {
		return err
	}
	return nil
}
