package manager

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/notifications"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sourcegraph/conc/pool"
)

type Downloader struct {
	manager      *Manager
	strmURL      string
	mountPath    string
	dest         string
	logger       zerolog.Logger
	maxDownloads int
}

// NewDownloadManager creates a new strm manager
func NewDownloadManager(manager *Manager) *Downloader {
	cfg := config.Get()
	strmURL := cfg.AppURL
	if strmURL == "" {
		bindAddress := cfg.BindAddress
		if bindAddress == "" {
			bindAddress = "localhost"
		}

		strmURL = fmt.Sprintf("http://%s:%s", bindAddress, cfg.Port)
	}
	return &Downloader{
		manager:      manager,
		strmURL:      strmURL,
		mountPath:    cfg.Mount.MountPath,
		logger:       manager.logger.With().Str("component", "downloader").Logger(),
		dest:         cfg.DownloadFolder,
		maxDownloads: cfg.MaxDownloads,
	}
}

func (d *Downloader) download(torrent *storage.Entry) error {
	var (
		isMultiSeason bool
		seasons       []SeasonInfo
	)
	if !torrent.SkipMultiSeason {
		isMultiSeason, seasons = d.detectMultiSeason(torrent)
	}
	torrentMountPath := d.manager.GetTorrentMountPath(torrent)
	if isMultiSeason {

		seasonResults := convertToMultiSeason(torrent, seasons)
		for _, result := range seasonResults {
			if err := d.manager.queue.Add(result); err != nil {
				d.logger.Error().Err(err).Msgf("Failed to save season torrent")
				continue
			}
			// Then process the symlinks for each season torrent
			if err := d.process(result, torrentMountPath); err != nil {
				d.markAsError(result, err)
			}
		}
	}
	return d.process(torrent, torrentMountPath)
}

func (d *Downloader) process(entry *storage.Entry, mountPath string) error {
	switch entry.Action {
	case config.DownloadActionDownload:
		return d.processDownload(entry)
	case config.DownloadActionSymlink:
		return d.processSymlink(entry, mountPath)
	case config.DownloadActionStrm:
		return d.processStrm(entry)
	case config.DownloadActionNone:
		d.markAsCompleted(entry)
		// Remove entry from queue
		_ = d.manager.queue.Delete(entry.InfoHash, nil)
		return nil
	default:
		return d.processSymlink(entry, mountPath)
	}
}

func (d *Downloader) markAsCompleted(entry *storage.Entry) {
	// Mark as completed
	entry.MarkAsCompleted(entry.DownloadPath())
	_ = d.manager.queue.Update(entry)

	// Send notification
	msg := fmt.Sprintf("Download completed: %s [%s] -> %s", entry.Name, entry.Category, entry.DownloadPath())
	d.manager.Notifications.Notify(notifications.Event{
		Type:    config.EventDownloadComplete,
		Status:  "success",
		Entry:   entry,
		Message: msg,
	})

	// Trigger arr refresh
	go func() {
		a := d.manager.arr.GetOrCreate(entry.Category)
		a.Refresh()
	}()
}

func (d *Downloader) markAsError(entry *storage.Entry, err error) {
	d.logger.Error().Err(err).Str("name", entry.Name).Msg("Failed to process action")
	entry.MarkAsError(err)
	_ = d.manager.queue.Update(entry)

	// Send error notification
	msg := fmt.Sprintf("Download failed: %s [%s] - %s", entry.Name, entry.Category, err.Error())
	d.manager.Notifications.Notify(notifications.Event{
		Type:    config.EventDownloadFailed,
		Status:  "error",
		Entry:   entry,
		Message: msg,
		Error:   err,
	})
}

