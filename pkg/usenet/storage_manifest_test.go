package usenet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sirrobot01/decypharr/internal/customerror"
)

func newTestNZBStorage(t *testing.T, metaDir string) *NZBStorage {
	t.Helper()
	// These paths never log, so the zero logger keeps the test off the config file.
	return &NZBStorage{metaDir: metaDir}
}

// A manifest that is gone while its directory is intact is a permanent loss, so
// repair can replace the release.
func TestReadMetaReportsMissingManifest(t *testing.T) {
	store := newTestNZBStorage(t, t.TempDir())

	_, err := store.readMetaLocked("gone")
	if !errors.Is(err, customerror.UsenetManifestMissingError) {
		t.Fatalf("err = %v, want UsenetManifestMissingError", err)
	}
}

// An unmounted data dir makes every read return ENOENT. Classifying that as a
// missing manifest would mark the whole library broken and blocklist it in the
// arrs, so it has to stay unclassified and defer instead.
func TestReadMetaDoesNotBlameManifestWhenDirIsGone(t *testing.T) {
	store := newTestNZBStorage(t, filepath.Join(t.TempDir(), "never-mounted"))

	_, err := store.readMetaLocked("gone")
	if err == nil {
		t.Fatal("err = nil, want failure")
	}
	if errors.Is(err, customerror.UsenetManifestMissingError) {
		t.Fatalf("err = %v, must not be classified as a missing manifest", err)
	}
}

func TestReadMetaReturnsStoredBlob(t *testing.T) {
	dir := t.TempDir()
	store := newTestNZBStorage(t, dir)
	want := []byte("meta-bytes")
	if err := os.WriteFile(filepath.Join(dir, "nzb-1"+metaFileExtension), want, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := store.readMetaLocked("nzb-1")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("data = %q, want %q", got, want)
	}
}

// A blob that does not decode is a permanent local failure too: the bytes will
// not start parsing on a later sweep.
func TestSampleFileMessageIDsReportsInvalidManifest(t *testing.T) {
	dir := t.TempDir()
	store := newTestNZBStorage(t, dir)
	// v2 magic followed by truncated garbage, so the codec path is taken and fails.
	blob := []byte{codecMagicV2, 0xff, 0xff, 0xff}
	if err := os.WriteFile(filepath.Join(dir, "nzb-1"+metaFileExtension), blob, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := store.SampleFileMessageIDs("nzb-1", "movie.mkv", 10)
	if !errors.Is(err, customerror.UsenetManifestInvalidError) {
		t.Fatalf("err = %v, want UsenetManifestInvalidError", err)
	}
}
