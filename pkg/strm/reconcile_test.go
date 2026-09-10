package strm

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func newTestReconciler(t *testing.T) *Reconciler {
	t.Helper()
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	strg, err := storage.NewStorage(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = strg.Close() })

	m := NewReconciler(t.Context(), strg, nil, zerolog.Nop())

	cfg := config.Get()
	cfg.AppURL = "http://media.local:8282"
	cfg.Strm.Enabled = true
	cfg.Strm.Path = t.TempDir()
	noSidecars := false
	cfg.Strm.DownloadSidecars = &noSidecars
	return m
}

func addStrmTestEntry(t *testing.T, m *Reconciler, infohash, name string, files ...string) *storage.Entry {
	t.Helper()
	entry := &storage.Entry{
		InfoHash: infohash,
		Name:     name,
		Files:    make(map[string]*storage.File),
	}
	for _, f := range files {
		entry.Files[f] = &storage.File{Name: f, Size: 100, InfoHash: infohash}
	}
	if err := m.storage.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	return entry
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Golden-tree: seed the export tree with current, stale, orphaned, and
// foreign .strm files, then assert the sweep converges disk to the desired
// state and leaves foreign files alone.
func TestStrmSweepGoldenTree(t *testing.T) {
	m := newTestReconciler(t)
	cfg := config.Get()

	infohash := "aabbccddeeff00112233445566778899aabbccdd"
	entry := addStrmTestEntry(t, m, infohash, "Movie.2023.1080p", "Movie.2023.1080p.mkv")
	fileID := entry.Files["Movie.2023.1080p.mkv"].ID
	want := FileURL(BaseURL(cfg), cfg.Strm.Secret, infohash, fileID, "Movie.2023.1080p.mkv")

	unknown := strings.Repeat("00", 20)
	strmPath := filepath.Join(cfg.Strm.Path, "Movie.2023.1080p", "Movie.2023.1080p.strm")
	orphanPath := filepath.Join(cfg.Strm.Path, "Gone.2020", "Gone.2020.strm")
	foreignPath := filepath.Join(cfg.Strm.Path, "foreign.strm")

	// Stale content at the desired path (old host), an orphan from a deleted
	// entry, and a foreign file we must never touch.
	stale := "http://old-host:9999/stream/" + infohash + "/" + fileID + "/Movie.2023.1080p.mkv?s=deadbeef"
	mustWrite(t, strmPath, stale)
	mustWrite(t, orphanPath, FileURL("http://media.local:8282", cfg.Strm.Secret, unknown, "0123456789abcdef", "Gone.mkv"))
	mustWrite(t, foreignPath, "plex://movie/12345")

	rep, err := m.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Errors) != 0 {
		t.Fatalf("sweep errors: %v", rep.Errors)
	}
	if got := mustRead(t, strmPath); got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
	if rep.Written != 1 || rep.Deleted != 1 {
		t.Errorf("written = %d, deleted = %d, want 1, 1", rep.Written, rep.Deleted)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Error("orphan not deleted")
	}
	if _, err := os.Stat(filepath.Dir(orphanPath)); !os.IsNotExist(err) {
		t.Error("empty orphan folder not pruned")
	}
	if got := mustRead(t, foreignPath); got != "plex://movie/12345" {
		t.Errorf("foreign file touched: %q", got)
	}

	// Convergence: a second sweep verifies everything and writes nothing.
	rep, err = m.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Written != 0 || rep.Deleted != 0 || rep.Verified != 1 {
		t.Errorf("second sweep written = %d, deleted = %d, verified = %d", rep.Written, rep.Deleted, rep.Verified)
	}
}

func TestStrmSweepDisabled(t *testing.T) {
	m := newTestReconciler(t)
	config.Get().Strm.Enabled = false

	if _, err := m.Sweep(context.Background()); err == nil {
		t.Fatal("sweep must refuse to run while disabled")
	}
}

// A repair that renames a file (new file ID) must replace the old .
func TestStrmSyncEntryRemovesStaleAfterRename(t *testing.T) {
	m := newTestReconciler(t)
	cfg := config.Get()

	infohash := "aabbccddeeff00112233445566778899aabbccdd"
	entry := addStrmTestEntry(t, m, infohash, "Show.S01", "Show.S01E01.mkv")

	rep := &Report{}
	m.syncEntry(context.Background(), entry, rep)
	oldPath := filepath.Join(cfg.Strm.Path, entry.GetFolder(), "Show.S01E01.strm")
	if _, err := os.Stat(oldPath); err != nil {
		t.Fatal(err)
	}

	// Repair replaces the file under a new name.
	delete(entry.Files, "Show.S01E01.mkv")
	entry.Files["Show.S01E01.Repack.mkv"] = &storage.File{Name: "Show.S01E01.Repack.mkv", Size: 100, InfoHash: infohash}
	if err := m.storage.AddOrUpdate(entry); err != nil {
		t.Fatal(err)
	}
	m.syncEntry(context.Background(), entry, rep)

	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Error("stale .strm for renamed file not removed")
	}
	newPath := filepath.Join(cfg.Strm.Path, entry.GetFolder(), "Show.S01E01.Repack.strm")
	content := mustRead(t, newPath)
	ih, id, ok := ParseURL(content)
	if !ok || ih != infohash || id != entry.Files["Show.S01E01.Repack.mkv"].ID {
		t.Errorf("new .strm content = %q", content)
	}
}

// Deleting an entry removes its files from the export tree without waiting
// for a sweep; the folder is pruned when empty.
func TestStrmRemoveEntry(t *testing.T) {
	m := newTestReconciler(t)
	cfg := config.Get()

	infohash := "aabbccddeeff00112233445566778899aabbccdd"
	entry := addStrmTestEntry(t, m, infohash, "Movie.2023", "Movie.2023.mkv")
	rep := &Report{}
	m.syncEntry(context.Background(), entry, rep)

	dir := filepath.Join(cfg.Strm.Path, entry.GetFolder())
	if _, err := os.Stat(filepath.Join(dir, "Movie.2023.strm")); err != nil {
		t.Fatal(err)
	}

	// Synchronous variant of RemoveEntryAsync's work for the test: reuse the
	// async trigger and wait for the folder to disappear.
	m.RemoveEntryAsync(entry)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("entry folder %s not removed", dir)
}

func TestSidecarStreamIsCompleteBeforePublication(t *testing.T) {
	for _, body := range []string{"complete", "short"} {
		t.Run(body, func(t *testing.T) {
			reconciler := newTestReconciler(t)
			entry := &storage.Entry{InfoHash: "entry"}
			file := &storage.File{Name: "movie.srt", Size: 8}
			ctx := t.Context()
			reconciler.openStream = func(gotCtx context.Context, gotEntry *storage.Entry, filename string) (io.ReadCloser, error) {
				if gotCtx != ctx || gotEntry != entry || filename != file.Name {
					t.Fatal("stream request lost its context or identity")
				}
				return io.NopCloser(strings.NewReader(body)), nil
			}
			dest := filepath.Join(t.TempDir(), file.Name)
			err := reconciler.downloadSidecar(ctx, entry, file, dest)
			if body == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				if got := mustRead(t, dest); got != body {
					t.Fatalf("sidecar = %q", got)
				}
			} else {
				if err == nil {
					t.Fatal("short sidecar was accepted")
				}
				if _, err := os.Stat(dest); !os.IsNotExist(err) {
					t.Fatalf("partial sidecar was published: %v", err)
				}
				if _, err := os.Stat(dest + ".part"); !os.IsNotExist(err) {
					t.Fatalf("partial temporary file remains: %v", err)
				}
			}
		})
	}
}
