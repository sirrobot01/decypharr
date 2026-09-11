package reacquire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

func TestMatchLibraryFilesBySymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, _ := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Movie.Release", FileID: "file", FileName: "movie.mkv"}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].managed.FileID != "file" {
		t.Fatalf("managed file ID = %q, want file", matches[0].managed.FileID)
	}
}

func TestMatchLibraryFilesResolvesRelativeSymlink(t *testing.T) {
	dir := t.TempDir()
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join("..", "managed", "Movie.Release", "movie.mkv")
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, _ := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Movie.Release", FileID: "file", FileName: "movie.mkv"}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
}

func TestMatchLibraryFilesSkipsNonSymlink(t *testing.T) {
	dir := t.TempDir()
	libraryPath := filepath.Join(dir, "Movie.mkv")
	if err := os.WriteFile(libraryPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: filepath.Base(dir), FileID: "file", FileName: "Movie.mkv"}},
		dir,
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.notSymlink != 1 {
		t.Fatalf("not-symlink = %d, want 1", stats.notSymlink)
	}
}

func TestMatchLibraryFilesRequiresFolderAndFilename(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Other.Release", FileID: "file", FileName: "movie.mkv"}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.unknownEntry != 1 {
		t.Fatalf("unknown-entry = %d, want 1", stats.unknownEntry)
	}
}

func TestMatchLibraryFilesRejectsAmbiguousManagedFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{
			{EntryID: "entry-1", EntryFolder: "Movie.Release", FileID: "file-1", FileName: "movie.mkv"},
			{EntryID: "entry-2", EntryFolder: "Movie.Release", FileID: "file-2", FileName: "movie.mkv"},
		},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.ambiguousTarget != 1 {
		t.Fatalf("ambiguous-target = %d, want 1", stats.ambiguousTarget)
	}
}

func TestMatchLibraryFilesRejectsDuplicateArrTargets(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryDir := filepath.Join(dir, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	libraryPaths := []string{
		filepath.Join(libraryDir, "Movie.mkv"),
		filepath.Join(libraryDir, "Movie copy.mkv"),
	}
	for _, path := range libraryPaths {
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{
			{ArrFileID: 42, Path: libraryPaths[0]},
			{ArrFileID: 43, Path: libraryPaths[1]},
		},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Movie.Release", FileID: "file", FileName: "movie.mkv"}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.conflicted != 2 {
		t.Fatalf("conflicted = %d, want 2", stats.conflicted)
	}
}

func TestReconcileBuildsIndexFromSymlinks(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	regularPath := filepath.Join(dir, "library", "Other.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(regularPath, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v3/movie" {
			http.NotFound(w, request)
			return
		}
		_, _ = fmt.Fprintf(w, `[{"id":9,"movieFile":{"id":42,"path":%q}},{"id":10,"movieFile":{"id":43,"path":%q}}]`, libraryPath, regularPath)
	}))
	defer server.Close()

	instance := arr.Arr{Name: "radarr", Host: server.URL, Token: "secret", Type: arr.Radarr}
	arrs := arr.New()
	arrs.AddOrUpdate(instance)
	writer := new(recordingBindingWriter)
	managed := []ManagedFile{{
		EntryID:     "entry",
		EntryName:   "Movie Release",
		EntryFolder: "Movie.Release",
		FileID:      "file",
		FileName:    "movie.mkv",
		DownloadID:  "download",
	}}
	indexer := &Indexer{
		arrs:        arrs,
		catalog:     fixedManagedCatalog(managed),
		writer:      writer,
		managedRoot: filepath.Join(dir, "managed"),
	}

	stats, err := indexer.reconcile(t.Context(), instance, indexRequest{}, managed)
	if err != nil {
		t.Fatal(err)
	}
	if len(writer.replacement) != 1 {
		t.Fatalf("bindings = %#v", writer.replacement)
	}
	if stats.libraryFiles != 2 || stats.managedFiles != 1 || stats.matchedFolder != 1 || stats.notSymlink != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	binding := writer.replacement[0]
	if binding.EntryID != "entry" || binding.EntryFileID != "file" || binding.ArrFileID != 42 {
		t.Fatalf("binding = %#v", binding)
	}
}

type fixedManagedCatalog []ManagedFile

func (c fixedManagedCatalog) ListManagedFiles(context.Context, string) ([]ManagedFile, error) {
	return c, nil
}

type recordingBindingWriter struct {
	replacement []Binding
	upserts     []Binding
}

func (w *recordingBindingWriter) UpsertBinding(binding Binding) error {
	w.upserts = append(w.upserts, binding)
	return nil
}

func (w *recordingBindingWriter) ReplaceArrGeneration(_ string, _ uint64, bindings []Binding) error {
	w.replacement = bindings
	return nil
}

