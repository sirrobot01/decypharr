package manager

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"sort"
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
	"github.com/sirrobot01/decypharr/pkg/usenet/parser"
)

type ImportType string

const (
	ImportTypeQBit    ImportType = "qbit"
	ImportTypeAPI     ImportType = "api"
	ImportTypeSABnzbd ImportType = "sabnzbd"
	ImportSwitcher    ImportType = "switcher"
)

type ImportRequest struct {
	Name             string                `json:"name"`
	NZBContent       []byte                `json:"-"`
	Id               string                `json:"id"`
	DownloadFolder   string                `json:"downloadFolder"`
	SelectedDebrid   string                `json:"debrid"`
	Magnet           *utils.Magnet         `json:"magnet"`
	Arr              *arr.Arr              `json:"arr"`
	Action           config.DownloadAction `json:"action"`
	DownloadUncached *bool                 `json:"downloadUncached"`
	CallBackUrl      string                `json:"callBackUrl"`
	SkipMultiSeason  bool                  `json:"skip_multi_season"`

	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
	Error       string    `json:"error,omitempty"`

	Type  ImportType `json:"type"`
	Async bool       `json:"async"`
}

func NewTorrentRequest(debrid string, downloadFolder string, magnet *utils.Magnet, arr *arr.Arr, action config.DownloadAction, downloadUncached *bool, callBackUrl string, importType ImportType, skipMultiSeason bool) *ImportRequest {

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

func NewNZBRequest(name, downloadFolder string, nzbContent []byte, arr *arr.Arr, action config.DownloadAction, callBackUrl string, importType ImportType, skipMultiSeason bool) *ImportRequest {
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

	queue  []*ImportRequest
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	cond   *sync.Cond // For blocking operations
}

func newQueue(ctx context.Context, storage *storage.Storage, capacity int, removeStalledAfterStr string) *Queue {

	ctx, cancel := context.WithCancel(ctx)
	q := &Queue{
		storage: storage,
		queue:   make([]*ImportRequest, 0, capacity),
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger.New("queue"),
	}

	if removeStalledAfterStr != "" {
		removeStalledAfter, err := utils.ParseDuration(removeStalledAfterStr)
		if err == nil {
			q.removeStalledAfter = removeStalledAfter
		}
	}

	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *Queue) ReQueue(importReq *ImportRequest) error {
	if importReq.Magnet == nil {
		return fmt.Errorf("magnet is required")
	}

	if importReq.Arr == nil {
		return fmt.Errorf("arr is required")
	}

	importReq.Status = "queued"
	importReq.CompletedAt = time.Time{}
	importReq.Error = ""
	err := q.PushRequest(importReq)
	if err != nil {
		return err
	}
	return nil
}

func (q *Queue) Add(torrent *storage.Entry) error {
	return q.storage.AddQueue(torrent)
}

func (q *Queue) GetTorrent(infohash string) (*storage.Entry, error) {
	return q.storage.GetQueued(infohash)
}

func (q *Queue) deleteEntryFiles(entry *storage.Entry) {
	downloadedPath := entry.DownloadPath()
	if downloadedPath == "" {
		return
	}
	if err := os.RemoveAll(downloadedPath); err != nil {
		q.logger.Error().Err(err).Str("path", downloadedPath).Msg("Failed to delete downloaded file")
	}
}

func (q *Queue) wrapCleanupWithFileDelete(cleanup func(t *storage.Entry) error) func(*storage.Entry) error {
	return func(entry *storage.Entry) error {
		q.deleteEntryFiles(entry)
		if cleanup != nil {
			return cleanup(entry)
		}
		return nil
	}
}

func (q *Queue) Delete(infohash string, cleanup func(t *storage.Entry) error) error {
	return q.storage.DeleteQueued(infohash, q.wrapCleanupWithFileDelete(cleanup))
}

// DeleteEntryOnly removes a queued entry without deleting its downloaded files.
// Use this when the download client receives a remove-without-cleanup signal
// (e.g. qBittorrent deleteFiles=false) so locally downloaded files remain
// accessible for a subsequent import attempt.
func (q *Queue) DeleteEntryOnly(infohash string) error {
	return q.storage.DeleteQueued(infohash, nil)
}

func (q *Queue) DeleteWhere(category string, protocol config.Protocol, state storage.TorrentState, hashes []string, cleanup func(t *storage.Entry) error) error {
	return q.storage.DeleteWhereQueued(q.ListFilterFunc(category, protocol, state, hashes), q.wrapCleanupWithFileDelete(cleanup))
}

func (q *Queue) DeleteStalled() error {
	cutoff := time.Now().Add(-q.removeStalledAfter)
	return q.storage.DeleteWhereQueued(func(t *storage.Entry) bool {
		if !t.AddedOn.Before(cutoff) {
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
	}, nil)
}

func (q *Queue) Update(torrent *storage.Entry) error {
	// Update the state here
	return q.storage.UpdateQueue(torrent)
}

func (q *Queue) ListFilterFunc(category string, protocol config.Protocol, state storage.TorrentState, hashes []string) func(*storage.Entry) bool {
	hashSet := make(map[string]struct{}, len(hashes))
	if len(hashes) > 0 {
		for _, h := range hashes {
			hashSet[h] = struct{}{}
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
				if _, ok := hashSet[t.InfoHash]; !ok {
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

func (q *Queue) ListFilter(category string, protocol config.Protocol, state storage.TorrentState, hashes []string, sortBy string, reverse bool) []*storage.Entry {
	filterFunc := q.ListFilterFunc(category, protocol, state, hashes)
	torrents, err := q.storage.FilterQueued(filterFunc)
	if err != nil {
		// return empty list on error
		return []*storage.Entry{}
	}

	if sortBy != "" {
		sort.Slice(torrents, func(i, j int) bool {
			// If ascending is false, swap i and j to get descending order
			if !reverse {
				i, j = j, i
			}

			switch sortBy {
			case "name":
				return torrents[i].Name < torrents[j].Name
			case "size":
				return torrents[i].Size < torrents[j].Size
			case "added_on":
				return torrents[i].AddedOn.Before(torrents[j].AddedOn)
			case "completed", "downloaded":
				return torrents[i].CompletedAt.Before(*torrents[j].CompletedAt)
			case "progress":
				return torrents[i].Progress < torrents[j].Progress
			case "category":
				return torrents[i].Category < torrents[j].Category
			case "seeders":
				return torrents[i].Seeders < torrents[j].Seeders
			default:
				// Default sort by added_on
				return torrents[i].AddedOn.Before(torrents[j].AddedOn)
			}
		})
	}
	return torrents
}

func (q *Queue) UpdateWhere(predicate func(*storage.Entry) bool, updateFunc func(*storage.Entry) bool) error {
	return q.storage.UpdateWhereQueued(predicate, updateFunc)
}

func (q *Queue) PushRequest(req *ImportRequest) error {
	if req == nil {
		return fmt.Errorf("import request cannot be nil")
	}

	q.mu.Lock()
	defer q.mu.Unlock()

	select {
	case <-q.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
	}

	if len(q.queue) >= cap(q.queue) {
		return fmt.Errorf("queue is full")
	}

	q.queue = append(q.queue, req)
	q.cond.Signal() // Wake up any waiting Pop()
	return nil
}

func (q *Queue) PopRequest() (*ImportRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	select {
	case <-q.ctx.Done():
		return nil, fmt.Errorf("queue is shutting down")
	default:
	}

	if len(q.queue) == 0 {
		return nil, fmt.Errorf("no import requests available")
	}

	req := q.queue[0]
	q.queue = q.queue[1:]
	return req, nil
}

// DeleteRequest specific request by ID
func (q *Queue) DeleteRequest(requestID string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, req := range q.queue {
		if req.Id == requestID {
			// Remove from slice
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			return true
		}
	}
	return false
}

// DeleteRequestWhere requests matching a condition
func (q *Queue) DeleteRequestWhere(predicate func(*ImportRequest) bool) int {
	q.mu.Lock()
	defer q.mu.Unlock()

	deleted := 0
	for i := len(q.queue) - 1; i >= 0; i-- {
		if predicate == nil || predicate(q.queue[i]) {
			q.queue = append(q.queue[:i], q.queue[i+1:]...)
			deleted++
		}
	}
	return deleted
}

// FindRequest request without removing it
func (q *Queue) FindRequest(requestID string) *ImportRequest {
	q.mu.RLock()
	defer q.mu.RUnlock()

	for _, req := range q.queue {
		if req.Id == requestID {
			return req
		}
	}
	return nil
}

func (q *Queue) RequestsSize() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.queue)
}

func (q *Queue) IsEmpty() bool {
	return q.RequestsSize() == 0
}

func (q *Queue) Close() {
	q.cancel()
	q.cond.Broadcast()
}

// nzbJob represents a queued NZB processing job
type nzbJob struct {
	entry  *storage.Entry
	meta   *storage.NZB
	groups map[string]*parser.FileGroup
}

// nzbJobQueue is an unbounded, thread-safe job queue
type nzbJobQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	jobs   []*nzbJob
	closed bool
}

func newNzbJobQueue() *nzbJobQueue {
	q := &nzbJobQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// Push adds a job to the queue (never blocks)
func (q *nzbJobQueue) Push(job *nzbJob) {
	q.mu.Lock()
	q.jobs = append(q.jobs, job)
	q.mu.Unlock()
	q.cond.Signal() // Wake one waiting worker
}

// Pop removes and returns the next job, blocking if queue is empty
func (q *nzbJobQueue) Pop() (*nzbJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for len(q.jobs) == 0 && !q.closed {
		q.cond.Wait() // release lock and wait for signal
	}

	if q.closed && len(q.jobs) == 0 {
		return nil, false // Queue closed and empty
	}

	job := q.jobs[0]
	q.jobs = q.jobs[1:]
	return job, true
}

// Len returns current queue length
func (q *nzbJobQueue) Len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// Close signals all waiting workers to exit
func (q *nzbJobQueue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cond.Broadcast() // Wake all waiting workers
}
