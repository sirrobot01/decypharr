package storage

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/sirrobot01/appendstore"
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
