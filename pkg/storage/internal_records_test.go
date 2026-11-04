package storage

import (
	"errors"
	"testing"

	"github.com/sirrobot01/appendstore"
	"github.com/sirrobot01/decypharr/internal/config"
)

func newStorageWithInternalRecord(t *testing.T) *Storage {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SaveMigrationStatus(&SystemMigrationStatus{Total: 7}); err != nil {
		t.Fatal(err)
	}
	entry := &Entry{
		InfoHash: "abc", Name: "folder", Protocol: config.ProtocolNZB,
		Files: map[string]*File{"file": {Name: "file", InfoHash: "abc", Size: 10}},
	}
	if err := s.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestIterationSkipsInternalRecords(t *testing.T) {
	s := newStorageWithInternalRecord(t)

	t.Run("ForEachMeta", func(t *testing.T) {
		var names []string
		if err := s.ForEachMeta(func(m *EntryMetaInfo) error {
			names = append(names, m.Name)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(names) != 1 || names[0] != "folder" {
			t.Fatalf("names = %q, want only the entry", names)
		}
	})
	t.Run("ForEach", func(t *testing.T) {
		var hashes []string
		if err := s.ForEach(func(e *Entry) error {
			hashes = append(hashes, e.InfoHash)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(hashes) != 1 || hashes[0] != "abc" {
			t.Fatalf("hashes = %q, want only the entry", hashes)
		}
	})
	t.Run("ForEachBatch", func(t *testing.T) {
		var hashes []string
		if err := s.ForEachBatch(10, func(batch []*Entry) error {
			for _, e := range batch {
				hashes = append(hashes, e.InfoHash)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(hashes) != 1 || hashes[0] != "abc" {
			t.Fatalf("hashes = %q, want only the entry", hashes)
		}
	})
	t.Run("List", func(t *testing.T) {
		entries, err := s.List(nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].InfoHash != "abc" {
			t.Fatalf("List returned %d entries, want only the entry", len(entries))
		}
	})
	t.Run("Count", func(t *testing.T) {
		if count, err := s.Count(); err != nil || count != 1 {
			t.Fatalf("Count = %d, %v, want 1", count, err)
		}
	})
}

func TestCountWithoutMigrationStatus(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if count, err := s.Count(); err != nil || count != 0 {
		t.Fatalf("Count = %d, %v, want 0", count, err)
	}
}

// Entry lookups take hashes from API requests, so they must not reach internal
// records: a delete by that key would otherwise wipe the migration status.
func TestEntryLookupsCannotReachInternalRecords(t *testing.T) {
	s := newStorageWithInternalRecord(t)

	if exists, err := s.Exists(migrationStatusKey); err != nil || exists {
		t.Fatalf("Exists = %v, %v, want false", exists, err)
	}
	if entry, err := s.Get(migrationStatusKey); !errors.Is(err, appendstore.ErrKeyNotFound) {
		t.Fatalf("Get = %v, %v, want ErrKeyNotFound", entry, err)
	}
	if err := s.Delete(migrationStatusKey); !errors.Is(err, appendstore.ErrKeyNotFound) {
		t.Fatalf("Delete error = %v, want ErrKeyNotFound", err)
	}
	status, err := s.GetMigrationStatus()
	if err != nil || status.Total != 7 {
		t.Fatalf("migration status = %+v, %v, want it intact", status, err)
	}
}
