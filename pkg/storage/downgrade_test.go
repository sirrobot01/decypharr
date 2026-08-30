package storage

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
)

func newTestStorage(t *testing.T, dir string) *Storage {
	t.Helper()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func addTestEntry(t *testing.T, s *Storage, name string) {
	t.Helper()
	entry := &Entry{
		Protocol:         config.ProtocolTorrent,
		InfoHash:         "hash-" + name,
		Name:             name,
		OriginalFilename: name,
		Category:         "movies",
		ActiveProvider:   "provider-a",
		Size:             1234,
		AddedOn:          utils.Now(),
		Files:            map[string]*File{},
	}
	if err := s.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
}

// The whole point of the command: a user on the current format can go back to a
// release that only reads version 3, keeping what they have.
func TestDowngradeRoundTripsThroughVersion3(t *testing.T) {
	dir := t.TempDir()
	s := newTestStorage(t, dir)
	addTestEntry(t, s, "Example Show")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	if err := Downgrade(dir, 3, false); err != nil {
		t.Fatal(err)
	}

	for _, name := range storeNames {
		path := filepath.Join(dir, name+".db")
		if got := dbVersion(t, path); got != 3 {
			t.Fatalf("%s = version %d, want 3", name, got)
		}
		// The databases this build was reading must still be there.
		if _, err := os.Stat(path + downgradeBackupSuffix); err != nil {
			t.Fatalf("%s: no pre-downgrade copy: %v", name, err)
		}
		// Nothing half-finished is left behind.
		if _, err := os.Stat(path + downgradeWorkSuffix); !os.IsNotExist(err) {
			t.Fatalf("%s: working copy left behind: %v", name, err)
		}
	}

	// Coming back up must restore the entry, which is what makes this a round
	// trip rather than a one-way export.
	restored := newTestStorage(t, dir)
	defer restored.Close()
	entry, err := restored.Get("hash-Example Show")
	if err != nil {
		t.Fatalf("entry did not survive the round trip: %v", err)
	}
	if entry.Category != "movies" || entry.ActiveProvider != "provider-a" || entry.Size != 1234 {
		t.Fatalf("entry lost metadata: %+v", entry)
	}
}

// A second run would otherwise overwrite the copy the first one kept.
func TestDowngradeRefusesExistingBackup(t *testing.T) {
	dir := t.TempDir()
	s := newTestStorage(t, dir)
	addTestEntry(t, s, "Example Show")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	stale := filepath.Join(dir, "entries.db"+downgradeBackupSuffix)
	if err := os.WriteFile(stale, []byte("an earlier run"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Downgrade(dir, 3, false); err == nil {
		t.Fatal("downgrade ran with a backup already in place")
	}
	kept, err := os.ReadFile(stale)
	if err != nil || string(kept) != "an earlier run" {
		t.Fatalf("existing backup = %q, %v", kept, err)
	}
	// The refusal happens before anything is converted.
	if got := dbVersion(t, filepath.Join(dir, "entries.db")); got == 3 {
		t.Fatal("a database was converted despite the refusal")
	}

	// Going back and forth more than once needs the stale copy replaced.
	if err := Downgrade(dir, 3, true); err != nil {
		t.Fatalf("downgrade with -replace-backup: %v", err)
	}
	if got := dbVersion(t, filepath.Join(dir, "entries.db")); got != 3 {
		t.Fatalf("entries = version %d, want 3", got)
	}
	if got := dbVersion(t, stale); got == 0 {
		t.Fatal("the stale backup was not replaced with the current database")
	}
}

func TestDowngradeReportsAnEmptyDirectory(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	if err := Downgrade(t.TempDir(), 3, false); err == nil {
		t.Fatal("downgrade accepted a directory with no databases")
	}
}