func TestMatchLibraryFilesMatchesRenamedFolderBySize(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Show.S01.Complete", "episode.mkv")
	libraryPath := filepath.Join(dir, "library", "Episode.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath, Size: 1024}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Show.S01", FileID: "file", FileName: "episode.mkv", FileSize: 1024}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].confidence != ConfidenceManagedTarget {
		t.Fatalf("confidence = %q, want %q", matches[0].confidence, ConfidenceManagedTarget)
	}
	if stats.matchedSize != 1 || stats.matchedFolder != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestMatchLibraryFilesSizeMatchNeedsUniqueName(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Show.S01.Complete", "episode.mkv")
	libraryPath := filepath.Join(dir, "library", "Episode.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath, Size: 1024}},
		[]ManagedFile{
			{EntryID: "entry-1", EntryFolder: "Show.S01", FileID: "file-1", FileName: "episode.mkv", FileSize: 1024},
			{EntryID: "entry-2", EntryFolder: "Show.S01.Repack", FileID: "file-2", FileName: "episode.mkv", FileSize: 1024},
		},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.ambiguousTarget != 1 {
		t.Fatalf("ambiguous_target = %d, want 1", stats.ambiguousTarget)
	}
}

func TestMatchLibraryFilesSkipsTargetsOutsideMount(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath, Size: 1024}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Movie.Release", FileID: "file", FileName: "movie.mkv", FileSize: 1024}},
		filepath.Join(dir, "managed"),
	)
	if len(matches) != 0 {
		t.Fatalf("matches = %d, want 0", len(matches))
	}
	if stats.foreignTarget != 1 || stats.unknownEntry != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestMatchLibraryFilesFolderMatchWinsOverSizeMatch(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "managed")
	libraryDir := filepath.Join(dir, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exactPath := filepath.Join(libraryDir, "Exact.mkv")
	driftedPath := filepath.Join(libraryDir, "Drifted.mkv")
	if err := os.Symlink(filepath.Join(root, "Show.S01", "episode.mkv"), exactPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Show.S01.Complete", "episode.mkv"), driftedPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{
			{ArrFileID: 42, Path: exactPath, Size: 1024},
			{ArrFileID: 43, Path: driftedPath, Size: 1024},
		},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Show.S01", FileID: "file", FileName: "episode.mkv", FileSize: 1024}},
		root,
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if matches[0].library.ArrFileID != 42 || matches[0].confidence != ConfidenceExactPath {
		t.Fatalf("match = %#v", matches[0])
	}
	if stats.matchedFolder != 1 || stats.conflicted != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestMatchLibraryFilesMatchesNestedMountTarget(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "managed")
	target := filepath.Join(root, "Show.S01", "Season 1", "episode.mkv")
	libraryPath := filepath.Join(dir, "library", "Episode.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	matches, stats := matchLibraryFiles(
		[]arr.LibraryFile{{ArrFileID: 42, Path: libraryPath}},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Show.S01", FileID: "file", FileName: "episode.mkv"}},
		root,
	)
	if len(matches) != 1 {
		t.Fatalf("matches = %d, want 1", len(matches))
	}
	if stats.matchedFolder != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestMatchLibraryFilesSeparatesUnknownEntryFromUnknownFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "managed")
	libraryDir := filepath.Join(dir, "library")
	if err := os.MkdirAll(libraryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	knownEntry := filepath.Join(libraryDir, "Known.mkv")
	goneEntry := filepath.Join(libraryDir, "Gone.mkv")
	if err := os.Symlink(filepath.Join(root, "Show.S01", "other.mkv"), knownEntry); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "Deleted.S01", "episode.mkv"), goneEntry); err != nil {
		t.Fatal(err)
	}

	_, stats := matchLibraryFiles(
		[]arr.LibraryFile{
			{ArrFileID: 42, Path: knownEntry},
			{ArrFileID: 43, Path: goneEntry},
		},
		[]ManagedFile{{EntryID: "entry", EntryFolder: "Show.S01", FileID: "file", FileName: "episode.mkv"}},
		root,
	)
	if stats.unknownFile != 1 || stats.unknownEntry != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestReconcileTargetedReadsOnlyTheEntrysMovie(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "managed", "Movie.Release", "movie.mkv")
	libraryPath := filepath.Join(dir, "library", "Movie.mkv")
	if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, libraryPath); err != nil {
		t.Fatal(err)
	}

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		switch request.URL.Path {
		case "/api/v3/history":
			_, _ = fmt.Fprint(w, `{"page":1,"pageSize":100,"totalRecords":1,"records":[{"id":1,"downloadId":"download","eventType":"grabbed","movieId":9}]}`)
		case "/api/v3/movie/9":
			_, _ = fmt.Fprintf(w, `{"id":9,"movieFile":{"id":42,"movieId":9,"path":%q}}`, libraryPath)
		default:
			t.Errorf("unexpected request to %q", request.URL.Path)
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	instance := arr.Arr{Name: "radarr", Host: server.URL, Token: "secret", Type: arr.Radarr}
	arrs := arr.New()
	arrs.AddOrUpdate(instance)
	writer := new(recordingBindingWriter)
	managed := []ManagedFile{{
		EntryID:     "entry",
		EntryName:   "Movie Release",
		EntryFolder: "Movie.Release",
		FileID:      "file",
		FileName:    "movie.mkv",
		DownloadID:  "download",
	}}
	indexer := &Indexer{
		arrs:        arrs,
		catalog:     fixedManagedCatalog(managed),
		writer:      writer,
		managedRoot: filepath.Join(dir, "managed"),
	}

	request := indexRequest{arrName: "radarr", entryID: "entry"}
	stats, err := indexer.reconcile(t.Context(), instance, request, managed)
	if err != nil {
		t.Fatal(err)
	}
	if stats.matched() != 1 {
		t.Fatalf("stats = %#v", stats)
	}
	if len(writer.upserts) != 1 {
		t.Fatalf("upserts = %#v", writer.upserts)
	}
	if slices.Contains(paths, "/api/v3/movie") {
		t.Fatalf("listed the whole library: %v", paths)
	}
}

