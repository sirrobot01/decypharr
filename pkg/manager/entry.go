package manager

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"time"

	debrid "github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/manager/virtualfolders"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/version"
)

const (
	EntryAllFolder     string = "__all__"
	EntryBadFolder     string = "__bad__"
	EntryTorrentFolder string = "torrents"
	EntryNZBFolder     string = "nzbs"

	EntryKindSystem   string = "system"
	EntryKindProvider string = "provider"
	EntryKindVirtual  string = "virtual"
	EntryKindEntry    string = "entry"
	EntryKindFile     string = "file"
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
	kind         string
	sys          any // For caching fuse nodes
}

func (f *FileInfo) Name() string         { return f.name }
func (f *FileInfo) Size() int64          { return f.size }
func (f *FileInfo) Mode() os.FileMode    { return f.mode }
func (f *FileInfo) ModTime() time.Time   { return f.modTime }
func (f *FileInfo) IsDir() bool          { return f.isDir }
func (f *FileInfo) Sys() any             { return f.sys }
func (f *FileInfo) SetSys(v any)         { f.sys = v }
func (f *FileInfo) Content() []byte      { return f.content }
func (f *FileInfo) Parent() string       { return f.parent }
func (f *FileInfo) ActiveDebrid() string { return f.activeDebrid }
func (f *FileInfo) CanDelete() bool      { return f.canDelete }
func (f *FileInfo) IsRemote() bool       { return len(f.content) == 0 }
func (f *FileInfo) ByteRange() *[2]int64 { return f.byteRange }
func (f *FileInfo) InfoHash() string     { return f.infohash }
func (f *FileInfo) Kind() string         { return f.kind }

// GetTorrentMountPath returns the full mount path for a torrent
// Returns the path based on the new unified mount structure
func (m *Manager) GetTorrentMountPath(torrent *storage.Entry) string {
	return filepath.Join(m.config.Mount.MountPath, EntryAllFolder, torrent.GetFolder())
}

func (m *Manager) setMountPaths() {
	m.rootInfo = &FileInfo{
		name:    "",
		size:    0,
		kind:    EntryKindSystem,
		modTime: time.Now(),
		isDir:   true,
	}
}

func (m *Manager) RootInfo() *FileInfo {
	if m.rootInfo == nil {
		m.rootInfo = &FileInfo{
			name:    "",
			size:    0,
			kind:    EntryKindSystem,
			modTime: time.Now(),
			isDir:   true,
		}
	}
	return m.rootInfo
}

