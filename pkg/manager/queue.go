package manager

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type ImportType string

const (
	ImportTypeQBit    ImportType = "qbit"
	ImportTypeAPI     ImportType = "api"
	ImportTypeSABnzbd ImportType = "sabnzbd"
	ImportTypeWatch   ImportType = "watch"
	ImportSwitcher    ImportType = "switcher"
)

type ImportRequest struct {
	Name             string                `json:"name"`
	NZBContent       []byte                `json:"-"`
	Id               string                `json:"id"`
	DownloadFolder   string                `json:"downloadFolder"`
	SelectedDebrid   string                `json:"debrid"`
	Magnet           *utils.Magnet         `json:"magnet"`
	Arr              arr.Arr               `json:"arr"`
	Action           config.DownloadAction `json:"action"`
	DownloadUncached *bool                 `json:"downloadUncached"`
	CallBackUrl      string                `json:"callBackUrl"`
	SkipMultiSeason  bool                  `json:"skip_multi_season"`

	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completedAt"`
	Error       string    `json:"error,omitempty"`

	Type  ImportType `json:"type"`
	Async bool       `json:"async"`
}

func NewTorrentRequest(debrid string, downloadFolder string, magnet *utils.Magnet, arr arr.Arr, action config.DownloadAction, downloadUncached *bool, callBackUrl string, importType ImportType, skipMultiSeason bool) *ImportRequest {

	return &ImportRequest{
		Id:               uuid.New().String(),
		Status:           "started",
		DownloadFolder:   downloadFolder,
		SelectedDebrid:   cmp.Or(arr.SelectedDebrid, debrid), // Use debrid from arr if available
		Magnet:           magnet,
		Arr:              arr,
		Action:           action,
		DownloadUncached: downloadUncached,
		CallBackUrl:      callBackUrl,
		Type:             importType,
		SkipMultiSeason:  skipMultiSeason,
	}
}

func NewNZBRequest(name, downloadFolder string, nzbContent []byte, arr arr.Arr, action config.DownloadAction, callBackUrl string, importType ImportType, skipMultiSeason bool) *ImportRequest {
	return &ImportRequest{
		Name:            name,
		Id:              uuid.New().String(),
		Status:          "started",
		DownloadFolder:  downloadFolder,
		SelectedDebrid:  "usenet", // NZB imports always use usenet
		NZBContent:      nzbContent,
		Arr:             arr,
		Action:          action,
		CallBackUrl:     callBackUrl,
		Type:            importType,
		SkipMultiSeason: skipMultiSeason,
	}
}

type Queue struct {
	storage            *storage.Storage
	logger             zerolog.Logger
	removeStalledAfter time.Duration

	inflightMu sync.Mutex
	inflight   map[string]*inflightEntry
}

// errEntryDeleted is the cancellation cause for work whose queue entry was
// deleted while it was running.
var errEntryDeleted = errors.New("queue entry deleted")

// inflightCancelWait bounds how long a delete waits for cancelled work to stop
// before it removes the entry's files.
const inflightCancelWait = 15 * time.Second

// inflightEntry is the post-download work currently running for one entry.
type inflightEntry struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	done   chan struct{}
}

func newQueue(storage *storage.Storage, removeStalledAfterStr string) *Queue {
	q := &Queue{
		storage: storage,
		logger:  logger.New("queue"),
	}

	if removeStalledAfterStr != "" {
		removeStalledAfter, err := utils.ParseDuration(removeStalledAfterStr)
		if err == nil {
			q.removeStalledAfter = removeStalledAfter
		}
	}

	return q
}

// track registers work for infohash and returns a context derived from parent
// that is cancelled when the entry is deleted from the queue. release must be
// called once the work, including its final queue writes, has finished.
func (q *Queue) track(parent context.Context, infohash string) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	work := &inflightEntry{ctx: ctx, cancel: cancel, done: make(chan struct{})}
	key := strings.ToLower(infohash)
	q.inflightMu.Lock()
	if q.inflight == nil {
		q.inflight = make(map[string]*inflightEntry)
	}
	q.inflight[key] = work
	q.inflightMu.Unlock()

	var once sync.Once
	return ctx, func() {
		once.Do(func() {
			q.inflightMu.Lock()
			if q.inflight[key] == work {
				delete(q.inflight, key)
			}
			q.inflightMu.Unlock()
			cancel(nil)
			close(work.done)
		})
	}
}

// cancelInflight stops any running work for infohash and waits for it to
// release, so the caller can remove the entry and its files afterwards.
func (q *Queue) cancelInflight(infohash string) {
	q.inflightMu.Lock()
	work := q.inflight[strings.ToLower(infohash)]
	q.inflightMu.Unlock()
	if work == nil {
		return
	}
	work.cancel(errEntryDeleted)
	timer := time.NewTimer(inflightCancelWait)
	defer timer.Stop()
	select {
	case <-work.done:
	case <-timer.C:
		q.logger.Warn().Str("infohash", infohash).Msg("Timed out waiting for cancelled download to stop")
	}
}

// isDeleted reports whether infohash has running work that was cancelled
// because the entry was deleted. Such work must not write the entry back.
func (q *Queue) isDeleted(infohash string) bool {
	q.inflightMu.Lock()
	work := q.inflight[strings.ToLower(infohash)]
	q.inflightMu.Unlock()
	return work != nil && isEntryDeleted(work.ctx)
}

// isEntryDeleted reports whether ctx was cancelled by deleting its entry.
func isEntryDeleted(ctx context.Context) bool {
	return errors.Is(context.Cause(ctx), errEntryDeleted)
}

