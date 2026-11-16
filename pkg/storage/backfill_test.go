package storage

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

// Entries saved without AddedOn get their stored time, and so do their files
// in the name index, which the mounts read the file mtime from.
func TestBackfillAddedOn(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	infohash := "aabbccddeeff00112233445566778899aabbccdd"
	entry := &Entry{
		InfoHash: infohash,
		Name:     "Show.S01E01",
		Protocol: config.ProtocolNZB,
		Files: map[string]*File{
			"Show.S01E01.mkv": {Name: "Show.S01E01.mkv", Size: 100, InfoHash: infohash},
		},
	}
	if err := s.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}

	changed, err := s.BackfillAddedOn()
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("changed = %d, want 1", changed)
	}

	got, err := s.Get(infohash)
	if err != nil {
		t.Fatal(err)
	}
	if !got.AddedOn.Equal(got.UpdatedAt) {
		t.Fatalf("AddedOn = %v, want UpdatedAt %v", got.AddedOn, got.UpdatedAt)
	}
	item, err := s.GetEntryItem("Show.S01E01")
	if err != nil {
		t.Fatal(err)
	}
	if f := item.Files["Show.S01E01.mkv"]; !f.AddedOn.Equal(got.AddedOn) {
		t.Fatalf("name index file AddedOn = %v, want %v", f.AddedOn, got.AddedOn)
	}
	_ = s.ForEachMeta(func(meta *EntryMetaInfo) error {
		if !meta.AddedOn.Equal(got.AddedOn) {
			t.Fatalf("index AddedOn = %v, want %v", meta.AddedOn, got.AddedOn)
		}
		return nil
	})

	if changed, _ := s.BackfillAddedOn(); changed != 0 {
		t.Fatalf("second run changed %d entries, want 0", changed)
	}
}
