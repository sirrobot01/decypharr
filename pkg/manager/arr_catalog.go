package manager

import (
	"context"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// managedCatalogBatch bounds how many entries a full catalog scan holds at once.
const managedCatalogBatch = 256

type managedArrCatalog struct {
	storage *storage.Storage
	logger  zerolog.Logger
}

// catalogSkips counts the files a scan left out of the catalog. A deleted file
// is left out on purpose. A file with no ID is not: every file is given a
// stable ID before it is persisted, so one without an ID cannot be indexed,
// cannot be reacquired, and is invisible to every count downstream.
type catalogSkips struct {
	deleted int
	noID    int
}

// ListManagedFiles returns every managed file. Ownership is not filtered by
// entry category: an entry synced from the provider carries no category, and an
// Arr can only import a file Decypharr symlinked, so the library symlink is the
// only proof of ownership. A non-empty entryID reads that one entry: a targeted
// reindex runs after every completed download, so it must never walk the whole
// library.
func (c managedArrCatalog) ListManagedFiles(ctx context.Context, entryID string) ([]reacquire.ManagedFile, error) {
	if entryID != "" {
		entry, err := c.storage.Get(entryID)
		if err != nil || entry == nil {
			return nil, nil
		}
		var skips catalogSkips
		entryFiles, err := c.entryFiles(entry, &skips)
		if err != nil {
			return nil, err
		}
		if skips.noID > 0 {
			c.logger.Warn().
				Str("entry_id", entryID).
				Int("skipped_no_id", skips.noID).
				Msg("Managed entry has files without an ID")
		}
		return entryFiles, nil
	}

	files := make([]reacquire.ManagedFile, 0)
	var skips catalogSkips
	entries := 0
	var missingIDs []*storage.Entry
	err := c.storage.ForEachBatch(managedCatalogBatch, func(batch []*storage.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for _, entry := range batch {
			entries++
			if needsFileIDs(entry) {
				missingIDs = append(missingIDs, entry)
				continue
			}
			files = append(files, entryManagedFiles(entry, &skips)...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// File IDs are assigned by a write, which cannot run inside the scan.
	backfilled := len(missingIDs)
	for _, entry := range missingIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entryFiles, err := c.entryFiles(entry, &skips)
		if err != nil {
			return nil, err
		}
		files = append(files, entryFiles...)
	}

	event := c.logger.Debug()
	if skips.noID > 0 {
		// The backfill above ran and still left files without an ID.
		event = c.logger.Warn()
	}
	event.
		Int("entries", entries).
		Int("files", len(files)).
		Int("id_backfilled_entries", backfilled).
		Int("skipped_no_id", skips.noID).
		Int("skipped_deleted", skips.deleted).
		Msg("Scanned the managed catalog")
	return files, nil
}

func (c managedArrCatalog) entryFiles(entry *storage.Entry, skips *catalogSkips) ([]reacquire.ManagedFile, error) {
	if needsFileIDs(entry) {
		if err := c.storage.AddOrUpdate(entry); err != nil {
			return nil, err
		}
	}
	return entryManagedFiles(entry, skips), nil
}

func entryManagedFiles(entry *storage.Entry, skips *catalogSkips) []reacquire.ManagedFile {
	folder := entry.GetFolder()
	files := make([]reacquire.ManagedFile, 0, len(entry.Files))
	for fileName, file := range entry.Files {
		if file == nil {
			continue
		}
		if file.Deleted {
			skips.deleted++
			continue
		}
		if file.ID == "" {
			skips.noID++
			continue
		}
		if file.Name != "" {
			fileName = file.Name
		}
		files = append(files, reacquire.ManagedFile{
			EntryID:     entry.InfoHash,
			EntryName:   entry.Name,
			EntryFolder: folder,
			FileID:      file.ID,
			FileName:    fileName,
			FileSize:    file.Size,
			DownloadID:  entry.InfoHash,
		})
	}
	return files
}

func needsFileIDs(entry *storage.Entry) bool {
	for _, file := range entry.Files {
		if file != nil && !file.Deleted && file.ID == "" {
			return true
		}
	}
	return false
}