// processSymlink creates symlinks for torrent files
func (d *Downloader) processSymlink(entry *storage.Entry, mountPath string) error {
	files := entry.GetActiveFiles()
	torrentSymlinkPath := entry.DownloadPath()
	d.logger.Info().Str("mount_path", mountPath).Msgf("Creating symlinks for %d files in %s", len(files), torrentSymlinkPath)

	// Create symlink directory
	err := os.MkdirAll(torrentSymlinkPath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create directory: %s: %v", torrentSymlinkPath, err)
	}

	// Track pending files
	remainingFiles := make(map[string]*storage.File)
	for _, file := range files {
		remainingFiles[file.Name] = file
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(30 * time.Minute)
	filePaths := make([]string, 0, len(remainingFiles))

	var checkDirectory func(string) // Recursive function
	checkDirectory = func(dirPath string) {
		entries, err := os.ReadDir(dirPath)
		if err != nil {
			return
		}

		for _, item := range entries {
			entryName := item.Name()
			fullPath := filepath.Join(dirPath, entryName)

			// Check if this matches a remaining file
			if file, exists := remainingFiles[entryName]; exists {
				fileSymlinkPath := filepath.Join(torrentSymlinkPath, file.Name)

				if err := os.Symlink(fullPath, fileSymlinkPath); err == nil || os.IsExist(err) {
					filePaths = append(filePaths, fileSymlinkPath)
					delete(remainingFiles, entryName)
					d.logger.Info().Msgf("File is ready: %s/%s", entry.GetFolder(), file.Name)
				}
			} else if item.IsDir() {
				// If not found and it's a directory, check inside
				checkDirectory(fullPath)
			}
		}
	}

	for len(remainingFiles) > 0 {
		select {
		case <-ticker.C:
			checkDirectory(mountPath)

		case <-timeout:
			return fmt.Errorf("timeout waiting for files: %d files still pending", len(remainingFiles))
		}
	}

	entry.IsDownloading = true
	_ = d.manager.queue.Update(entry)

	// Run ffprobe on files to warm cache and trigger imports
	if !d.manager.config.SkipPreCache && len(filePaths) > 0 {
		probeFiles := filePaths
		if len(probeFiles) > MaxNZBPreCacheFiles {
			probeFiles = probeFiles[:MaxNZBPreCacheFiles]
		}
		d.logger.Debug().Int("files", len(probeFiles)).Msgf("Running ffprobe on %s", entry.Name)
		if err := d.manager.RunFFprobe(probeFiles); err != nil {
			d.logger.Error().Msgf("Failed to run ffprobe: %s", err)
		} else {
			d.logger.Debug().Str("entry", entry.Name).Msgf("Ran ffprobe on %d/%d files", len(probeFiles), len(filePaths))
		}
	}

	d.markAsCompleted(entry)

	return nil
}

// processDownload downloads all files for an entry with progress tracking
// For torrents: uses HTTP download from debrid
// For NZBs: uses parallel NNTP segment download
func (d *Downloader) processDownload(entry *storage.Entry) error {
	// Check if this is a usenet entry
	if entry.IsNZB() {
		return d.processUsenetDownload(entry)
	}
	return d.processTorrentDownload(entry)
}

// processTorrentDownload downloads files from debrid via HTTP
func (d *Downloader) processTorrentDownload(entry *storage.Entry) error {
	files := entry.GetActiveFiles()
	d.logger.Info().Msgf("Downloading %d files...", len(files))

	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}
	downloadedFolder := entry.DownloadPath()
	if err := os.MkdirAll(downloadedFolder, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create download directory: %s: %v", downloadedFolder, err)
	}
	entry.SizeDownloaded = 0
	entry.IsDownloading = true
	entry.Progress = 0

	var progressMu sync.Mutex
	progressCallback := func(downloaded int64, speed int64) {
		progressMu.Lock()
		defer progressMu.Unlock()

		entry.SizeDownloaded += downloaded
		entry.Speed = speed
		if totalSize > 0 {
			entry.Progress = float64(entry.SizeDownloaded) / float64(totalSize)
		}
		entry.UpdatedAt = time.Now()
		_ = d.manager.queue.Update(entry)
	}

	// Resolve download links before spawning goroutines
	type downloadTask struct {
		file *storage.File
		link string
	}
	var tasks []downloadTask
	for _, file := range files {
		downloadLink, err := d.manager.linkService.GetLink(context.Background(), entry, file.Name)
		if err != nil {
			d.logger.Error().Msgf("Failed to get download link for %s: %v", file.Name, err)
			continue
		}
		tasks = append(tasks, downloadTask{file: file, link: downloadLink.DownloadLink})
	}

	p := pool.New().WithErrors().WithFirstError().WithMaxGoroutines(d.maxDownloads)
	for _, task := range tasks {
		p.Go(func() error {
			if err := d.localDownloader(
				task.link,
				filepath.Join(downloadedFolder, task.file.Name),
				task.file.ByteRange,
				progressCallback,
			); err != nil {
				d.logger.Error().Msgf("Failed to download %s: %v", task.file.Name, err)
				return err
			}
			d.logger.Info().Msgf("Downloaded %s", task.file.Name)
			return nil
		})
	}

	if err := p.Wait(); err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	d.markAsCompleted(entry)
	d.logger.Info().Msgf("Downloaded all files for %s", entry.Name)
	return nil
}

