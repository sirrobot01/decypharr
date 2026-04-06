package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/version"
)

const (
	EntryAllFolder     string = "__all__"
	EntryBadFolder     string = "__bad__"
	EntryTorrentFolder string = "torrents"
	EntryNZBFolder     string = "nzbs"
)

// FileInfo implements os.FileInfo
type FileInfo struct {
	name         string
	size         int64
	mode         os.FileMode
	modTime      time.Time
	isDir        bool
	content      []byte
	parent       string
	activeDebrid string
	canDelete    bool
	byteRange    *[2]int64
	infohash     string
	sys          interface{} // For caching fuse nodes
}

func (f *FileInfo) Name() string         { return f.name }
func (f *FileInfo) Size() int64          { return f.size }
func (f *FileInfo) Mode() os.FileMode    { return f.mode }
func (f *FileInfo) ModTime() time.Time   { return f.modTime }
func (f *FileInfo) IsDir() bool          { return f.isDir }
func (f *FileInfo) Sys() interface{}     { return f.sys }
func (f *FileInfo) SetSys(v interface{}) { f.sys = v }
func (f *FileInfo) Content() []byte      { return f.content }
func (f *FileInfo) Parent() string       { return f.parent }
func (f *FileInfo) ActiveDebrid() string { return f.activeDebrid }
func (f *FileInfo) CanDelete() bool      { return f.canDelete }
func (f *FileInfo) IsRemote() bool       { return len(f.content) == 0 }
func (f *FileInfo) ByteRange() *[2]int64 { return f.byteRange }
func (f *FileInfo) InfoHash() string     { return f.infohash }

// GetTorrentMountPath returns the full mount path for a torrent
// Returns the path based on the new unified mount structure
func (m *Manager) GetTorrentMountPath(torrent *storage.Entry) string {
	return filepath.Join(m.config.Mount.MountPath, EntryAllFolder, torrent.GetFolder())
}

func (m *Manager) setMountPaths() {
	m.rootInfo = &FileInfo{
		name:    "",
		size:    0,
		modTime: utils.Now(),
		isDir:   true,
	}
}

func (m *Manager) RootInfo() *FileInfo {
	if m.rootInfo == nil {
		m.rootInfo = &FileInfo{
			name:    "",
			size:    0,
			modTime: utils.Now(),
			isDir:   true,
		}
	}
	return m.rootInfo
}

// GetEntries returns the subdirectories under a given mount name
// it would show __all__, __bad__, torrents, nzbs and any custom folders
func (m *Manager) GetEntries() []FileInfo {
	now := utils.Now()
	var subDirs []FileInfo
	extras := []string{EntryAllFolder, EntryBadFolder, EntryTorrentFolder, EntryNZBFolder}
	for _, dir := range extras {
		subDirs = append(subDirs, FileInfo{
			name:    dir,
			isDir:   true,
			modTime: now,
			size:    0,
		})
	}
	// AddOrUpdate custom folders
	if m.customFolders != nil {
		for _, folderName := range m.customFolders.folders {
			subDirs = append(subDirs, FileInfo{
				name:    folderName,
				isDir:   true,
				modTime: now,
				size:    0,
			})
		}
	}

	// AddOrUpdate version.txt
	subDirs = append(subDirs, FileInfo{
		name:    "version.txt",
		isDir:   false,
		modTime: now,
		size:    int64(len(version.GetInfo().String())),
		content: []byte(version.GetInfo().Version),
	})
	return subDirs
}

func (m *Manager) GetEntryChildren(group string) (*FileInfo, []FileInfo) {
	return m.entry.Get(group)
}

func (m *Manager) GetTorrentChildren(name string) (*FileInfo, []FileInfo) {
	return m.entry.Get(torrentEntryCachePrefix + name)
}

func (m *Manager) GetTorrentEntry(torrentName string) (*FileInfo, error) {
	current, _ := m.GetTorrentChildren(torrentName)
	if current == nil {
		return nil, fmt.Errorf("torrent %s not found", torrentName)
	}
	return current, nil
}

// GetEntryInfo returns a FileInfo for a torrent/entry by name - O(1) lookup
func (m *Manager) GetEntryInfo(name string) (*FileInfo, error) {
	entry, err := m.storage.GetEntryItem(name)
	if err != nil {
		return nil, fmt.Errorf("entry %s not found", name)
	}

	// get metadata from first file (all files in an entry share the same parent entry)
	var modTime time.Time
	var infohash string
	for _, f := range entry.Files {
		modTime = f.AddedOn
		infohash = f.InfoHash
		break
	}

	return &FileInfo{
		infohash:  infohash,
		name:      entry.Name,
		size:      entry.Size,
		modTime:   modTime,
		isDir:     true,
		canDelete: true,
	}, nil
}

