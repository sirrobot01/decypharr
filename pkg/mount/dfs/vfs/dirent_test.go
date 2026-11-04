package vfs

import (
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestIsValidDirentName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "Example.Release", want: true},
		{name: ".hidden", want: true},
		{name: "..dots", want: true},
		{name: "Tëst ✓", want: true},
		{name: strings.Repeat("a", maxDirentNameLen), want: true},
		{name: strings.Repeat("a", maxDirentNameLen+1), want: false},
		{name: "", want: false},
		{name: ".", want: false},
		{name: "..", want: false},
		{name: "bad/name", want: false},
		{name: "bad\x00name", want: false},
	}
	for _, tt := range tests {
		if got := isValidDirentName(tt.name); got != tt.want {
			t.Errorf("isValidDirentName(%.24q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestListableChildrenFiltersWithoutTouchingInput(t *testing.T) {
	config.Reset()
	t.Cleanup(config.Reset)
	mgr := manager.New()
	t.Cleanup(func() { _ = mgr.Stop() })
	for hash, name := range map[string]string{
		"aaa": "Alpha",
		"bbb": strings.Repeat("L", maxDirentNameLen+1),
		"ccc": "With/Slash",
		"ddd": "Delta",
	} {
		entry := &storage.Entry{
			InfoHash: hash, Name: name, Protocol: config.ProtocolNZB,
			Files: map[string]*storage.File{"file": {Name: "file", InfoHash: hash, Size: 1}},
		}
		if err := mgr.Storage().AddOrUpdate(entry); err != nil {
			t.Fatal(err)
		}
	}
	_, children := mgr.GetEntryChildren(manager.EntryAllFolder)
	if len(children) != 4 {
		t.Fatalf("manager returned %d children, want 4: the UI and WebDAV need them all", len(children))
	}
	before := names(children)

	m := &Manager{rl: logger.NewRateLimitedLogger()}
	listed := names(m.ListableChildren("/__all__", children))
	if len(listed) != 2 || !listed["Alpha"] || !listed["Delta"] {
		t.Fatalf("ListableChildren kept %v, want Alpha and Delta", listed)
	}
	after := names(children)
	if len(after) != len(before) {
		t.Fatalf("input slice was modified: %v", after)
	}
	for name := range before {
		if !after[name] {
			t.Fatalf("input slice lost %.24q; it is shared with the entry cache", name)
		}
	}
}

func TestListableChildrenReturnsCleanListingUnchanged(t *testing.T) {
	m := &Manager{rl: logger.NewRateLimitedLogger()}
	if got := m.ListableChildren("/empty", nil); got != nil {
		t.Fatalf("ListableChildren(nil) = %v, want nil", got)
	}
}

func names(children []manager.FileInfo) map[string]bool {
	out := make(map[string]bool, len(children))
	for i := range children {
		out[children[i].Name()] = true
	}
	return out
}
