package manager

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager/virtualfolders"
)

func TestApplyVirtualFoldersKeepsOldDefinitionsOnError(t *testing.T) {
	t.Parallel()
	m := &Manager{virtualFolders: mustCompileVirtualFolders(t, config.VirtualFolder{Name: "Old"})}
	err := m.ApplyVirtualFolders([]config.VirtualFolder{{
		Name: "Broken", Conditions: []config.VirtualFolderCondition{{
			Field: config.VirtualFolderFieldEntryName, Operator: config.VirtualFolderOperatorMatchesRegex, Value: "[",
		}},
	}})
	if err == nil {
		t.Fatal("invalid regular expression was accepted")
	}
	folders := m.GetVirtualFolders()
	if len(folders) != 1 || folders[0] != "Old" {
		t.Fatalf("failed update replaced live definitions: %v", folders)
	}
}

func TestApplyVirtualFoldersReplacesDefinitionsAndClearsCache(t *testing.T) {
	t.Parallel()
	m := &Manager{}
	m.entry = NewEntryCache(m)
	m.entry.entries.Store("Old", EntryCacheItem{current: &FileInfo{name: "Old"}})
	m.virtualFolders = mustCompileVirtualFolders(t, config.VirtualFolder{Name: "Old"})

	if err := m.ApplyVirtualFolders([]config.VirtualFolder{{Name: "New"}}); err != nil {
		t.Fatalf("ApplyVirtualFolders() error = %v", err)
	}
	if _, ok := m.entry.entries.Load("Old"); ok {
		t.Fatal("old virtual-folder cache entry survived live update")
	}
	folders := m.GetVirtualFolders()
	if len(folders) != 1 || folders[0] != "New" {
		t.Fatalf("live definitions = %v, want [New]", folders)
	}
}

func mustCompileVirtualFolders(t *testing.T, definitions ...config.VirtualFolder) *virtualfolders.Folders {
	t.Helper()
	compiled, err := virtualfolders.Compile(definitions)
	if err != nil {
		t.Fatalf("virtualfolders.Compile() error = %v", err)
	}
	return compiled
}
