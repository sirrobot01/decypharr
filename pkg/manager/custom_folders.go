package manager

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager/virtualfolders"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type VirtualFolderPreviewItem struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Size     int64  `json:"size"`
}

func (m *Manager) initVirtualFolders() {
	compiled, err := virtualfolders.Compile(m.config.VirtualFolders)
	if err != nil {
		m.logger.Error().Err(err).Msg("Ignoring invalid virtual folders")
	}
	m.virtualFoldersMu.Lock()
	m.virtualFolders = compiled
	m.virtualFoldersMu.Unlock()
}

// ApplyVirtualFolders installs valid definitions and clears directory caches.
// It refreshes the mount root to show changes without a service restart.
func (m *Manager) ApplyVirtualFolders(definitions []config.VirtualFolder) error {
	compiled, err := virtualfolders.Compile(definitions)
	if err != nil {
		return err
	}

	m.virtualFoldersMu.Lock()
	m.virtualFolders = compiled
	m.virtualFoldersMu.Unlock()

	if m.entry != nil {
		m.entry.InvalidateAll()
	}
	if m.mountManager != nil {
		go func() {
			if err := m.mountManager.Refresh([]string{""}); err != nil {
				m.logger.Warn().Err(err).Msg("Failed to refresh mount after virtual-folder update")
			}
		}()
	}
	return nil
}

func (m *Manager) virtualFoldersSnapshot() *virtualfolders.Folders {
	m.virtualFoldersMu.RLock()
	defer m.virtualFoldersMu.RUnlock()
	return m.virtualFolders
}

func (m *Manager) GetVirtualFolders() []string {
	virtualFolders := m.virtualFoldersSnapshot()
	if virtualFolders == nil {
		return nil
	}
	return virtualFolders.Names()
}

func (m *Manager) virtualFolderFileNames(meta *storage.EntryMetaInfo) func() []string {
	var loaded bool
	var names []string
	return func() []string {
		if loaded {
			return names
		}
		loaded = true
		item, err := m.storage.Get(meta.InfoHash)
		if err != nil || item == nil {
			return nil
		}
		names = make([]string, 0, len(item.Files))
		for name := range item.Files {
			names = append(names, name)
		}
		return names
	}
}

func (m *Manager) PreviewVirtualFolder(definition config.VirtualFolder, limit int) (int, []VirtualFolderPreviewItem, error) {
	compiled, err := virtualfolders.Compile([]config.VirtualFolder{definition})
	if err != nil {
		return 0, nil, err
	}
	if limit < 1 || limit > 20 {
		limit = 5
	}

	total := 0
	samples := make([]VirtualFolderPreviewItem, 0, limit)
	seen := make(map[string]struct{})
	err = m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		if !compiled.Matches(definition.Name, meta, m.virtualFolderFileNames(meta)) {
			return nil
		}
		if _, ok := seen[meta.Name]; ok {
			return nil
		}
		seen[meta.Name] = struct{}{}
		total++
		if len(samples) < limit {
			samples = append(samples, VirtualFolderPreviewItem{
				Name: meta.Name, Provider: meta.Provider, Protocol: meta.Protocol, Size: meta.Size,
			})
		}
		return nil
	})
	return total, samples, err
}
