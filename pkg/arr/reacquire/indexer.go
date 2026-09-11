package reacquire

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/logger"
)

// ManagedFile is the stable Decypharr-side identity used by the arr.Arr index.
type ManagedFile struct {
	EntryID     string `json:"entry_id"`
	EntryName   string `json:"entry_name"`
	EntryFolder string `json:"entry_folder"`
	FileID      string `json:"file_id"`
	FileName    string `json:"file_name"`
	FileSize    int64  `json:"file_size"`
	DownloadID  string `json:"download_id"`
}

// ManagedCatalog exposes managed files without coupling pkg/arr to storage.
// A non-empty entry ID must return only that entry's files: a targeted index
// runs after every completed download and must not walk the whole library.
// The catalog is not scoped to an Arr; the library symlink decides which Arr
// owns a managed file.
type ManagedCatalog interface {
	ListManagedFiles(ctx context.Context, entryID string) ([]ManagedFile, error)
}

type bindingWriter interface {
	UpsertBinding(Binding) error
	ReplaceArrGeneration(string, uint64, []Binding) error
}

type indexRequest struct {
	arrName string
	entryID string
	attempt int
	version uint64
}

type Indexer struct {
	arrs        *arr.Service
	catalog     ManagedCatalog
	writer      bindingWriter
	managedRoot string
	logger      zerolog.Logger

	wake               chan struct{}
	queue              []indexRequest
	pending            map[string]struct{}
	covered            map[string]uint64
	mu                 sync.Mutex
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	lifecycleMu        sync.Mutex
	started            atomic.Bool
	closed             atomic.Bool
	generationSequence atomic.Uint64
	requestSequence    atomic.Uint64
}

var targetedIndexBackoff = [...]time.Duration{
	time.Second,
	2 * time.Second,
	5 * time.Second,
	10 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
	30 * time.Second,
}

// NewIndexer builds the indexer. managedRoot is the mount directory that holds
// every entry folder; library symlinks that point outside it are not managed by
// Decypharr and are skipped.
func NewIndexer(arrs *arr.Service, catalog ManagedCatalog, writer bindingWriter, managedRoot string) *Indexer {
	return &Indexer{
		arrs:        arrs,
		catalog:     catalog,
		writer:      writer,
		managedRoot: managedRoot,
		logger:      logger.New("arr-indexer"),
		wake:        make(chan struct{}, 1),
		pending:     make(map[string]struct{}),
		covered:     make(map[string]uint64),
	}
}

func (i *Indexer) Start(ctx context.Context) error {
	if i == nil || i.arrs == nil || i.catalog == nil || i.writer == nil {
		return fmt.Errorf("arr indexer is not configured")
	}
	i.lifecycleMu.Lock()
	defer i.lifecycleMu.Unlock()
	if i.closed.Load() {
		return fmt.Errorf("arr indexer is closed")
	}
	if !i.started.CompareAndSwap(false, true) {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	workerCtx, cancel := context.WithCancel(ctx)
	i.mu.Lock()
	i.ctx = workerCtx
	i.cancel = cancel
	i.mu.Unlock()
	i.wg.Go(func() { i.run(workerCtx) })
	i.Refresh()
	return nil
}

func (i *Indexer) Close() {
	if i == nil {
		return
	}
	i.lifecycleMu.Lock()
	if !i.closed.CompareAndSwap(false, true) {
		i.lifecycleMu.Unlock()
		return
	}
	if i.cancel != nil {
		i.cancel()
	}
	i.lifecycleMu.Unlock()
	i.wg.Wait()
}

func (i *Indexer) Refresh() bool {
	return i.enqueue(indexRequest{})
}

func (i *Indexer) ReindexEntry(arrName, entryID string) bool {
	arrName = strings.TrimSpace(arrName)
	entryID = strings.TrimSpace(entryID)
	if arrName == "" || entryID == "" {
		return false
	}
	return i.enqueue(indexRequest{
		arrName: arrName,
		entryID: entryID,
		version: i.requestSequence.Add(1),
	})
}

func (i *Indexer) enqueue(request indexRequest) bool {
	if i == nil || i.closed.Load() {
		return false
	}
	key := request.arrName + "\x00" + request.entryID
	i.mu.Lock()
	if i.ctx == nil || i.ctx.Err() != nil {
		i.mu.Unlock()
		return false
	}
	if _, exists := i.pending[key]; exists {
		i.mu.Unlock()
		return true
	}
	i.pending[key] = struct{}{}
	i.queue = append(i.queue, request)
	i.mu.Unlock()

	select {
	case i.wake <- struct{}{}:
	default:
	}
	return true
}

func (i *Indexer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-i.wake:
			for {
				request, ok := i.nextRequest()
				if !ok {
					break
				}
				i.handle(ctx, request)
				if ctx.Err() != nil {
					return
				}
			}
		}
	}
}