// GetEntries returns the subdirectories under a given mount name
// It shows built-in, per-provider, and virtual folders.
func (m *Manager) GetEntries() []FileInfo {
	now := time.Now()
	var subDirs []FileInfo
	extras := []string{EntryAllFolder, EntryBadFolder, EntryTorrentFolder, EntryNZBFolder}
	for _, dir := range extras {
		subDirs = append(subDirs, FileInfo{
			name:    dir,
			isDir:   true,
			modTime: now,
			size:    0,
			kind:    EntryKindSystem,
		})
	}

	// Per-provider folders (one per configured debrid client)
	m.clients.Range(func(name string, _ debrid.Client) bool {
		subDirs = append(subDirs, FileInfo{
			name:    name,
			isDir:   true,
			modTime: now,
			size:    0,
			kind:    EntryKindProvider,
		})
		return true
	})

	// Add virtual folders.
	if virtualFolders := m.virtualFoldersSnapshot(); virtualFolders != nil {
		for _, folderName := range virtualFolders.Names() {
			subDirs = append(subDirs, FileInfo{
				name:    folderName,
				isDir:   true,
				modTime: now,
				size:    0,
				kind:    EntryKindVirtual,
			})
		}
	}

	// AddOrUpdate version.txt — size MUST equal len(content) or FUSE reads
	// will hang/short-read waiting for bytes the backend never produces.
	versionContent := []byte(version.GetInfo().String() + "\n")
	subDirs = append(subDirs, FileInfo{
		name:    "version.txt",
		isDir:   false,
		modTime: now,
		size:    int64(len(versionContent)),
		content: versionContent,
		kind:    EntryKindSystem,
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

	// Prefer metadata from a file that is still listed; deleted files keep
	// their records in the name index. A folder whose files are all deleted
	// stays visible as an empty folder, so rmdir can clear it.
	var modTime time.Time
	var infohash string
	var size int64
	live := false
	for _, f := range entry.Files {
		if !f.Deleted {
			size += f.Size
		}
		if infohash == "" || (!live && !f.Deleted) {
			modTime = f.AddedOn
			infohash = f.InfoHash
			live = !f.Deleted
		}
	}

	return &FileInfo{
		infohash:  infohash,
		name:      entry.Name,
		size:      size,
		modTime:   modTime,
		isDir:     true,
		canDelete: true,
		kind:      EntryKindEntry,
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
		kind:      EntryKindFile,
		byteRange: file.ByteRange,
	}, nil
}

// getEntryChildren
// Groups are built-in, provider, or virtual folders.
// Uses metadata-only iteration (no disk reads, no protobuf deserialization)
func (m *Manager) getEntryChildren(group string) (*FileInfo, []FileInfo) {
	currentDir := &FileInfo{
		name:    group,
		size:    0,
		modTime: time.Now(),
		isDir:   true,
	}
	switch group {
	case EntryAllFolder:
		currentDir.kind = EntryKindSystem
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
				kind:         EntryKindEntry,
			})
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryTorrentFolder:
		currentDir.kind = EntryKindSystem
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
					kind:         EntryKindEntry,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryNZBFolder:
		currentDir.kind = EntryKindSystem
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
					kind:         EntryKindEntry,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case EntryBadFolder:
		currentDir.kind = EntryKindSystem
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
					kind:         EntryKindEntry,
				})
			}
			return nil
		})
		if err != nil {
			return nil, nil
		}
		return currentDir, infos
	case "version.txt":
		currentDir.kind = EntryKindFile
		currentDir.content = []byte(version.GetInfo().String() + "\n")
		currentDir.size = int64(len(currentDir.content))
		currentDir.isDir = false
		return currentDir, nil
	default:
		// Per-provider folder if the name matches a configured client
		if _, ok := m.clients.Load(group); ok {
			currentDir.kind = EntryKindProvider
			var infos []FileInfo
			seen := make(map[string]struct{})
			err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
				if meta.Provider == group {
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
						kind:         EntryKindEntry,
					})
				}
				return nil
			})
			if err != nil {
				return nil, nil
			}
			return currentDir, infos
		}
		virtualFolders := m.virtualFoldersSnapshot()
		if !virtualFolders.Has(group) {
			return nil, nil
		}
		currentDir.kind = EntryKindVirtual
		return currentDir, m.getVirtualFolderChildren(virtualFolders, group)
	}
}

func (m *Manager) getTorrentChildren(name string) (*FileInfo, []FileInfo) {
	// Find the torrent by folder name
	entry, err := m.storage.GetEntryItem(name)
	if err != nil || entry == nil {
		return nil, nil
	}

	// Convert files to FileInfo
	if len(entry.Files) == 0 {
		return nil, nil
	}
	infos := make([]FileInfo, 0, len(entry.Files))
	size := int64(0)
	var modTime time.Time
	for _, file := range entry.Files {
		if file.Deleted {
			// A folder whose files are all deleted lists as empty.
			if len(infos) == 0 {
				modTime = file.AddedOn
			}
			continue
		}
		if len(infos) == 0 {
			modTime = file.AddedOn
		}
		infos = append(infos, FileInfo{
			name:      file.Name,
			size:      file.Size,
			modTime:   file.AddedOn,
			isDir:     false,
			parent:    entry.Name,
			canDelete: true,
			byteRange: file.ByteRange,
			kind:      EntryKindFile,
		})
		size += file.Size
	}

	currentDir := &FileInfo{
		name:      entry.Name,
		size:      size,
		modTime:   modTime,
		isDir:     true,
		canDelete: true,
		kind:      EntryKindEntry,
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
		unlock := m.lockFolder(entry.name)
		defer unlock()
		return m.deleteFolderEntries(entry.name, entry.infohash)
	}
	// This is a file within a torrent
	return m.removeTorrentFile(entry.Parent(), entry.Name(), true)
}

