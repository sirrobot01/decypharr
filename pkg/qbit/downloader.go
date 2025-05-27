package qbit

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/cavaliergopher/grab/v3"
	"github.com/sirrobot01/decypharr/internal/utils"
	debrid "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func Download(client *grab.Client, url, filename string, byterange *[2]int64, progressCallback func(int64, int64)) error {
	req, err := grab.NewRequest(filename, url)
	if err != nil {
		return err
	}

	// Set byte range if specified
	if byterange != nil {
		byterangeStr := fmt.Sprintf("%d-%d", byterange[0], byterange[1])
		req.HTTPRequest.Header.Set("Range", "bytes="+byterangeStr)
	}

	resp := client.Do(req)

	t := time.NewTicker(time.Second * 2)
	defer t.Stop()

	var lastReported int64
Loop:
	for {
		select {
		case <-t.C:
			current := resp.BytesComplete()
			speed := int64(resp.BytesPerSecond())
			if current != lastReported {
				if progressCallback != nil {
					progressCallback(current-lastReported, speed)
				}
				lastReported = current
			}
		case <-resp.Done:
			break Loop
		}
	}

	// Report final bytes
	if progressCallback != nil {
		progressCallback(resp.BytesComplete()-lastReported, 0)
	}

	return resp.Err()
}

func (q *QBit) ProcessManualFile(torrent *Torrent) (string, error) {
	debridTorrent := torrent.DebridTorrent
	q.logger.Info().Msgf("Downloading %d files...", len(debridTorrent.Files))
	torrentPath := filepath.Join(q.DownloadFolder, debridTorrent.Arr.Name, utils.RemoveExtension(debridTorrent.OriginalFilename))
	torrentPath = utils.RemoveInvalidChars(torrentPath)
	err := os.MkdirAll(torrentPath, os.ModePerm)
	if err != nil {
		// add previous error to the error and return
		return "", fmt.Errorf("failed to create directory: %s: %v", torrentPath, err)
	}
	q.downloadFiles(torrent, torrentPath)
	return torrentPath, nil
}

func (q *QBit) downloadFiles(torrent *Torrent, parent string) {
	debridTorrent := torrent.DebridTorrent
	var wg sync.WaitGroup

	totalSize := int64(0)
	for _, file := range debridTorrent.GetFiles() {
		totalSize += file.Size
	}
	debridTorrent.Mu.Lock()
	debridTorrent.SizeDownloaded = 0 // Reset downloaded bytes
	debridTorrent.Progress = 0       // Reset progress
	debridTorrent.Mu.Unlock()
	progressCallback := func(downloaded int64, speed int64) {
		debridTorrent.Mu.Lock()
		defer debridTorrent.Mu.Unlock()
		torrent.Mu.Lock()
		defer torrent.Mu.Unlock()

		// Update total downloaded bytes
		debridTorrent.SizeDownloaded += downloaded
		debridTorrent.Speed = speed

		// Calculate overall progress
		if totalSize > 0 {
			debridTorrent.Progress = float64(debridTorrent.SizeDownloaded) / float64(totalSize) * 100
		}
		q.UpdateTorrentMin(torrent, debridTorrent)
	}
	client := &grab.Client{
		UserAgent: "Decypharr[QBitTorrent]",
		HTTPClient: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		},
	}
	errChan := make(chan error, len(debridTorrent.Files))
	for _, file := range debridTorrent.GetFiles() {
		if file.DownloadLink == nil {
			q.logger.Info().Msgf("No download link found for %s", file.Name)
			continue
		}
		wg.Add(1)
		q.downloadSemaphore <- struct{}{}
		go func(file debrid.File) {
			defer wg.Done()
			defer func() { <-q.downloadSemaphore }()
			filename := file.Name

			err := Download(
				client,
				file.DownloadLink.DownloadLink,
				filepath.Join(parent, filename),
				file.ByteRange,
				progressCallback,
			)

			if err != nil {
				q.logger.Error().Msgf("Failed to download %s: %v", filename, err)
				errChan <- err
			} else {
				q.logger.Info().Msgf("Downloaded %s", filename)
			}
		}(file)
	}
	wg.Wait()

	close(errChan)
	var errors []error
	for err := range errChan {
		if err != nil {
			errors = append(errors, err)
		}
	}
	if len(errors) > 0 {
		q.logger.Error().Msgf("Errors occurred during download: %v", errors)
		return
	}
	q.logger.Info().Msgf("Downloaded all files for %s", debridTorrent.Name)
}

