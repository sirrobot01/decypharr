package storage

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

// The migration status shares the entries store. It must not list as an
// entry: an entry with an empty name makes the kernel reject the readdir.
func TestEntryIterationSkipsInternalKeys(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SaveMigrationStatus(&SystemMigrationStatus{}); err != nil {
		t.Fatal(err)
	}
	infohash := "aabbccddeeff00112233445566778899aabbccdd"
	if err := s.AddOrUpdate(&Entry{InfoHash: infohash, Name: "Movie", Protocol: config.ProtocolTorrent}); err != nil {
		t.Fatal(err)
	}

	var metas []string
	if err := s.ForEachMeta(func(m *EntryMetaInfo) error {
		metas = append(metas, m.InfoHash)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var each []string
	if err := s.ForEach(func(e *Entry) error {
		each = append(each, e.InfoHash)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var batched []string
	if err := s.ForEachBatch(10, func(batch []*Entry) error {
		for _, e := range batch {
			batched = append(batched, e.InfoHash)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	listed, err := s.List(nil)
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]int{"ForEachMeta": len(metas), "ForEach": len(each), "ForEachBatch": len(batched), "List": len(listed)} {
		if got != 1 {
			t.Errorf("%s returned %d entries, want 1 (metas %q)", name, got, metas)
		}
	}
	if _, err := s.GetMigrationStatus(); err != nil {
		t.Fatalf("GetMigrationStatus() = %v, want the record kept", err)
	}
}
