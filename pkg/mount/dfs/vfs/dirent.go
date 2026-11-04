package vfs

import (
	"strings"

	"github.com/sirrobot01/decypharr/pkg/manager"
)

// maxDirentNameLen is the longest name every kernel accepts in a FUSE directory
// entry. The limit is 1024 bytes on older kernels and 4095 on current ones.
// Names beyond the 255 bytes most filesystems allow still list and open fine.
const maxDirentNameLen = 1024

// isValidDirentName reports whether name can appear in a directory listing.
func isValidDirentName(name string) bool {
	if name == "" || name == "." || name == ".." || len(name) > maxDirentNameLen {
		return false
	}
	return !strings.ContainsAny(name, "/\x00")
}

// ListableChildren returns children without the names a FUSE listing cannot
// carry. The kernel fails the whole readdir with EIO on one such name, which
// would hide every other entry in dir. This filters here rather than in the
// manager because WebDAV and the UI have no such limits and keep showing them.
//
// children may be shared with the manager's entry cache, so it is never modified.
func (m *Manager) ListableChildren(dir string, children []manager.FileInfo) []manager.FileInfo {
	var listable []manager.FileInfo
	for i := range children {
		name := children[i].Name()
		if isValidDirentName(name) {
			if listable != nil {
				listable = append(listable, children[i])
			}
			continue
		}
		m.rl.Rate("invalid-dirent:"+dir+"/"+name).Warn().
			Str("dir", dir).Str("name", name).Int("length", len(name)).
			Msg("Hiding entry from mount listing: name is not valid on a FUSE filesystem")
		if listable == nil {
			listable = make([]manager.FileInfo, i, len(children)-1)
			copy(listable, children[:i])
		}
	}
	if listable == nil {
		return children
	}
	return listable
}