func (q *QBit) ProcessSymlink(torrent *Torrent) (string, error) {
	debridTorrent := torrent.DebridTorrent
	files := debridTorrent.Files
	if len(files) == 0 {
		return "", fmt.Errorf("no video files found")
	}
	q.logger.Info().Msgf("Checking symlinks for %d files...", len(files))
	rCloneBase := debridTorrent.MountPath
	torrentPath, err := q.getTorrentPath(rCloneBase, debridTorrent) // /MyTVShow/
	// This returns filename.ext for alldebrid instead of the parent folder filename/
	torrentFolder := torrentPath
	if err != nil {
		return "", fmt.Errorf("failed to get torrent path: %v", err)
	}
	// Check if the torrent path is a file
	torrentRclonePath := filepath.Join(rCloneBase, torrentPath) // leave it as is
	if debridTorrent.Debrid == "alldebrid" && utils.IsMediaFile(torrentPath) {
		// Alldebrid hotfix for single file torrents
		torrentFolder = utils.RemoveExtension(torrentFolder)
		torrentRclonePath = rCloneBase // /mnt/rclone/magnets/  // Remove the filename since it's in the root folder
	}
	torrentSymlinkPath := filepath.Join(q.DownloadFolder, debridTorrent.Arr.Name, torrentFolder) // /mnt/symlinks/{category}/MyTVShow/
	err = os.MkdirAll(torrentSymlinkPath, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("failed to create directory: %s: %v", torrentSymlinkPath, err)
	}

	realPaths := make(map[string]string)
	err = filepath.WalkDir(torrentRclonePath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			filename := d.Name()
			rel, _ := filepath.Rel(torrentRclonePath, path)
			realPaths[filename] = rel
		}
		return nil
	})
	if err != nil {
		q.logger.Warn().Msgf("Error while scanning rclone path: %v", err)
	}

	pending := make(map[string]debridTypes.File)
	for _, file := range files {
		if realRelPath, ok := realPaths[file.Name]; ok {
			file.Path = realRelPath
		}
		pending[file.Path] = file
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	timeout := time.After(30 * time.Minute)
	filePaths := make([]string, 0, len(pending))

	for len(pending) > 0 {
		select {
		case <-ticker.C:
			for path, file := range pending {
				fullFilePath := filepath.Join(torrentRclonePath, file.Path)
				if _, err := os.Stat(fullFilePath); !os.IsNotExist(err) {
					fileSymlinkPath := filepath.Join(torrentSymlinkPath, file.Name)
					if err := os.Symlink(fullFilePath, fileSymlinkPath); err != nil && !os.IsExist(err) {
						q.logger.Debug().Msgf("Failed to create symlink: %s: %v", fileSymlinkPath, err)
					} else {
						filePaths = append(filePaths, fileSymlinkPath)
						delete(pending, path)
						q.logger.Info().Msgf("File is ready: %s", file.Name)
					}
				}
			}
		case <-timeout:
			q.logger.Warn().Msgf("Timeout waiting for files, %d files still pending", len(pending))
			return torrentSymlinkPath, fmt.Errorf("timeout waiting for files: %d files still pending", len(pending))
		}
	}
	if q.SkipPreCache {
		return torrentSymlinkPath, nil
	}

	go func() {

		if err := q.preCacheFile(debridTorrent.Name, filePaths); err != nil {
			q.logger.Error().Msgf("Failed to pre-cache file: %s", err)
		} else {
			q.logger.Trace().Msgf("Pre-cached %d files", len(filePaths))
		}
	}()
	return torrentSymlinkPath, nil
}

