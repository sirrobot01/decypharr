package repair

import (
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"os"
	"path/filepath"
)

func fileIsSymlinked(file string) bool {
	info, err := os.Lstat(file)
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeSymlink != 0
}

func getSymlinkTarget(file string) string {
	if fileIsSymlinked(file) {
		target, err := os.Readlink(file)
		if err != nil {
			return ""
		}
		if !filepath.IsAbs(target) {
			dir := filepath.Dir(file)
			target = filepath.Join(dir, target)
		}
		return target
	}
	return ""
}

func fileIsReadable(filePath string) error {
	// First check if file exists and is accessible
	info, err := os.Stat(filePath)
	if err != nil {
		return err
	}

	// Check if it's a regular file
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file")
	}

	// Try to read the first 1024 bytes
	err = checkFileStart(filePath)
	if err != nil {
		return err
	}

	return nil
}

func checkFileStart(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	// Read first 1kb
	buffer := make([]byte, 1024)
	_, err = f.Read(buffer)
	if err != nil {
		return err
	}
	return nil
}

func collectFiles(media arr.Content) map[string][]arr.ContentFile {
	uniqueParents := make(map[string][]arr.ContentFile)
	files := media.Files
	for _, file := range files {
		target := getSymlinkTarget(file.Path)
		if target != "" {
			file.IsSymlink = true
			dir, f := filepath.Split(target)
			torrentNamePath := filepath.Clean(dir)
			// Set target path folder/file.mkv
			file.TargetPath = f
			uniqueParents[torrentNamePath] = append(uniqueParents[torrentNamePath], file)
		}
	}
	return uniqueParents
}

func (r *Repair) checkTorrentFiles(torrentPath string, files []arr.ContentFile, clients map[string]types.Client, caches map[string]*store.Cache) []arr.ContentFile {
	brokenFiles := make([]arr.ContentFile, 0)

	emptyFiles := make([]arr.ContentFile, 0)

	r.logger.Debug().Msgf("Checking %s", torrentPath)

	// Get the debrid client
	dir := filepath.Dir(torrentPath)
	debridName := r.findDebridForPath(dir, clients)
	if debridName == "" {
		r.logger.Debug().Msgf("No debrid found for %s. Skipping", torrentPath)
		return emptyFiles
	}

	cache, ok := caches[debridName]
	if !ok {
		r.logger.Debug().Msgf("No cache found for %s. Skipping", debridName)
		return emptyFiles
	}
	tor, ok := r.torrentsMap.Load(debridName)
	if !ok {
		r.logger.Debug().Msgf("Could not find torrents for %s. Skipping", debridName)
		return emptyFiles
	}

	torrentsMap := tor.(map[string]store.CachedTorrent)

	// Check if torrent exists
	torrentName := filepath.Clean(filepath.Base(torrentPath))
	torrent, ok := torrentsMap[torrentName]
	if !ok {
		r.logger.Debug().Msgf("Can't find torrent %s in %s. Marking as broken", torrentName, debridName)
		// Return all files as broken
		return files
	}

	// Batch check files
	filePaths := make([]string, len(files))
	for i, file := range files {
		filePaths[i] = file.TargetPath
	}

	brokenFilePaths := cache.GetBrokenFiles(&torrent, filePaths)
	if len(brokenFilePaths) > 0 {
		r.logger.Debug().Msgf("%d broken files found in %s", len(brokenFilePaths), torrentName)

		// Create a set for O(1) lookup
		brokenSet := make(map[string]bool, len(brokenFilePaths))
		for _, brokenPath := range brokenFilePaths {
			brokenSet[brokenPath] = true
		}

		// Filter broken files
		for _, contentFile := range files {
			if brokenSet[contentFile.TargetPath] {
				brokenFiles = append(brokenFiles, contentFile)
			}
		}
	}

	return brokenFiles
}

func (r *Repair) findDebridForPath(dir string, clients map[string]types.Client) string {
	// Check cache first
	if debridName, exists := r.debridPathCache.Load(dir); exists {
		return debridName.(string)
	}

	// Find debrid client
	for _, client := range clients {
		mountPath := client.GetMountPath()
		if mountPath == "" {
			continue
		}

		if filepath.Clean(mountPath) == filepath.Clean(dir) {
			debridName := client.Name()

			// Cache the result
			r.debridPathCache.Store(dir, debridName)

			return debridName
		}
	}

	// Cache empty result to avoid repeated lookups
	r.debridPathCache.Store(dir, "")

	return ""
}
