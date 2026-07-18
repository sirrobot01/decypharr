package wire

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// LocalCacheWorker handles copying files from rclone mounts to local storage,
// replacing symlinks with real files. Inspired by RealFin's local-cache approach.
type LocalCacheWorker struct {
	cfg       config.LocalCache
	mediaDirs []string
	sem       chan struct{} // concurrency limiter
	logger    zerolog.Logger
	mu        sync.Mutex
	active    map[string]bool // tracks files currently being copied
}

func NewLocalCacheWorker() *LocalCacheWorker {
	cfg := config.Get()
	lc := cfg.LocalCache

	if lc.MinFreeMB == 0 {
		lc.MinFreeMB = 20000 // 20 GB default
	}
	if lc.StaleHours == 0 {
		lc.StaleHours = 24
	}
	if lc.MaxParallel == 0 {
		lc.MaxParallel = 2
	}
	if lc.ScanInterval == 0 {
		lc.ScanInterval = 1800 // 30 min
	}
	if lc.RcloneIndicator == "" {
		lc.RcloneIndicator = "decypharr"
	}

	// Explicit cache dirs win. This matters when the arrs import the symlink
	// itself into the media library: the download folder is then bypassed, so
	// caching it would consume disk without changing what the media server
	// reads. Pointing CacheDirs at the library replaces those symlinks in place.
	var mediaDirs []string
	if len(lc.CacheDirs) > 0 {
		mediaDirs = append(mediaDirs, lc.CacheDirs...)
	} else {
		// Fall back to the download folder + categories.
		dlFolder := cfg.QBitTorrent.DownloadFolder
		if len(cfg.QBitTorrent.Categories) > 0 {
			for _, cat := range cfg.QBitTorrent.Categories {
				mediaDirs = append(mediaDirs, filepath.Join(dlFolder, cat))
			}
		} else {
			mediaDirs = []string{dlFolder}
		}
	}

	return &LocalCacheWorker{
		cfg:       lc,
		mediaDirs: mediaDirs,
		sem:       make(chan struct{}, lc.MaxParallel),
		logger:    logger.Default().With().Str("component", "local-cache").Logger(),
		active:    make(map[string]bool),
	}
}

// CopySymlinksInDir is called immediately after symlinks are created.
// It finds all symlinks in the given directory pointing to rclone mounts
// and starts copying them to local storage in the background.
func (w *LocalCacheWorker) CopySymlinksInDir(dir string) {
	go func() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			w.logger.Error().Err(err).Msgf("Failed to read dir: %s", dir)
			return
		}

		for _, entry := range entries {
			if entry.IsDir() {
				// Recurse into subdirectories
				w.CopySymlinksInDir(filepath.Join(dir, entry.Name()))
				continue
			}

			fullPath := filepath.Join(dir, entry.Name())
			info, err := os.Lstat(fullPath)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				continue
			}

			target, err := filepath.EvalSymlinks(fullPath)
			if err != nil {
				continue
			}

			if strings.Contains(target, w.cfg.RcloneIndicator) {
				w.copyFileAsync(fullPath, target)
			}
		}
	}()
}

// copyFileAsync starts a background copy for a single symlink
func (w *LocalCacheWorker) copyFileAsync(symlinkPath, targetPath string) {
	w.mu.Lock()
	if w.active[symlinkPath] {
		w.mu.Unlock()
		return // already being copied
	}
	w.active[symlinkPath] = true
	w.mu.Unlock()

	go func() {
		defer func() {
			w.mu.Lock()
			delete(w.active, symlinkPath)
			w.mu.Unlock()
		}()

		w.sem <- struct{}{}        // acquire slot
		defer func() { <-w.sem }() // release slot

		if err := w.downloadAndReplace(symlinkPath, targetPath); err != nil {
			w.logger.Error().Err(err).Msgf("Failed to cache: %s", filepath.Base(symlinkPath))
		}
	}()
}