func (m *Manager) GetTorrentFile(torrentName, fileName string) (*FileInfo, error) {
	entry, err := m.storage.GetEntryItem(torrentName)
	if err != nil {
		return nil, fmt.Errorf("torrent %s not found", torrentName)
	}
	file, err := entry.GetFile(fileName)
	if err != nil {
		return nil, fmt.Errorf("file %s not found in torrent %s", fileName, torrentName)
	}
	return &FileInfo{
		infohash:  file.InfoHash,
		name:      file.Name,
		size:      file.Size,
		modTime:   file.AddedOn,
		isDir:     false,
		parent:    entry.Name,
		canDelete: true,
		byteRange: file.ByteRange,
	}, nil
}

// getEntryChildren
// Groups are __all__, __bad__, custom folders
// Uses metadata-only iteration (no disk reads, no protobuf deserialization)
func (m *Manager) getEntryChildren(group string) (*FileInfo, []FileInfo) {
	currentDir := &FileInfo{
		name:    group,
		size:    0,
		modTime: utils.Now(),
		isDir:   true,
	}
	switch group {
	case EntryAllFolder:
		// This returns all entries - using metadata-only iteration (no disk reads)
		var infos []FileInfo
		seen := make(map[string]struct{})
		err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
			if _, ok := seen[meta.Name]; ok {
				return nil
			}
			seen[meta.Name] = struct{}{}
			infos = append(infos, FileInfo{
				infohash:     meta.InfoHash,
				name:         meta.Name,
				size:         meta.Size,
				modTime:      meta.AddedOn,
				isDir:        true,
				activeDebrid: meta.Provider,
				canDelete:    true,
			})
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryTorrentFolder:
		// This returns all torrents - using metadata-only iteration
		var infos []FileInfo
		seen := make(map[string]struct{})
		err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
			if meta.Protocol == "torrent" {
				if _, ok := seen[meta.Name]; ok {
					return nil
				}
				seen[meta.Name] = struct{}{}
				infos = append(infos, FileInfo{
					infohash:     meta.InfoHash,
					name:         meta.Name,
					size:         meta.Size,
					modTime:      meta.AddedOn,
					isDir:        true,
					activeDebrid: meta.Provider,
					canDelete:    true,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryNZBFolder:
		// This returns all nzbs - using metadata-only iteration
		var infos []FileInfo
		seen := make(map[string]struct{})
		err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
			if meta.Protocol == "nzb" {
				if _, ok := seen[meta.Name]; ok {
					return nil
				}
				seen[meta.Name] = struct{}{}
				infos = append(infos, FileInfo{
					infohash:     meta.InfoHash,
					name:         meta.Name,
					size:         meta.Size,
					modTime:      meta.AddedOn,
					isDir:        true,
					activeDebrid: meta.Provider,
					canDelete:    true,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryBadFolder:
		// Filter for bad entries - using metadata-only iteration
		var infos []FileInfo
		seen := make(map[string]struct{})
		err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
			if meta.Bad {
				if _, ok := seen[meta.Name]; ok {
					return nil
				}
				seen[meta.Name] = struct{}{}
				infos = append(infos, FileInfo{
					infohash:     meta.InfoHash,
					name:         meta.Name,
					size:         meta.Size,
					modTime:      meta.AddedOn,
					isDir:        true,
					activeDebrid: meta.Provider,
					canDelete:    true,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case "version.txt":
		currentDir.content = []byte(version.GetInfo().Version)
		currentDir.size = int64(len(currentDir.content))
		currentDir.isDir = false
		return currentDir, nil
	default:
		// Custom folder
		return currentDir, m.getCustomFolderChildren(group)
	}
}

func (m *Manager) getTorrentChildren(name string) (*FileInfo, []FileInfo) {
	// Find the torrent by folder name
	entry, err := m.storage.GetEntryItem(name)
	if err != nil || entry == nil {
		return nil, nil
	}

	// Convert files to FileInfo
	infos := make([]FileInfo, 0, len(entry.Files))
	size := int64(0)
	for _, file := range entry.Files {
		infos = append(infos, FileInfo{
			name:      file.Name,
			size:      file.Size,
			modTime:   file.AddedOn,
			isDir:     false,
			parent:    entry.Name,
			canDelete: true,
			byteRange: file.ByteRange,
		})
		size += file.Size
	}
	if len(infos) == 0 {
		return nil, nil
	}

	currentDir := &FileInfo{
		name:    entry.Name,
		size:    size,
		modTime: infos[0].modTime,
		isDir:   true,
	}
	return currentDir, infos
}

func (m *Manager) RemoveEntry(entry *FileInfo) error {
	if entry == nil {
		return fmt.Errorf("entry is nil")
	}
	if !entry.CanDelete() {
		return fmt.Errorf("entry %s cannot be deleted", entry.name)
	}

	if entry.isDir {
		// This is a torrent folder
		m.logger.Debug().Str("entry", entry.name).Msg("Removing entry folder")
		infohash := entry.infohash
		if infohash == "" {
			// Fallback: look up from storage
			et, err := m.storage.GetEntryItem(entry.name)
			if err != nil {
				return fmt.Errorf("torrent %s not found", entry.name)
			}
			if len(et.Files) == 0 {
				return fmt.Errorf("torrent %s has no files", entry.name)
			}
			firstFile, err := et.GetFirstFile()
			if err != nil {
				return fmt.Errorf("failed to get first file of torrent %s: %w", entry.name, err)
			}
			infohash = firstFile.InfoHash
		}
		return m.DeleteEntry(infohash, true)
	}
	// This is a file within a torrent
	return m.RemoveTorrentFile(entry.Parent(), entry.Name())
}

func (m *Manager) CopyEntry(entry *FileInfo, destPath string, delete bool) error {
	if entry == nil {
		return fmt.Errorf("entry is nil")
	}
	if !entry.CanDelete() {
		return fmt.Errorf("entry %s cannot be copied", entry.name)
	}
	//if entry.isDir {
	//	// This is a torrent folder
	//	m.logger.Debug().Str("torrent", entry.name).Msg("Copying torrent folder")
	//	torr, err := m.GetTorrentByName(entry.name)
	//	if err != nil {
	//		return fmt.Errorf("torrent %s not found", entry.name)
	//	}
	//	// Create a copy of the torrent, with the new destination
	//	// To do this, we need to create a new torrent with the same files, but with the new folder name
	//	newTorrent := *torr
	//	newTorrent.Folder = filepath.Base(destPath)
	//	// Set a new infohash to avoid conflicts
	//	newTorrent.InfoHash = utils.GenerateInfoHash()
	//	err = m.AddOrUpdate(&newTorrent, func(t *storage.Entry) {
	//		m.RefreshEntries(true)
	//	})
	//	if delete {
	//		// Delete the original torrent
	//		err = m.DeleteEntry(torr.InfoHash, false) // do not delete from debrid
	//	}
	//	return err
	//}
	//// This is a file within a torrent
	//
	//torr, err := m.GetTorrentByName(entry.Parent())
	//if err != nil {
	//	return fmt.Errorf("torrent %s not found", entry.Parent())
	//}
	//file, err := torr.GetFile(entry.Name())
	//if err != nil {
	//	return fmt.Errorf("file %s not found in torrent %s", entry.Name(), entry.Parent())
	//}
	//// Create a copy of the file, with the new name
	//newFile := *file
	//newFile.Name = filepath.Base(destPath)
	//// Add the new file to the torrent
	//torr.Files[newFile.Name] = &newFile
	//err = m.AddOrUpdate(torr, func(t *storage.Entry) {
	//	m.RefreshEntries(true)
	//})
	//if delete {
	//	// Remove the original file
	//	err = m.RemoveTorrentFile(torr.Folder, file.Name)
	//}
	return fmt.Errorf("copying entries is not supported yet")
}

func (m *Manager) RemoveTorrentFile(torrentName, filename string) error {
	item, err := m.storage.GetEntryItem(torrentName)
	if err != nil {
		return fmt.Errorf("entry %s not found", torrentName)
	}
	file, err := item.GetFile(filename)
	if err != nil {
		return fmt.Errorf("file %s not found in entry %s", filename, torrentName)
	}
	file.Deleted = true
	item.Files[filename] = file

	// Update item in storage
	if err := m.storage.UpdateItem(item); err != nil {
		return fmt.Errorf("failed to update entry %s: %w", torrentName, err)
	}

	// If the torrent has no more files, delete the entire entry
	hasFiles := false
	for _, f := range item.Files {
		if !f.Deleted {
			hasFiles = true
			break
		}
	}
	if !hasFiles {
		m.logger.Debug().Str("entry", torrentName).Msg("Removing entry folder as it has no more files")
		return m.DeleteEntry(file.InfoHash, true)
	}
	return nil
}

func (m *Manager) getCustomFolderChildren(folder string) []FileInfo {
	filters := m.customFolders.filters[folder]
	if len(filters) == 0 {
		return nil
	}

	// Use metadata-only iteration (no disk reads)
	var infos []FileInfo
	seen := make(map[string]struct{})
	err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		if meta.Bad {
			return nil
		}
		getFileNames := func() []string {
			item, err := m.storage.GetEntryItem(meta.Name)
			if err != nil || item == nil {
				return nil
			}
			names := make([]string, 0, len(item.Files))
			for fn := range item.Files {
				names = append(names, strings.ToLower(fn))
			}
			return names
		}
		if m.customFolders.matchesFilter(folder, &FileInfo{
			name: meta.Name,
			size: meta.Size,
		}, meta.AddedOn, getFileNames) {
			if _, ok := seen[meta.Name]; ok {
				return nil
			}
			seen[meta.Name] = struct{}{}
			infos = append(infos, FileInfo{
				infohash:     meta.InfoHash,
				name:         meta.Name,
				size:         meta.Size,
				modTime:      meta.AddedOn,
				isDir:        true,
				activeDebrid: meta.Provider,
				canDelete:    true,
			})
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return infos
}
