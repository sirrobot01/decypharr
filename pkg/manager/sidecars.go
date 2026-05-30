package manager

import (
	"os"
	"path/filepath"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
)

func sidecarDir() string {
	return filepath.Join(config.GetMainPath(), "sidecars")
}

func sidecarFilePath(torrentName, filename string) string {
	return filepath.Join(sidecarDir(), torrentName, filename)
}

// InjectSidecarFile writes a sidecar file (e.g. subtitle) to disk.
func (m *Manager) InjectSidecarFile(torrentName, filename string, content []byte) error {
	dir := filepath.Join(sidecarDir(), torrentName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, filename), content, 0644)
}

// GetSidecars returns FileInfo entries for all sidecar files in a torrent folder.
func (m *Manager) GetSidecars(torrentName string) []FileInfo {
	dir := filepath.Join(sidecarDir(), torrentName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	infos := make([]FileInfo, 0, len(entries))
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
			parent:      torrentName,
			sidecarPath: p,
		})
	}
	return infos
}

// GetSidecarFile returns a FileInfo for a specific sidecar file, or nil if not found.
func (m *Manager) GetSidecarFile(torrentName, filename string) *FileInfo {
	p := sidecarFilePath(torrentName, filename)
	info, err := os.Stat(p)
	if err != nil {
		return nil
	}
	return &FileInfo{
		name:        filename,
		size:        info.Size(),
		modTime:     info.ModTime(),
		isDir:       false,
		parent:      torrentName,
		sidecarPath: p,
	}
}

// loadSidecars is a no-op in disk mode — files are read directly from disk on demand.
func (m *Manager) loadSidecars() {}

// newSidecarRegistry is a no-op in disk mode.
func newSidecarRegistry() *sidecarRegistry { return nil }

type sidecarRegistry struct{}

// Stub to satisfy init() call in manager.go
func (m *Manager) initSidecars() {}

// sidecarFile stub kept for compatibility
type sidecarFile struct {
	content []byte
	modTime time.Time
}
