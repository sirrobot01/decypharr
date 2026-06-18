package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

// SubtitleExts is the set of subtitle file extensions recognised across all FUSE backends.
var SubtitleExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true,
	".sub": true, ".vtt": true, ".idx": true,
}

// IsSubtitleFile reports whether name has a recognised subtitle extension.
func IsSubtitleFile(name string) bool {
	dot := strings.LastIndex(name, ".")
	if dot < 0 {
		return false
	}
	return SubtitleExts[strings.ToLower(name[dot:])]
}

const sidecarCacheTTL = time.Second

type sidecarCacheEntry struct {
	files []FileInfo
	at    time.Time
}

var (
	sidecarCacheMu sync.Mutex
	sidecarCache   = map[string]sidecarCacheEntry{}
)

// GetInfoHashByName resolves a torrent folder name to its InfoHash.
// Returns an empty string if not found.
func (m *Manager) GetInfoHashByName(torrentName string) string {
	item, err := m.storage.GetEntryItem(torrentName)
	if err != nil {
		return ""
	}
	first, err := item.GetFirstFile()
	if err != nil {
		return ""
	}
	return first.InfoHash
}

func sidecarDir() string {
	return filepath.Join(config.GetMainPath(), "sidecars")
}

func sidecarDirForHash(infoHash string) string {
	return filepath.Join(sidecarDir(), infoHash)
}

func sidecarFilePath(infoHash, filename string) string {
	return filepath.Join(sidecarDirForHash(infoHash), filename)
}

// InjectSidecarFile writes a sidecar file (e.g. subtitle) to disk atomically,
// keyed by InfoHash. A temp file is written first and then renamed into place so
// readers (FUSE, Plex) never see a partial write.
func (m *Manager) InjectSidecarFile(infoHash, filename string, content []byte) error {
	dir := sidecarDirForHash(infoHash)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	finalPath := filepath.Join(dir, filename)
	tmp, err := os.CreateTemp(dir, ".sidecar-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp sidecar: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err = tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write temp sidecar: %w", err)
	}
	if err = tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close temp sidecar: %w", err)
	}
	if err = os.Rename(tmpPath, finalPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename sidecar: %w", err)
	}
	// Invalidate the cache so the new file is visible immediately.
	sidecarCacheMu.Lock()
	delete(sidecarCache, infoHash)
	sidecarCacheMu.Unlock()
	return nil
}

// GetSidecars returns FileInfo entries for all sidecar files stored under infoHash.
// Results are cached for sidecarCacheTTL to absorb Plex/Jellyfin scan storms.
func (m *Manager) GetSidecars(infoHash string) []FileInfo {
	now := time.Now()
	sidecarCacheMu.Lock()
	if ce, ok := sidecarCache[infoHash]; ok && now.Sub(ce.at) < sidecarCacheTTL {
		sidecarCacheMu.Unlock()
		return ce.files
	}
	sidecarCacheMu.Unlock()

	dir := sidecarDirForHash(infoHash)
	entries, err := os.ReadDir(dir)
	var infos []FileInfo
	if err == nil {
		infos = make([]FileInfo, 0, len(entries))
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			p := filepath.Join(dir, e.Name())
			infos = append(infos, FileInfo{
				name:        e.Name(),
				size:        info.Size(),
				modTime:     info.ModTime(),
				isDir:       false,
				infohash:    infoHash,
				sidecarPath: p,
			})
		}
	}

	sidecarCacheMu.Lock()
	sidecarCache[infoHash] = sidecarCacheEntry{files: infos, at: now}
	sidecarCacheMu.Unlock()
	return infos
}

// GetSidecarFile returns a FileInfo for a specific sidecar file, or nil if not found.
func (m *Manager) GetSidecarFile(infoHash, filename string) *FileInfo {
	p := sidecarFilePath(infoHash, filename)
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	return &FileInfo{
		name:        filename,
		size:        info.Size(),
		modTime:     info.ModTime(),
		isDir:       false,
		infohash:    infoHash,
		sidecarPath: p,
	}
}

// deleteSidecars removes the entire sidecar directory for an InfoHash (best-effort).
func (m *Manager) deleteSidecars(infoHash string) {
	dir := sidecarDirForHash(infoHash)
	if err := os.RemoveAll(dir); err != nil {
		m.logger.Warn().Err(err).Str("infohash", infoHash).Msg("Failed to remove sidecar dir")
	}
	sidecarCacheMu.Lock()
	delete(sidecarCache, infoHash)
	sidecarCacheMu.Unlock()
}