func (i *Indexer) nextRequest() (indexRequest, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if len(i.queue) == 0 {
		return indexRequest{}, false
	}
	request := i.queue[0]
	i.queue[0] = indexRequest{}
	i.queue = i.queue[1:]
	delete(i.pending, request.arrName+"\x00"+request.entryID)
	return request, true
}

func (i *Indexer) handle(ctx context.Context, request indexRequest) {
	if request.arrName == "" {
		i.handleRefresh(ctx, request)
		return
	}
	if request.entryID == "" {
		i.handleArrRefresh(ctx, request)
		return
	}
	if i.coveredByRefresh(request) {
		return
	}

	instance, ok := i.arrs.Get(request.arrName)
	if !ok {
		i.logger.Warn().Str("arr", request.arrName).Msg("Targeted Arr index skipped: instance not found")
		return
	}
	managed, err := i.catalog.ListManagedFiles(ctx, request.entryID)
	if err == nil {
		var stats matchStats
		stats, err = i.reconcile(ctx, instance, request, managed)
		if err == nil && stats.matched() > 0 {
			return
		}
		if err == nil {
			i.retryTargeted(ctx, request)
			return
		}
	}
	i.logger.Debug().Err(err).Str("arr", request.arrName).Str("entry_id", request.entryID).Msg("Targeted Arr index attempt failed")
	i.retryTargeted(ctx, request)
}

// handleRefresh reconciles every Sonarr and Radarr instance against one read of
// the managed catalog. The catalog is the same for all of them, and walking the
// whole entry store once per Arr is the expensive half of a refresh.
func (i *Indexer) handleRefresh(ctx context.Context, request indexRequest) {
	coverThrough := i.requestSequence.Load()
	managed, err := i.catalog.ListManagedFiles(ctx, "")
	if err != nil {
		i.logger.Warn().Err(err).Msg("Arr index reconciliation failed: cannot read managed files")
		if request.attempt < len(targetedIndexBackoff) && ctx.Err() == nil {
			i.retry(ctx, request, targetedIndexBackoff[request.attempt])
		}
		return
	}

	failed := false
	indexed, arrs := 0, 0
	for _, instance := range i.arrs.All() {
		if instance.Type != arr.Sonarr && instance.Type != arr.Radarr {
			continue
		}
		started := time.Now()
		stats, err := i.reconcile(ctx, instance, indexRequest{}, managed)
		if err != nil {
			failed = true
			i.logger.Warn().Err(err).Str("arr", instance.Name).Msg("Arr index reconciliation failed")
			continue
		}
		indexed += stats.matched()
		arrs++
		i.markCovered(instance.Name, coverThrough)
		stats.fields(i.logger.Info()).
			Str("arr", instance.Name).
			Dur("took", time.Since(started)).
			Msg("Indexed Arr library")
	}
	// managed_files counts the whole store, so only a total over every Arr says
	// how much of it a library actually claims. The rest is not an error: an
	// entry no Arr imported is still mounted and streamable.
	i.logger.Info().
		Int("arrs", arrs).
		Int("managed_files", len(managed)).
		Int("indexed", indexed).
		Int("unclaimed", len(managed)-indexed).
		Msg("Arr index refreshed")
	if failed && request.attempt < len(targetedIndexBackoff) && ctx.Err() == nil {
		i.retry(ctx, request, targetedIndexBackoff[request.attempt])
	}
}

// handleArrRefresh performs one authoritative full reconciliation for a
// single Arr. Exhausted targeted requests promote to this queue key, so any
// number of them coalesce into one library read and one generation replace.
func (i *Indexer) handleArrRefresh(ctx context.Context, request indexRequest) {
	instance, ok := i.arrs.Get(request.arrName)
	if !ok {
		i.logger.Warn().Str("arr", request.arrName).Msg("Arr index refresh skipped: instance not found")
		return
	}
	if instance.Type != arr.Sonarr && instance.Type != arr.Radarr {
		return
	}

	coverThrough := i.requestSequence.Load()
	managed, err := i.catalog.ListManagedFiles(ctx, "")
	if err == nil {
		started := time.Now()
		var stats matchStats
		stats, err = i.reconcile(ctx, instance, indexRequest{}, managed)
		if err == nil {
			i.markCovered(instance.Name, coverThrough)
			stats.fields(i.logger.Info()).
				Str("arr", instance.Name).
				Dur("took", time.Since(started)).
				Msg("Indexed Arr library")
			return
		}
	}

	i.logger.Warn().Err(err).Str("arr", request.arrName).Msg("Arr index reconciliation failed")
	if request.attempt < len(targetedIndexBackoff) && ctx.Err() == nil {
		i.retry(ctx, request, targetedIndexBackoff[request.attempt])
	}
}

