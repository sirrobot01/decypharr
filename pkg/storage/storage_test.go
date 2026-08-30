package storage

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

// writeLegacyDB writes a store log carrying an older format header. Only the
// header matters: appendstore decides to migrate before it reads any record.
func writeLegacyDB(t *testing.T, path string, version uint32) {
	t.Helper()
	header := make([]byte, 16)
	copy(header, "HYBR")
	binary.LittleEndian.PutUint32(header[4:8], version)
	if err := os.WriteFile(path, header, 0o644); err != nil {
		t.Fatal(err)
	}
}

func dbVersion(t *testing.T, path string) uint32 {
	t.Helper()
	header, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return binary.LittleEndian.Uint32(header[4:8])
}

// Upgrading rewrites the databases in a format older Decypharr builds reject,
// so startup must leave each original behind for a downgrade.
func TestStartupKeepsDowngradePath(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	dir := t.TempDir()
	for _, name := range storeNames {
		writeLegacyDB(t, filepath.Join(dir, name+".db"), 3)
	}

	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, name := range storeNames {
		path := filepath.Join(dir, name+".db")
		if got := dbVersion(t, path); got == 3 {
			t.Fatalf("%s was not migrated", name)
		}
		backup := path + ".v3.bak"
		if got := dbVersion(t, backup); got != 3 {
			t.Fatalf("%s backup is not the pre-upgrade format: v%d", name, got)
		}
	}
}

// A database already in the current format has nothing to preserve.
func TestStartupWithoutMigrationLeavesNoBackups(t *testing.T) {
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	dir := t.TempDir()
	s, err := NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening a fresh database must not migrate anything.
	s, err = NewStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bak" {
			t.Fatalf("unexpected backup %s", e.Name())
		}
	}
}