// ErrFolderNotEmpty reports that a folder still has files, so rmdir must fail.
var ErrFolderNotEmpty = errors.New("folder is not empty")

// RemoveEmptyEntry deletes a torrent folder the way rmdir does: it refuses
// while any file in the folder is still listed, and otherwise deletes every
// entry that renders the folder.
func (m *Manager) RemoveEmptyEntry(entry *FileInfo) error {
	if entry == nil {
		return fmt.Errorf("entry is nil")
	}
	if !entry.isDir || !entry.CanDelete() {
		return fmt.Errorf("entry %s cannot be deleted", entry.name)
	}
	unlock := m.lockFolder(entry.name)
	defer unlock()
	if item, err := m.storage.GetEntryItem(entry.name); err == nil {
		for _, f := range item.Files {
			if !f.Deleted {
				return ErrFolderNotEmpty
			}
		}
	}
	return m.deleteFolderEntries(entry.name, entry.infohash)
}

// lockFolder serialises deletes within one torrent folder.
func (m *Manager) lockFolder(name string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(name))
	mu := &m.folderLocks[h.Sum32()%uint32(len(m.folderLocks))]
	mu.Lock()
	return mu.Unlock
}

// deleteFolderEntries deletes every entry that renders the folder, or the
// fallback entry when none is found by name. Deleting only one of them would
// let the name index serve the others' files again.
func (m *Manager) deleteFolderEntries(name, fallback string) error {
	hashes := m.storage.EntryHashesByFolder(name)
	if len(hashes) == 0 && fallback != "" {
		hashes = []string{fallback}
	}
	if len(hashes) == 0 {
		return fmt.Errorf("torrent %s not found", name)
	}
	var firstErr error
	for _, infohash := range hashes {
		if err := m.DeleteEntry(infohash, true); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("delete entry %s of %s: %w", infohash, name, err)
		}
	}
	return firstErr
}

func (m *Manager) CopyEntry(entry *FileInfo, destPath string, delete bool) error {
	if entry == nil {
		return fmt.Errorf("entry is nil")
	}
	if !entry.CanDelete() {
		return fmt.Errorf("entry %s cannot be copied", entry.name)
	}
	return fmt.Errorf("copying entries is not supported yet")
}

// RemoveTorrentFile removes one file, as repair does for a broken file. When it
// was the last file of its entry, only that entry is deleted, so another entry
// with the same folder can keep serving its copy.
func (m *Manager) RemoveTorrentFile(torrentName, filename string) error {
	return m.removeTorrentFile(torrentName, filename, false)
}

// removeTorrentFile flags a file as deleted. Once no file in the folder is
// left, it deletes the file's entry, or with wholeFolder every entry that
// renders the folder.
func (m *Manager) removeTorrentFile(torrentName, filename string, wholeFolder bool) error {
	unlock := m.lockFolder(torrentName)
	defer unlock()

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
		if wholeFolder {
			return m.deleteFolderEntries(torrentName, file.InfoHash)
		}
		return m.DeleteEntry(file.InfoHash, true)
	}
	m.entry.InvalidateTorrent(torrentName)
	return nil
}

func (m *Manager) getVirtualFolderChildren(virtualFolders *virtualfolders.Folders, folder string) []FileInfo {
	// Use metadata-only iteration (no disk reads)
	var infos []FileInfo
	seen := make(map[string]struct{})
	err := m.storage.ForEachMeta(func(meta *storage.EntryMetaInfo) error {
		if virtualFolders.Matches(folder, meta, m.virtualFolderFileNames(meta)) {
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
				kind:         EntryKindEntry,
			})
		}
		return nil
	})
	if err != nil {
		return nil
	}
	return infos
}
