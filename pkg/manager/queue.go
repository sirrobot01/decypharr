package manager

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
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

func (q *Queue) Delete(infohash string, deleteFiles bool, cleanup func(t *storage.Entry) error) error {
	if deleteFiles {
		cleanup = q.wrapCleanupWithFileDelete(cleanup)
	}
	return q.storage.DeleteQueued(infohash, cleanup)
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