// downloadAndReplace copies the file from rclone mount to local,
// using a .part file during the copy, then replaces the symlink.
func (w *LocalCacheWorker) downloadAndReplace(symlinkPath, targetPath string) error {
	partFile := symlinkPath + ".part"
	relPath := filepath.Base(symlinkPath)

	// Check source is readable
	srcInfo, err := os.Stat(targetPath)
	if err != nil {
		return fmt.Errorf("source unreadable (mount down?): %s: %v", relPath, err)
	}
	expectedSize := srcInfo.Size()
	sizeMB := expectedSize / 1048576

	// Remove existing .part (restart)
	os.Remove(partFile)

	start := time.Now()
	w.logger.Info().Msgf("DOWNLOADING: %s (%d MB)", relPath, sizeMB)

	// Copy through FUSE mount
	if err := copyFile(targetPath, partFile); err != nil {
		os.Remove(partFile)
		return fmt.Errorf("copy failed: %s: %v", relPath, err)
	}

	// Verify size
	partInfo, err := os.Stat(partFile)
	if err != nil {
		os.Remove(partFile)
		return fmt.Errorf("stat .part failed: %s: %v", relPath, err)
	}

	if partInfo.Size() != expectedSize {
		os.Remove(partFile)
		return fmt.Errorf("size mismatch: %s (expected %d, got %d)", relPath, expectedSize, partInfo.Size())
	}

	// Replace symlink with real file
	if err := os.Remove(symlinkPath); err != nil {
		os.Remove(partFile)
		return fmt.Errorf("failed to remove symlink: %s: %v", relPath, err)
	}

	if err := os.Rename(partFile, symlinkPath); err != nil {
		// Try to restore the symlink on failure
		_ = os.Symlink(targetPath, symlinkPath)
		os.Remove(partFile)
		return fmt.Errorf("failed to rename .part: %s: %v", relPath, err)
	}

	elapsed := time.Since(start)
	speedMB := int64(0)
	if elapsed.Seconds() > 0 {
		speedMB = sizeMB / int64(elapsed.Seconds())
	}
	w.logger.Info().Msgf("COMPLETE: %s (%d MB in %s, ~%d MB/s)", relPath, sizeMB, elapsed.Round(time.Second), speedMB)

	return nil
}

// copyFile copies src to dst with a 1MB buffer
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	buf := make([]byte, 1024*1024) // 1MB buffer
	_, err = io.CopyBuffer(dstFile, srcFile, buf)
	if err != nil {
		return err
	}

	return dstFile.Sync()
}

// RunBackgroundScan periodically scans media directories for symlinks
// pointing to rclone mounts that haven't been cached yet.
// This catches any symlinks missed by the instant trigger.
func (w *LocalCacheWorker) RunBackgroundScan(stop <-chan struct{}) {
	w.logger.Info().Msgf("Background scan started (interval: %ds, dirs: %v)", w.cfg.ScanInterval, w.mediaDirs)

	// Run initial scan
	w.scan()

	ticker := time.NewTicker(time.Duration(w.cfg.ScanInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			w.cleanupStale()
			w.scan()
		case <-stop:
			w.logger.Info().Msg("Background scan stopped")
			return
		}
	}
}

func (w *LocalCacheWorker) scan() {
	w.logger.Debug().Msg("Scanning for uncached symlinks...")

	var found, started int
	for _, dir := range w.mediaDirs {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}

		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil // skip errors
			}
			if info.Mode()&os.ModeSymlink == 0 {
				// filepath.Walk follows symlinks, so we need Lstat
				linfo, lerr := os.Lstat(path)
				if lerr != nil || linfo.Mode()&os.ModeSymlink == 0 {
					return nil
				}
			}

			target, err := filepath.EvalSymlinks(path)
			if err != nil {
				return nil
			}

			if strings.Contains(target, w.cfg.RcloneIndicator) {
				found++
				// Check not already being downloaded (.part exists)
				if _, err := os.Stat(path + ".part"); err == nil {
					return nil // .part exists, already in progress
				}
				w.copyFileAsync(path, target)
				started++
			}
			return nil
		})
		if err != nil {
			w.logger.Error().Err(err).Msgf("Error scanning: %s", dir)
		}
	}

	if found > 0 {
		w.logger.Info().Msgf("Scan: found %d rclone symlinks, started %d copies", found, started)
	} else {
		w.logger.Debug().Msg("Scan: no uncached symlinks found")
	}
}

func (w *LocalCacheWorker) cleanupStale() {
	staleThreshold := time.Duration(w.cfg.StaleHours) * time.Hour

	for _, dir := range w.mediaDirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			if strings.HasSuffix(path, ".part") {
				if time.Since(info.ModTime()) > staleThreshold {
					w.logger.Info().Msgf("CLEANUP: Removing stale .part: %s", utils.FormatSize(info.Size()))
					os.Remove(path)
				}
			}
			return nil
		})
	}
}

// GetStatus returns current local cache status for the dashboard
func (w *LocalCacheWorker) GetStatus() LocalCacheStatus {
	w.mu.Lock()
	activeCount := len(w.active)
	activePaths := make([]string, 0, activeCount)
	for p := range w.active {
		activePaths = append(activePaths, p)
	}
	w.mu.Unlock()

	return LocalCacheStatus{
		Enabled:     w.cfg.Enabled,
		Active:      activeCount,
		ActiveFiles: activePaths,
	}
}

type LocalCacheStatus struct {
	Enabled     bool     `json:"enabled"`
	Active      int      `json:"active"`
	ActiveFiles []string `json:"active_files,omitempty"`
}