func (i *Indexer) coveredByRefresh(request indexRequest) bool {
	if request.version == 0 {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return request.version <= i.covered[request.arrName]
}

func (i *Indexer) markCovered(arrName string, version uint64) {
	i.mu.Lock()
	if i.covered == nil {
		i.covered = make(map[string]uint64)
	}
	i.covered[arrName] = max(i.covered[arrName], version)
	i.mu.Unlock()
}

// libraryFor reads the Arr files a reconcile matches against. A targeted index
// narrows the read to the series or movies the entry was imported into: a full
// Sonarr scan lists every series and then costs two requests per series, and it
// used to run again on every backoff attempt.
//
// A targeted request never widens itself to the whole library. If history does
// not name the media or targeted reads keep failing, the retry is eventually
// promoted to a coalesced authoritative refresh for that Arr.
func (i *Indexer) libraryFor(ctx context.Context, instance arr.Arr, request indexRequest, managed []ManagedFile) ([]arr.LibraryFile, error) {
	if request.entryID == "" {
		return i.arrs.LibraryFiles(ctx, instance.Name)
	}
	mediaIDs := i.targetedMediaIDs(ctx, instance, managed)
	if len(mediaIDs) == 0 {
		return nil, nil
	}
	return i.arrs.LibraryFilesForMedia(ctx, instance.Name, mediaIDs)
}

// targetedMediaIDs resolves the series or movies an entry was imported into,
// from the Arr history of the downloads that produced its managed files. It
// returns nothing when the files carry no download ID or history no longer
// holds the record. The caller retries before promoting to a coalesced refresh.
func (i *Indexer) targetedMediaIDs(ctx context.Context, instance arr.Arr, managed []ManagedFile) []int {
	var downloads []string
	for _, file := range managed {
		if file.DownloadID != "" && !slices.Contains(downloads, file.DownloadID) {
			downloads = append(downloads, file.DownloadID)
		}
	}

	var ids []int
	for _, downloadID := range downloads {
		records, err := i.arrs.DownloadHistory(ctx, instance.Name, downloadID, "")
		if err != nil {
			i.logger.Debug().Err(err).
				Str("arr", instance.Name).
				Str("download_id", downloadID).
				Msg("Targeted Arr index could not read download history")
			continue
		}
		for _, record := range records {
			id := record.SeriesID
			if instance.Type == arr.Radarr {
				id = record.MovieID
			}
			if id > 0 && !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// retryTargeted waits for the Arr to import the entry, then indexes it again.
func (i *Indexer) retryTargeted(ctx context.Context, request indexRequest) {
	if ctx.Err() != nil {
		return
	}
	if request.attempt < len(targetedIndexBackoff) {
		i.retry(ctx, request, targetedIndexBackoff[request.attempt])
		return
	}
	i.enqueue(indexRequest{arrName: request.arrName})
}

func (i *Indexer) reconcile(ctx context.Context, instance arr.Arr, request indexRequest, managed []ManagedFile) (matchStats, error) {
	entryID := request.entryID
	library, err := i.libraryFor(ctx, instance, request, managed)
	if err != nil {
		return matchStats{}, err
	}

	generation := uint64(time.Now().UnixMilli()) + i.generationSequence.Add(1)
	matches, stats := matchLibraryFiles(library, managed, i.managedRoot)
	bindings := bindingsFromMatches(instance, generation, matches)
	if entryID == "" {
		if err := i.writer.ReplaceArrGeneration(instance.Name, generation, bindings); err != nil {
			return stats, err
		}
		return stats, nil
	}
	for _, binding := range bindings {
		if err := i.writer.UpsertBinding(binding); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (i *Indexer) retry(ctx context.Context, request indexRequest, delay time.Duration) {
	request.attempt++
	i.wg.Go(func() {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			i.enqueue(request)
		}
	})
}