// processUsenetDownload downloads NZB files via parallel NNTP segment fetching
func (d *Downloader) processUsenetDownload(entry *storage.Entry) error {
	if d.manager.usenet == nil {
		return fmt.Errorf("usenet client not configured")
	}

	files := entry.GetActiveFiles()
	d.logger.Info().Msgf("Downloading %d NZB files via usenet...", len(files))

	downloadedFolder := entry.DownloadPath()
	if err := os.MkdirAll(downloadedFolder, os.ModePerm); err != nil {
		return fmt.Errorf("failed to create download directory: %s: %v", downloadedFolder, err)
	}

	totalSize := int64(0)
	for _, file := range files {
		totalSize += file.Size
	}

	entry.SizeDownloaded = 0
	entry.Progress = 0
	entry.IsDownloading = true
	_ = d.manager.queue.Update(entry)

	var progressMu sync.Mutex
	// Track per-file progress so we can compute the global total across all files
	fileProgress := make(map[string]int64)

	p := pool.New().WithErrors().WithFirstError().WithMaxGoroutines(d.maxDownloads)
	for _, file := range files {
		p.Go(func() error {
			destPath := filepath.Join(downloadedFolder, file.Name)
			destFile, err := os.Create(destPath)
			if err != nil {
				return fmt.Errorf("failed to create file %s: %w", file.Name, err)
			}
			defer destFile.Close()

			progressCallback := func(downloaded int64, speed int64) {
				progressMu.Lock()
				defer progressMu.Unlock()

				prev := fileProgress[file.Name]
				fileProgress[file.Name] = downloaded
				entry.SizeDownloaded += downloaded - prev
				entry.Speed = speed
				if totalSize > 0 {
					entry.Progress = float64(entry.SizeDownloaded) / float64(totalSize)
				}
				entry.UpdatedAt = time.Now()
				_ = d.manager.queue.Update(entry)
			}

			if err := d.manager.usenet.Download(d.manager.ctx, entry.InfoHash, file.Name, destFile, progressCallback); err != nil {
				_ = os.Remove(destPath)
				return fmt.Errorf("failed to download %s: %w", file.Name, err)
			}

			d.logger.Info().Msgf("Downloaded NZB file: %s", file.Name)
			return nil
		})
	}

	err := p.Wait()

	if err != nil {
		entry.MarkAsError(err)
		_ = d.manager.queue.Update(entry)
		return fmt.Errorf("NZB download failed: %w", err)
	}

	d.markAsCompleted(entry)
	d.logger.Info().Msgf("Downloaded all NZB files for %s", entry.Name)
	return nil
}

