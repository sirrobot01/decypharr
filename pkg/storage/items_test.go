package storage

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

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

// entryWithFile builds an entry that renders folder name with a single file.
func entryWithFile(infoHash, name, fileName string, addedOn time.Time) *Entry {
	return &Entry{
		InfoHash: infoHash,
		Name:     name,
		Protocol: config.ProtocolTorrent,
		AddedOn:  addedOn,
		Files: map[string]*File{
			fileName: {Name: fileName, Size: 10, InfoHash: infoHash, AddedOn: addedOn},
		},
	}
}

// Two entries can render the same folder: a grab keys the entry by the magnet
// infohash, while a later sync of the same cloud transfer keys it by whatever
// the provider reports (Premiumize has no infohash in transfer/list, so sync
// derives a synthetic key). Deleting one of them must leave the folder served
// by the other, not empty it.
func TestDeleteKeepsFolderServedByRemainingEntry(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	synced := time.Now().Add(-time.Hour)
	grabbed := time.Now()

	if err := s.AddOrUpdate(entryWithFile("synthetic", "Show.S01", "a.mkv", synced)); err != nil {
		t.Fatal(err)
	}
	// The grab is newer, so the name index now points at the grab's records.
	if err := s.AddOrUpdate(entryWithFile("realhash", "Show.S01", "a.mkv", grabbed)); err != nil {
		t.Fatal(err)
	}

	// The next sync no longer sees the grab's key and drops that entry.
	if err := s.Delete("realhash"); err != nil {
		t.Fatal(err)
	}

	item, err := s.GetEntryItem("Show.S01")
	if err != nil {
		t.Fatalf("folder lost from the name index while an entry still renders it: %v", err)
	}
	file, err := item.GetFile("a.mkv")
	if err != nil {
		t.Fatalf("folder serves no files: %v", err)
	}
	if file.InfoHash != "synthetic" {
		t.Fatalf("file still points at the deleted entry: %q", file.InfoHash)
	}
}

// The folder must still disappear once the last entry behind it is gone.
func TestDeleteRemovesFolderWhenNoEntryRemains(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.AddOrUpdate(entryWithFile("realhash", "Show.S02", "a.mkv", time.Now())); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("realhash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEntryItem("Show.S02"); !errors.Is(err, appendstore.ErrKeyNotFound) {
		t.Fatalf("expected the folder to be gone, got %v", err)
	}
}

// Databases written by older builds already carry folders that were dropped
// from the name index while their entry stayed live. Startup rebuilds them.
func TestReconcileEntryItemsRebuildsMissingFolder(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.AddOrUpdate(entryWithFile("synthetic", "Show.S03", "a.mkv", time.Now())); err != nil {
		t.Fatal(err)
	}
	// Reproduce the damaged state: the entry is live, its folder is not indexed.
	if err := s.entryItems.Delete("Show.S03"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	item, err := s.GetEntryItem("Show.S03")
	if err != nil {
		t.Fatalf("startup did not rebuild the folder: %v", err)
	}
	if _, err := item.GetFile("a.mkv"); err != nil {
		t.Fatalf("rebuilt folder serves no files: %v", err)
	}
	if item.Size != 10 {
		t.Fatalf("rebuilt folder size = %d, want 10", item.Size)
	}
}

// A healthy database has nothing to rebuild.
func TestReconcileEntryItemsLeavesHealthyIndexAlone(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	s, err := NewStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.AddOrUpdate(entryWithFile("realhash", "Show.S04", "a.mkv", time.Now())); err != nil {
		t.Fatal(err)
	}
	count, err := s.ReconcileEntryItems()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rebuilt %d folders on a healthy database", count)
	}
}