func TestReconcileTargetedNeverWidensToTheWholeLibrary(t *testing.T) {
	dir := t.TempDir()

	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		if request.URL.Path != "/api/v3/history" {
			t.Errorf("unexpected request to %q", request.URL.Path)
			http.NotFound(w, request)
			return
		}
		_, _ = fmt.Fprint(w, `{"page":1,"pageSize":100,"totalRecords":0,"records":[]}`)
	}))
	defer server.Close()

	instance := arr.Arr{Name: "radarr", Host: server.URL, Token: "secret", Type: arr.Radarr}
	arrs := arr.New()
	arrs.AddOrUpdate(instance)
	managed := []ManagedFile{{
		EntryID:     "entry",
		EntryFolder: "Movie.Release",
		FileID:      "file",
		FileName:    "movie.mkv",
		DownloadID:  "download",
	}}
	indexer := &Indexer{
		arrs:        arrs,
		catalog:     fixedManagedCatalog(managed),
		writer:      new(recordingBindingWriter),
		managedRoot: filepath.Join(dir, "managed"),
	}

	request := indexRequest{arrName: "radarr", entryID: "entry", attempt: len(targetedIndexBackoff)}
	stats, err := indexer.reconcile(t.Context(), instance, request, managed)
	if err != nil {
		t.Fatal(err)
	}
	if stats.matched() != 0 {
		t.Fatalf("stats = %#v, want no match", stats)
	}
	if slices.Contains(paths, "/api/v3/movie") {
		t.Fatalf("targeted attempt listed the whole library: %v", paths)
	}
	if !slices.Contains(paths, "/api/v3/history") {
		t.Fatalf("targeted attempt did not read history: %v", paths)
	}
}

func TestExhaustedTargetedRequestsCoalesceArrRefresh(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	indexer := NewIndexer(nil, nil, nil, "")
	indexer.ctx = ctx

	finalAttempt := len(targetedIndexBackoff)
	indexer.retryTargeted(ctx, indexRequest{arrName: "radarr", entryID: "one", attempt: finalAttempt})
	indexer.retryTargeted(ctx, indexRequest{arrName: "radarr", entryID: "two", attempt: finalAttempt})

	indexer.mu.Lock()
	queue := slices.Clone(indexer.queue)
	indexer.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("queued %d refreshes, want 1: %#v", len(queue), queue)
	}
	if queue[0].arrName != "radarr" || queue[0].entryID != "" {
		t.Fatalf("request = %#v, want an authoritative Radarr refresh", queue[0])
	}
}

func TestRefreshCoverageSkipsOnlyOlderTargetedRequests(t *testing.T) {
	indexer := NewIndexer(nil, nil, nil, "")
	indexer.markCovered("radarr", 10)

	if !indexer.coveredByRefresh(indexRequest{arrName: "radarr", entryID: "old", version: 10}) {
		t.Fatal("refresh did not cover an older targeted request")
	}
	if indexer.coveredByRefresh(indexRequest{arrName: "radarr", entryID: "new", version: 11}) {
		t.Fatal("refresh covered a targeted request created after its snapshot")
	}
	if indexer.coveredByRefresh(indexRequest{arrName: "sonarr", entryID: "other", version: 1}) {
		t.Fatal("Radarr refresh covered a Sonarr request")
	}
}