// withCancelInflight cancels running work for each entry before cleanup runs.
func (q *Queue) withCancelInflight(cleanup func(*storage.Entry) error) func(*storage.Entry) error {
	return func(entry *storage.Entry) error {
		q.cancelInflight(entry.InfoHash)
		if cleanup != nil {
			return cleanup(entry)
		}
		return nil
	}
}

func (q *Queue) Add(torrent *storage.Entry) error {
	return q.storage.AddQueue(torrent)
}

func (q *Queue) GetTorrent(infohash string) (*storage.Entry, error) {
	return q.storage.GetQueued(infohash)
}

func (q *Queue) deleteEntryFiles(entry *storage.Entry) error {
	if entry.IsNZB() && entry.Magnet != "" {
		if err := os.Remove(entry.Magnet); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove staged NZB %q: %w", entry.Magnet, err)
		}
	}
	downloadedPath := entry.DownloadPath()
	if downloadedPath == "" {
		return nil
	}
	if err := os.RemoveAll(downloadedPath); err != nil {
		return fmt.Errorf("remove downloaded files %q: %w", downloadedPath, err)
	}
	return nil
}

func (q *Queue) wrapCleanupWithFileDelete(cleanup func(t *storage.Entry) error) func(*storage.Entry) error {
	return func(entry *storage.Entry) error {
		if err := q.deleteEntryFiles(entry); err != nil {
			return err
		}
		if cleanup != nil {
			return cleanup(entry)
		}
		return nil
	}
}

// Delete removes an entry from the queue, first cancelling any download that
// is still running for it.
func (q *Queue) Delete(infohash string, deleteFiles bool, cleanup func(t *storage.Entry) error) error {
	q.cancelInflight(infohash)
	return q.remove(infohash, deleteFiles, cleanup)
}

// remove deletes an entry without cancelling running work. It is for the
// entry's own worker, which would otherwise wait on itself.
func (q *Queue) remove(infohash string, deleteFiles bool, cleanup func(t *storage.Entry) error) error {
	if deleteFiles {
		cleanup = q.wrapCleanupWithFileDelete(cleanup)
	}
	return q.storage.DeleteQueued(infohash, cleanup)
}

func (q *Queue) DeleteWhere(category string, protocol config.Protocol, state storage.TorrentState, hashes []string, cleanup func(t *storage.Entry) error) error {
	return q.storage.DeleteWhereQueued(q.ListFilterFunc(category, protocol, state, hashes), q.withCancelInflight(q.wrapCleanupWithFileDelete(cleanup)))
}

func (q *Queue) DeleteStalled() error {
	cutoff := time.Now().Add(-q.removeStalledAfter)
	return q.storage.DeleteWhereQueued(func(t *storage.Entry) bool {
		if !t.AddedOn.Before(cutoff) {
			return false
		}
		if t.Status == debridTypes.TorrentStatusQueued {
			return false
		}
		// Torrent entries: not downloading, no seeders, no progress
		if t.Status != debridTypes.TorrentStatusDownloading && t.Seeders == 0 && t.Progress == 0 {
			return true
		}
		// NZB entries stuck in error state with no progress
		if t.State == storage.EntryStateError && t.Progress == 0 {
			return true
		}
		return false
	}, q.withCancelInflight(nil))
}

func (q *Queue) Update(torrent *storage.Entry) error {
	// A cancelled worker for a deleted entry must not write it back.
	if q.isDeleted(torrent.InfoHash) {
		return errEntryDeleted
	}
	return q.storage.UpdateQueue(torrent)
}

func (q *Queue) ListFilterFunc(category string, protocol config.Protocol, state storage.TorrentState, hashes []string) func(*storage.Entry) bool {
	hashSet := make(map[string]struct{}, len(hashes))
	if len(hashes) > 0 {
		for _, h := range hashes {
			hashSet[strings.ToLower(h)] = struct{}{}
		}
	}

	var filterFunc func(*storage.Entry) bool
	if category != "" || len(hashes) != 0 || state != "" || protocol != config.ProtocolAll {
		filterFunc = func(t *storage.Entry) bool {
			if category != "" && t.Category != category {
				return false
			}
			if state != "" && t.State != state {
				return false
			}
			if len(hashSet) > 0 {
				if _, ok := hashSet[strings.ToLower(t.InfoHash)]; !ok {
					return false
				}
			}
			if protocol != config.ProtocolAll && t.Protocol != protocol {
				return false
			}
			return true
		}
	}
	return filterFunc
}

func (q *Queue) ListFilter(category string, protocol config.Protocol, state storage.TorrentState, hashes []string, sortBy string, reverse bool) ([]*storage.Entry, error) {
	filterFunc := q.ListFilterFunc(category, protocol, state, hashes)
	torrents, err := q.storage.FilterQueued(filterFunc)
	if err != nil {
		return nil, err
	}

	if sortBy != "" {
		slices.SortFunc(torrents, func(a, b *storage.Entry) int {
			if !reverse {
				a, b = b, a
			}
			switch sortBy {
			case "name":
				return cmp.Compare(a.Name, b.Name)
			case "size":
				return cmp.Compare(a.Size, b.Size)
			case "completed", "downloaded":
				var left, right time.Time
				if a.CompletedAt != nil {
					left = *a.CompletedAt
				}
				if b.CompletedAt != nil {
					right = *b.CompletedAt
				}
				return left.Compare(right)
			case "progress":
				return cmp.Compare(a.Progress, b.Progress)
			case "category":
				return cmp.Compare(a.Category, b.Category)
			case "seeders":
				return cmp.Compare(a.Seeders, b.Seeders)
			default:
				return a.AddedOn.Compare(b.AddedOn)
			}
		})
	}
	return torrents, nil
}

func (q *Queue) UpdateWhere(predicate func(*storage.Entry) bool, updateFunc func(*storage.Entry) bool) error {
	return q.storage.UpdateWhereQueued(predicate, updateFunc)
}
