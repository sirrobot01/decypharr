package vfs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"golang.org/x/sync/singleflight"
)

const (
	metaFlushInterval = 2 * time.Second

	// How long to keep unused cache items around before removing(no delete on disk, just remove from map and close file. Cleanup loop will remove from disk eventually.
	itemIdleTimeout = 1 * time.Minute

	// cacheEvictThreshold is the percentage of max cache size at which eviction starts.
	cacheEvictThreshold = 0.90

	// speedSampleInterval is how often the background goroutine updates downloadSpeed.
	speedSampleInterval = 1 * time.Second
)

// Cache manages sparse cache files for streaming
type Cache struct {
	config *config.FuseConfig
	logger zerolog.Logger

	items     *xsync.Map[string, *CacheItem]
	totalSize atomic.Int64
	itemCount atomic.Int64
	diskItems atomic.Int64

	// pool is the process-wide DFS buffer pool: it owns the shared RAM budget
	// and the disk limit (CacheDiskSize) that bounds total on-disk cache even
	// for a single huge open stream, by punching holes behind the read head.
	pool *buffer.Pool

	manager *manager.Manager

	ctx    context.Context
	cancel context.CancelFunc

	createGroup singleflight.Group
	threshold   int64
	cleanupMu   sync.Mutex

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
func NewCache(ctx context.Context, mgr *manager.Manager, config *config.FuseConfig) (*Cache, error) {
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
	// The DFS streaming-buffer pool: its own RAM budget plus a disk limit equal
	// to the cache size, so a single huge open stream stays bounded by punching
	// holes behind the read head once over the limit. Keep a back-window of
	// recent history behind the head (capped to a quarter of the disk limit so
	// the backstop can still reclaim when the limit is small).
	backWindow := int64(256 << 20)
	if maxSize > 0 && backWindow > maxSize/4 {
		backWindow = maxSize / 4
	}
	pool := buffer.NewPool(buffer.PoolConfig{
		Name:         "dfs",
		MemoryBudget: config.BufferMemory,
		DiskLimit:    maxSize,
		BackWindow:   backWindow,
	})

	c := &Cache{
		config:    config,
		logger:    logger.New("dfs"),
		items:     xsync.NewMap[string, *CacheItem](),
		manager:   mgr,
		ctx:       ctx,
		cancel:    cancel,
		threshold: threshold,
		pool:      pool,
	}
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
	val, err, _ := c.createGroup.Do(key, func() (interface{}, error) {
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
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].atime.Equal(candidates[j].atime) {
				return candidates[i].mtime.Before(candidates[j].mtime)
			}
			return candidates[i].atime.Before(candidates[j].atime)
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

	// item is referenced by the buffer's OnEvict closure below; it is assigned
	// before the buffer can fire OnEvict (which only happens during active
	// streaming, well after this returns), so the closure's nil-check is just
	// belt-and-suspenders.
	var item *CacheItem

	buf, err := c.pool.NewBuffer(buffer.Config{
		// 32 MB per-item RAM ceiling for the streaming working set; cold pages
		// live in the OS page cache around the sparse file. Aggregate RAM
		// across all open/cached files is bounded by the DFS buffer pool's
		// budget rather than by a tiny per-item ceiling, so a library scan
		// touching hundreds of files can't blow up RSS while a single stream
		// still gets enough headroom to play smoothly.
		MemorySize:    32 << 20,
		DiskPath:      cachePath,
		TotalSize:     fileSize,
		InitialRanges: seed,
		// When the DFS pool punches a hole behind the read head to stay under
		// the disk limit, drop the same range from the persisted metadata so a
		// later reopen doesn't claim bytes that are now a hole on disk.
		OnEvict: func(off, length int64) {
			if item != nil {
				item.onBufferEvict(off, length)
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create buffer: %w", err)
	}

	info.Size = fileSize
	info.ModTime = utils.Now()
	info.ATime = utils.Now()
	_logger := c.logger.With().Str("entry", entryName).Str("filename", filename).Logger()
	log := logger.NewRateLimitedLogger(logger.WithLogger(_logger))
	entry, err := c.manager.GetEntryByName(entryName, filename)
	if err != nil {
		_ = buf.Close()
		return nil, fmt.Errorf("failed to get storage entry: %w", err)
	}

	item = &CacheItem{
		cache:    c,
		key:      key,
		entry:    entry,
		filename: filename,
		buf:      buf,
		metaPath: metaPath,
		info:     info,
		logger:   log.Rate(buildCacheKey(entryName, filename)),
	}

	item.downloaders = NewDownloaders(c.ctx, c.manager, item, c.config)
	item.startMetaWriter()
	item.markMetadataDirty()
	return item, nil
}

// evictLoop runs periodic evict
func (c *Cache) evictLoop() {
	ticker := time.NewTicker(c.config.CacheCleanupInterval)
	defer ticker.Stop()

	// Run evict immediately on startup to remove stale items before they can be accessed
	c.evict()

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
	summary.freedBytes = summary.sizeBefore - summary.sizeAfter
	if summary.freedBytes < 0 {
		summary.freedBytes = 0
	}
	summary.result = cleanupResultText(summary.scan.errors, summary.evictionSkipped)
	if summary.scan.errors > 0 {
		summary.status = "warning"
	} else {
		summary.status = "healthy"
	}

	return summary
}

func cleanupResultStats(summary cleanupRunSummary) map[string]interface{} {
	return map[string]interface{}{
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
func (c *Cache) RunCleanup() map[string]interface{} {
	return cleanupResultStats(c.evict())
}

// PurgeCache removes all cached disk items that are not currently in use.
func (c *Cache) PurgeCache() map[string]interface{} {
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

	freedBytes := sizeBefore - totalSize
	if freedBytes < 0 {
		freedBytes = 0
	}
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

	return map[string]interface{}{
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
	bps := ((currentBytes - lastBytes) * int64(time.Second)) / elapsed
	if bps < 0 {
		bps = 0
	}
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
func (c *Cache) GetStats() map[string]interface{} {
	maxSize := c.config.CacheDiskSize
	utilization := 0.0
	if maxSize > 0 {
		utilization = float64(c.totalSize.Load()) / float64(maxSize)
	}

	hits := c.cacheHits.Load()
	misses := c.cacheMisses.Load()
	hitRate := 0.0
	if total := hits + misses; total > 0 {
		hitRate = float64(hits) / float64(total)
	}

	stats := map[string]interface{}{
		"type":              "vfs",
		"total_size":        c.totalSize.Load(),
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

	opens       atomic.Int32 // Number of open handles (prevents eviction)
	logger      *logger.RateLimitedEvent
	downloaders *Downloaders // Download coordinator

	metaMu sync.RWMutex
	dlMu   sync.Mutex

	metaDirty   atomic.Bool
	metaFlushCh chan struct{}
	metaStopCh  chan struct{}
	metaWG      sync.WaitGroup

	closeOnce sync.Once
	closeErr  error
}

func (item *CacheItem) startMetaWriter() {
	item.metaFlushCh = make(chan struct{}, 1)
	item.metaStopCh = make(chan struct{})
	item.metaWG.Add(1)
	go item.metaWriterLoop()
}

func (item *CacheItem) stopMetaWriter() {
	if item.metaStopCh == nil {
		return
	}
	close(item.metaStopCh)
	item.metaWG.Wait()
	item.metaStopCh = nil
	item.metaFlushCh = nil
}

func (item *CacheItem) metaWriterLoop() {
	defer item.metaWG.Done()
	ticker := time.NewTicker(metaFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			item.flushMetadata(false)
		case <-item.metaFlushCh:
			item.flushMetadata(false)
		case <-item.metaStopCh:
			item.flushMetadata(true)
			return
		}
	}
}

func (item *CacheItem) markMetadataDirty() {
	item.metaDirty.Store(true)
	if ch := item.metaFlushCh; ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (item *CacheItem) flushMetadata(force bool) {
	if !force && !item.metaDirty.Load() {
		return
	}
	// Clear the flag BEFORE snapshotting: a markMetadataDirty landing after
	// the snapshot then re-arms it and the next tick flushes the newer state.
	// Clearing after the write (as before) dropped that update — the final
	// mutation could sit unflushed until something else dirtied the item.
	item.metaDirty.Store(false)
	item.metaMu.RLock()
	info := item.info
	if len(info.Rs) > 0 {
		rsCopy := make(ranges.Ranges, len(info.Rs))
		copy(rsCopy, info.Rs)
		info.Rs = rsCopy
	}
	item.metaMu.RUnlock()

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
	item.dlMu.Lock()
	dls := item.downloaders
	item.dlMu.Unlock()

	if dls != nil {
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
	if off >= item.info.Size {
		return 0, io.EOF
	}

	// Clamp read size
	readSize := int64(len(p))
	if off+readSize > item.info.Size {
		readSize = item.info.Size - off
		p = p[:readSize]
	}

	r := ranges.Range{Pos: off, Size: readSize}

	// Track cache hit/miss: check if data is already present before downloading
	alreadyCached := item.HasRange(r)
	if alreadyCached {
		item.cache.RecordCacheHit()
	} else {
		item.cache.RecordCacheMiss()
	}

	// Ensure data is on disk (may block until downloaded or ctx canceled)
	item.dlMu.Lock()
	dls := item.downloaders
	item.dlMu.Unlock()
	if dls == nil {
		return 0, errors.New("downloaders closed")
	}

	// Publish the read position BEFORE downloading and reading, not just after.
	// The pool's disk backstop punches everything behind readHead-BackWindow; if
	// readHead still pointed at the previous (forward) position during a seek-back,
	// the backstop could punch the very range we re-download here right back out
	// from under the read — and the buffer's lock-free fast read path would hand
	// the resulting hole back as zeros with no error. Setting readHead to off
	// first pulls the protected frontier over [off, ...) for the whole
	// download-then-read sequence; we advance it to off+n afterward for forward
	// progress. (SetReadHead is a cheap atomic store and non-monotonic by design,
	// so pulling it back on a seek-back is exactly the intended behavior.)
	if item.buf != nil {
		item.buf.SetReadHead(off)
	}

	// Prioritize media-probe-style near-EOF reads so they don't queue behind
	// bulk prefetch, and retry transient failures a few times before surfacing
	// EIO — ffprobe treats a single read error as fatal.
	priority := isProbeRead(off, readSize, item.info.Size)
	if err := dls.DownloadWithRetry(ctx, r, priority); err != nil {
		return 0, fmt.Errorf("download failed: %w", err)
	}

	// Read via the buffer. It serves from its in-RAM block cache when hot
	// and from its sparse disk file otherwise.
	//
	// We do NOT fadvise(DONTNEED) the range just read — that defeats kernel
	// readahead and hurts prefetch. Instead, when DropBehindMargin is set, we
	// drop the cache for data well behind the read head (see DropBehind): the
	// margin keeps readahead and short seek-backs intact, and the bytes stay on
	// disk so a longer seek-back re-reads locally instead of re-downloading.
	if item.buf == nil {
		return 0, errors.New("cache file closed")
	}
	n, err := item.buf.ReadAt(p, off)
	if err == nil || errors.Is(err, io.EOF) {
		// Advance the read position to the end of what we just served. The region
		// we read was already protected by the SetReadHead(off) above; this moves
		// the frontier forward so the backstop can reclaim behind us on the next
		// sequential read, and so RAM eviction protects the active window ahead.
		item.buf.SetReadHead(off + int64(n))
		if margin := item.cache.config.DropBehindMargin; margin > 0 {
			item.buf.DropBehind(off+int64(n), margin)
		}
	}
	if errors.Is(err, buffer.ErrNotPresent) {
		// We checked info.Rs before downloading, so an ErrNotPresent here
		// would mean the metadata is out of sync with the buffer. Surface
		// as EIO-equivalent rather than confusing the caller with the
		// internal sentinel.
		return n, fmt.Errorf("buffer reported missing range at %d+%d: %w", off, len(p), err)
	}
	return n, err
}

// WriteAtNoOverwrite writes only the bytes in p that aren't already cached.
// Returns total p length as n (for io.Writer contract) and the count of
// bytes skipped because they were already present.
//
// The on-item info.Rs range tracker is the authoritative metadata view
// (serialized to JSON on Close); the buffer's internal tracker mirrors it
// after each insert. Keeping both in sync is what lets a reopened item
// resume cached data via the buffer's InitialRanges seed.
func (item *CacheItem) WriteAtNoOverwrite(p []byte, off int64) (n, skipped int, err error) {
	if item.buf == nil {
		return len(p), 0, errors.New("cache file closed")
	}
	writeRange := ranges.Range{Pos: off, Size: int64(len(p))}
	n = len(p)

	item.metaMu.RLock()
	frs := item.info.Rs.FindAll(writeRange)
	item.metaMu.RUnlock()

	for _, fr := range frs {
		if fr.Present {
			skipped += int(fr.R.Size)
			continue
		}
		localOff := fr.R.Pos - off
		if _, werr := item.buf.WriteAt(p[localOff:localOff+fr.R.Size], fr.R.Pos); werr != nil {
			return n, skipped, werr
		}
	}

	item.metaMu.Lock()
	item.info.Rs.Insert(writeRange)
	item.metaMu.Unlock()
	item.markMetadataDirty()
	return n, skipped, nil
}

// onBufferEvict is invoked by the buffer pool after it punches a hole behind
// the read head to keep the DFS cache under its disk limit. It drops the
// reclaimed range from the persisted metadata so a later reopen seeds
// InitialRanges with only what is actually still on disk — otherwise the
// reopened item would claim cached bytes that are now a hole and ReadAt would
// report a phantom missing range. The bytes are simply re-downloaded if the
// reader seeks back into the punched region.
func (item *CacheItem) onBufferEvict(off, length int64) {
	item.metaMu.Lock()
	item.info.Rs.Remove(ranges.Range{Pos: off, Size: length})
	item.metaMu.Unlock()
	item.markMetadataDirty()
}

// HasRange returns true if entire range is on disk
func (item *CacheItem) HasRange(r ranges.Range) bool {
	item.metaMu.RLock()
	defer item.metaMu.RUnlock()
	return item.info.Rs.Present(r)
}

// FindMissing returns portion of r not yet downloaded
func (item *CacheItem) FindMissing(r ranges.Range) ranges.Range {
	item.metaMu.RLock()
	defer item.metaMu.RUnlock()

	// Clip to file size
	if r.End() > item.info.Size {
		r.Size = item.info.Size - r.Pos
	}
	if r.Size <= 0 {
		return ranges.Range{}
	}
	return item.info.Rs.FindMissing(r)
}

// Close closes the cache item and saves metadata. The underlying buffer's
// disk file is NOT removed (DFS persistence across runs is part of the
// design — the user expects re-opens of a previously-cached file to hit
// disk, not re-download).
func (item *CacheItem) Close() error {
	item.closeOnce.Do(func() {
		item.dlMu.Lock()
		dls := item.downloaders
		item.downloaders = nil
		item.dlMu.Unlock()

		if dls != nil {
			if err := dls.Close(nil); err != nil && item.closeErr == nil {
				item.closeErr = err
			}
		}

		item.stopMetaWriter()
		item.flushMetadata(true)

		// Deliberately do NOT nil item.buf: the field is read without
		// synchronization by ReadAtContext/WriteAtNoOverwrite, so nilling it
		// here was a data race (and a latent nil deref) against a straggler
		// read. Left set, a post-Close access gets buffer.ErrClosed instead.
		if item.buf != nil {
			if err := item.buf.Close(); err != nil && item.closeErr == nil {
				item.closeErr = err
			}
		}
	})
	return item.closeErr
}

// Helper functions

func buildCacheKey(entryName, filename string) string {
	// Create safe filesystem key
	return fmt.Sprintf("%s/%s", entryName, filename)
}

// decodeJSONFile stream-decodes a JSON file into v, avoiding the intermediate
// []byte slurp of os.ReadFile + json.Unmarshal. Keeps allocation proportional
// to the decoded object rather than 2× the file size.
func decodeJSONFile(path string, v interface{}) error {
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