// processStrm creates symlinks for torrent files
func (d *Downloader) processStrm(torrent *storage.Entry) error {

	files := torrent.GetActiveFiles()
	d.logger.Info().Msgf("Creating .strm for %d files ...", len(files))

	torrentSymlinkPath := torrent.DownloadPath()

	// Create symlink directory
	err := os.MkdirAll(torrentSymlinkPath, os.ModePerm)
	if err != nil {
		return fmt.Errorf("failed to create directory: %s: %v", torrentSymlinkPath, err)
	}

	for _, file := range files {
		strmFilePath := filepath.Join(torrentSymlinkPath, file.Name+".strm")
		streamURL, err := url.JoinPath(
			d.strmURL,
			"webdav",
			"stream",
			EntryAllFolder,
			url.PathEscape(torrent.GetFolder()),
			url.PathEscape(file.Name),
		)
		if err != nil {
			continue
		}
		if err := os.WriteFile(strmFilePath, []byte(streamURL), 0644); err != nil {
			return fmt.Errorf("failed to create .strm file: %s: %v", strmFilePath, err)
		}
	}
	d.markAsCompleted(torrent)
	d.logger.Info().Str("destination", torrentSymlinkPath).Msgf("Created .strm files for %s", torrent.Name)
	return nil
}

func (d *Downloader) detectMultiSeason(torrent *storage.Entry) (bool, []SeasonInfo) {
	torrentName := torrent.Name
	files := torrent.GetActiveFiles()

	// Find all seasons present in the files
	seasonsFound := findAllSeasons(files)

	// Check if this is actually a multi-season torrent
	isMultiSeason := len(seasonsFound) > 1 || hasMultiSeasonIndicators(torrentName)

	if !isMultiSeason {
		return false, nil
	}

	d.logger.Info().Msgf("Multi-season torrent detected with seasons: %v", getSortedSeasons(seasonsFound))

	// Group files by season
	seasonGroups := groupFilesBySeason(files, seasonsFound)

	// Create SeasonInfo objects with proper naming
	var seasons []SeasonInfo
	for seasonNum, seasonFiles := range seasonGroups {
		if len(seasonFiles) == 0 {
			continue
		}

		// Generate season-specific name preserving all metadata
		seasonName := replaceMultiSeasonPattern(torrentName, seasonNum)

		seasons = append(seasons, SeasonInfo{
			SeasonNumber: seasonNum,
			Files:        seasonFiles,
			InfoHash:     generateSeasonHash(torrent.InfoHash, seasonNum),
			Name:         seasonName,
		})
	}

	return true, seasons
}

// localDownloader downloads a file via HTTP with progress reporting
func (d *Downloader) localDownloader(downloadURL, filename string, byterange *[2]int64, progressCallback func(int64, int64)) error {
	req, err := http.NewRequestWithContext(d.manager.ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Decypharr[QBitTorrent]")

	// Set byte range if specified
	if byterange != nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byterange[0], byterange[1]))
	}

	resp, err := d.manager.streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("unexpected status %d for %s", resp.StatusCode, downloadURL)
	}

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	// Use a progress-tracking writer that reports every 2 seconds
	var downloaded atomic.Int64
	startTime := time.Now()
	var lastReported int64

	t := time.NewTicker(2 * time.Second)
	defer t.Stop()

	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(f, io.TeeReader(resp.Body, &countWriter{n: &downloaded}))
		done <- err
	}()

	for {
		select {
		case <-t.C:
			current := downloaded.Load()
			elapsed := time.Since(startTime).Seconds()
			speed := int64(0)
			if elapsed > 0 {
				speed = int64(float64(current) / elapsed)
			}
			if current != lastReported && progressCallback != nil {
				progressCallback(current-lastReported, speed)
				lastReported = current
			}
		case err := <-done:
			// Report final bytes
			if progressCallback != nil {
				final := downloaded.Load()
				if final != lastReported {
					progressCallback(final-lastReported, 0)
				}
			}
			return err
		}
	}
}

// countWriter is a minimal io.Writer that atomically counts bytes written
type countWriter struct {
	n *atomic.Int64
}

func (cw *countWriter) Write(p []byte) (int, error) {
	n := len(p)
	cw.n.Add(int64(n))
	return n, nil
}
