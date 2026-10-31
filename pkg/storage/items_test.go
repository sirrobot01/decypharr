package storage

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sirrobot01/appendstore"
	"github.com/sirrobot01/decypharr/internal/config"
)

func TestEntryMutationsReportIndexFailures(t *testing.T) {
	for _, operation := range []string{"add", "delete", "update item"} {
		for _, failure := range []string{"corrupt", "closed"} {
			t.Run(operation+"/"+failure, func(t *testing.T) {
				s, err := NewStorage(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = s.Close() })
				entry := &Entry{InfoHash: "hash", Name: "folder", Files: map[string]*File{"file": {Name: "file", InfoHash: "hash", Size: 10}}}
				if err := s.AddOrUpdate(entry); err != nil {
					t.Fatal(err)
				}
				if failure == "corrupt" {
					if err := s.entryItems.Put("folder", []byte{0xff}, nil); err != nil {
						t.Fatal(err)
					}
				} else if err := s.entryItems.Close(); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "add":
					err = s.AddOrUpdate(entry)
				case "delete":
					err = s.Delete(entry.InfoHash)
				case "update item":
					err = s.UpdateEntryItem(entry)
				}
				if err == nil || !strings.Contains(err.Error(), "folder") {
					t.Fatalf("error = %v, want folder context", err)
				}
				if failure == "closed" && !errors.Is(err, appendstore.ErrStoreClosed) {
					t.Fatalf("error = %v, want ErrStoreClosed", err)
				}
				if _, err := s.Get(entry.InfoHash); err != nil {
					t.Fatalf("source entry was lost: %v", err)
				}
				if failure == "corrupt" {
					data, err := s.entryItems.Get("folder")
					if err != nil || !bytes.Equal(data, []byte{0xff}) {
						t.Fatalf("corrupt index was overwritten: %x, %v", data, err)
					}
				}
			})
		}
	}
}

func TestEntryWriteFailureLeavesIndexUnchanged(t *testing.T) {
	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	entry := &Entry{InfoHash: "hash", Name: "folder", Files: map[string]*File{"file": {Name: "file", InfoHash: "hash", Size: 10}}}
	if err := s.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	before, err := s.entryItems.Get("folder")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.entries.Close(); err != nil {
		t.Fatal(err)
	}
	entry.Files["file"].Size = 20
	if err := s.AddOrUpdate(entry); !errors.Is(err, appendstore.ErrStoreClosed) {
		t.Fatalf("error = %v, want ErrStoreClosed", err)
	}
	after, err := s.entryItems.Get("folder")
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("index changed after failed source write: %v", err)
	}
}

func TestEntryMutationReportsHealthFailure(t *testing.T) {
	for _, operation := range []string{"add", "delete"} {
		t.Run(operation, func(t *testing.T) {
			s, err := NewStorage(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			entry := &Entry{InfoHash: "hash", Name: "folder", Files: map[string]*File{"file": {Name: "file", InfoHash: "hash", Size: 10}}}
			if err := s.AddOrUpdate(entry); err != nil {
				t.Fatal(err)
			}
			if err := s.repairState.Close(); err != nil {
				t.Fatal(err)
			}
			if operation == "add" {
				entry.Files["file"].Size = 20
				err = s.AddOrUpdate(entry)
			} else {
				err = s.Delete(entry.InfoHash)
			}
			if !errors.Is(err, appendstore.ErrStoreClosed) {
				t.Fatalf("error = %v, want ErrStoreClosed", err)
			}
			item, err := s.GetEntryItem("folder")
			if err != nil || item.Files["file"].Size != 10 {
				t.Fatalf("index changed after health failure: %v, %v", item, err)
			}
		})
	}
}

func TestDeleteKeepsFolderSharedWithAnotherEntry(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	for _, hash := range []string{"first", "second"} {
		entry := &Entry{InfoHash: hash, Name: "folder", Files: map[string]*File{"file": {Name: "file", InfoHash: hash, Size: 10}}}
		if err := s.AddOrUpdate(entry); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Delete("first"); err != nil {
		t.Fatal(err)
	}
	item, err := s.GetEntryItem("folder")
	if err != nil || item.Files["file"] == nil {
		t.Fatalf("folder lost the remaining entry's file: %v, %v", item, err)
	}
}

func TestIterationSkipsInternalRecords(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.SaveMigrationStatus(&SystemMigrationStatus{}); err != nil {
		t.Fatal(err)
	}
	entry := &Entry{InfoHash: "abc", Name: "folder", Protocol: config.ProtocolNZB, Files: map[string]*File{"file": {Name: "file", InfoHash: "abc", Size: 10}}}
	if err := s.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}

	var metaNames []string
	if err := s.ForEachMeta(func(m *EntryMetaInfo) error {
		metaNames = append(metaNames, m.Name)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(metaNames) != 1 || metaNames[0] != "folder" {
		t.Errorf("ForEachMeta returned %q, want only the real entry", metaNames)
	}

	listed, err := s.List(nil)
	if err != nil || len(listed) != 1 || listed[0].InfoHash != "abc" {
		t.Errorf("List returned %d entries (err %v), want only the real entry", len(listed), err)
	}

	each := 0
	if err := s.ForEach(func(*Entry) error { each++; return nil }); err != nil || each != 1 {
		t.Errorf("ForEach visited %d entries (err %v), want 1", each, err)
	}

	batched := 0
	if err := s.ForEachBatch(10, func(b []*Entry) error { batched += len(b); return nil }); err != nil || batched != 1 {
		t.Errorf("ForEachBatch visited %d entries (err %v), want 1", batched, err)
	}

	if n, err := s.Count(); err != nil || n != 1 {
		t.Errorf("Count = %d (err %v), want 1", n, err)
	}
}