func (q *QBit) createSymlinksWebdav(debridTorrent *debrid.Torrent, rclonePath, torrentFolder string) (string, error) {
	files := debridTorrent.Files
	symlinkPath := filepath.Join(q.DownloadFolder, debridTorrent.Arr.Name, torrentFolder) // /mnt/symlinks/{category}/MyTVShow/
	err := os.MkdirAll(symlinkPath, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("failed to create directory: %s: %v", symlinkPath, err)
	}

	remainingFiles := make(map[string]debrid.File)
	for _, file := range files {
		remainingFiles[file.Name] = file
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(30 * time.Minute)
	filePaths := make([]string, 0, len(files))

	for len(remainingFiles) > 0 {
		select {
		case <-ticker.C:
			entries, err := os.ReadDir(rclonePath)
			if err != nil {
				continue
			}

			// Check which files exist in this batch
			for _, entry := range entries {
				filename := entry.Name()
				if file, exists := remainingFiles[filename]; exists {
					fullFilePath := filepath.Join(rclonePath, filename)
					fileSymlinkPath := filepath.Join(symlinkPath, file.Name)

					if err := os.Symlink(fullFilePath, fileSymlinkPath); err != nil && !os.IsExist(err) {
						q.logger.Debug().Msgf("Failed to create symlink: %s: %v", fileSymlinkPath, err)
					} else {
						filePaths = append(filePaths, fileSymlinkPath)
						delete(remainingFiles, filename)
						q.logger.Info().Msgf("File is ready: %s", file.Name)
					}
				}
			}

		case <-timeout:
			q.logger.Warn().Msgf("Timeout waiting for files, %d files still pending", len(remainingFiles))
			return symlinkPath, fmt.Errorf("timeout waiting for files")
		}
	}

	if q.SkipPreCache {
		return symlinkPath, nil
	}

	go func() {

		if err := q.preCacheFile(debridTorrent.Name, filePaths); err != nil {
			q.logger.Error().Msgf("Failed to pre-cache file: %s", err)
		} else {
			q.logger.Debug().Msgf("Pre-cached %d files", len(filePaths))
		}
	}() // Pre-cache the files in the background
	// Pre-cache the first 256KB and 1MB of the file
	return symlinkPath, nil
}

func (q *QBit) createSymlinks(debridTorrent *debrid.Torrent, rclonePath, torrentFolder string) (string, error) {
	files := debridTorrent.Files
	symlinkPath := filepath.Join(q.DownloadFolder, debridTorrent.Arr.Name, torrentFolder) // /mnt/symlinks/{category}/MyTVShow/
	err := os.MkdirAll(symlinkPath, os.ModePerm)
	if err != nil {
		return "", fmt.Errorf("failed to create directory: %s: %v", symlinkPath, err)
	}

	remainingFiles := make(map[string]debrid.File)
	for _, file := range files {
		remainingFiles[file.Path] = file
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(30 * time.Minute)
	filePaths := make([]string, 0, len(files))

	for len(remainingFiles) > 0 {
		select {
		case <-ticker.C:
			entries, err := os.ReadDir(rclonePath)
			if err != nil {
				continue
			}

			// Check which files exist in this batch
			for _, entry := range entries {
				filename := entry.Name()
				if file, exists := remainingFiles[filename]; exists {
					fullFilePath := filepath.Join(rclonePath, filename)
					fileSymlinkPath := filepath.Join(symlinkPath, file.Name)

					if err := os.Symlink(fullFilePath, fileSymlinkPath); err != nil && !os.IsExist(err) {
						q.logger.Debug().Msgf("Failed to create symlink: %s: %v", fileSymlinkPath, err)
					} else {
						filePaths = append(filePaths, fileSymlinkPath)
						delete(remainingFiles, filename)
						q.logger.Info().Msgf("File is ready: %s", file.Name)
					}
				}
			}

		case <-timeout:
			q.logger.Warn().Msgf("Timeout waiting for files, %d files still pending", len(remainingFiles))
			return symlinkPath, fmt.Errorf("timeout waiting for files")
		}
	}

	if q.SkipPreCache {
		return symlinkPath, nil
	}

	go func() {

		if err := q.preCacheFile(debridTorrent.Name, filePaths); err != nil {
			q.logger.Error().Msgf("Failed to pre-cache file: %s", err)
		} else {
			q.logger.Trace().Msgf("Pre-cached %d files", len(filePaths))
		}
	}() // Pre-cache the files in the background
	return symlinkPath, nil
}

func (q *QBit) getTorrentPath(rclonePath string, debridTorrent *debrid.Torrent) (string, error) {
	for {
		torrentPath, err := debridTorrent.GetMountFolder(rclonePath)
		if err == nil {
			q.logger.Debug().Msgf("Found torrent path: %s", torrentPath)
			return torrentPath, err
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (q *QBit) preCacheFile(name string, filePaths []string) error {
	q.logger.Trace().Msgf("Pre-caching torrent: %s", name)
	if len(filePaths) == 0 {
		return fmt.Errorf("no file paths provided")
	}

	for _, filePath := range filePaths {
		err := func(f string) error {

			file, err := os.Open(f)
			if err != nil {
				if os.IsNotExist(err) {
					// File has probably been moved by arr, return silently
					return nil
				}
				return fmt.Errorf("failed to open file: %s: %v", f, err)
			}
			defer file.Close()

			// Pre-cache the file header (first 256KB) using 16KB chunks.
			if err := q.readSmallChunks(file, 0, 256*1024, 16*1024); err != nil {
				return err
			}
			if err := q.readSmallChunks(file, 1024*1024, 64*1024, 16*1024); err != nil {
				return err
			}
			return nil
		}(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func (q *QBit) readSmallChunks(file *os.File, startPos int64, totalToRead int, chunkSize int) error {
	_, err := file.Seek(startPos, 0)
	if err != nil {
		return err
	}

	buf := make([]byte, chunkSize)
	bytesRemaining := totalToRead

	for bytesRemaining > 0 {
		toRead := chunkSize
		if bytesRemaining < chunkSize {
			toRead = bytesRemaining
		}

		n, err := file.Read(buf[:toRead])
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		bytesRemaining -= n
	}
	return nil
}
