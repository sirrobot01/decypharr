package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type ImportType string

const (
	ImportTypeQBitTorrent ImportType = "qbit"
	ImportTypeAPI         ImportType = "api"
)

type ImportRequest struct {
	DownloadFolder   string        `json:"downloadFolder"`
	SelectedDebrid   string        `json:"debrid"`
	Magnet           *utils.Magnet `json:"magnet"`
	Arr              *arr.Arr      `json:"arr"`
	IsSymlink        bool          `json:"isSymlink"`
	DownloadUncached bool          `json:"downloadUncached"`
	CallBackUrl      string        `json:"callBackUrl"`

	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
	Error       error     `json:"error,omitempty"`

	Type  ImportType `json:"type"`
	Async bool       `json:"async"`
}

func NewImportRequest(debrid string, downloadFolder string, magnet *utils.Magnet, arr *arr.Arr, isSymlink, downloadUncached bool, callBackUrl string, importType ImportType) *ImportRequest {
	return &ImportRequest{
		Status:           "started",
		DownloadFolder:   downloadFolder,
		SelectedDebrid:   debrid,
		Magnet:           magnet,
		Arr:              arr,
		IsSymlink:        isSymlink,
		DownloadUncached: downloadUncached,
		CallBackUrl:      callBackUrl,
		Type:             importType,
	}
}

type importResponse struct {
	Status      string               `json:"status"`
	CompletedAt time.Time            `json:"completedAt"`
	Error       error                `json:"error"`
	Torrent     *Torrent             `json:"torrent"`
	Debrid      *debridTypes.Torrent `json:"debrid"`
}

func (i *ImportRequest) sendCallback(torrent *Torrent, debridTorrent *debridTypes.Torrent) {
	if i.CallBackUrl == "" {
		return
	}

	// Check if the callback URL is valid
	if _, err := url.ParseRequestURI(i.CallBackUrl); err != nil {
		return
	}

	client := request.New()
	payload, err := json.Marshal(&importResponse{
		Status:      i.Status,
		Error:       i.Error,
		CompletedAt: i.CompletedAt,
		Torrent:     torrent,
		Debrid:      debridTorrent,
	})
	if err != nil {
		return
	}
	req, err := http.NewRequest("POST", i.CallBackUrl, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	_, _ = client.Do(req)

}

func (i *ImportRequest) markAsFailed(err error, torrent *Torrent, debridTorrent *debridTypes.Torrent) {
	i.Status = "failed"
	i.Error = err
	i.CompletedAt = time.Now()
	i.sendCallback(torrent, debridTorrent)
}

func (i *ImportRequest) markAsCompleted(torrent *Torrent, debridTorrent *debridTypes.Torrent) {
	i.Status = "completed"
	i.Error = nil
	i.CompletedAt = time.Now()
	i.sendCallback(torrent, debridTorrent)
}

type ImportQueue struct {
	queue    map[string]chan *ImportRequest // Map to hold queues for different debrid services
	mu       sync.RWMutex                   // Mutex to protect access to the queue map
	ctx      context.Context
	cancel   context.CancelFunc
	capacity int // Capacity of each channel in the queue
}

func NewImportQueue(ctx context.Context, capacity int) *ImportQueue {
	ctx, cancel := context.WithCancel(ctx)
	return &ImportQueue{
		queue:    make(map[string]chan *ImportRequest),
		ctx:      ctx,
		cancel:   cancel,
		capacity: capacity,
	}
}

func (iq *ImportQueue) Push(req *ImportRequest) error {
	if req == nil {
		return fmt.Errorf("import request cannot be nil")
	}

	iq.mu.Lock()
	defer iq.mu.Unlock()

	if _, exists := iq.queue[req.SelectedDebrid]; !exists {
		iq.queue[req.SelectedDebrid] = make(chan *ImportRequest, iq.capacity) // Create a new channel for the debrid service
	}

	select {
	case iq.queue[req.SelectedDebrid] <- req:
		return nil
	case <-iq.ctx.Done():
		return fmt.Errorf("retry queue is shutting down")
	}
}

func (iq *ImportQueue) TryPop(selectedDebrid string) (*ImportRequest, error) {
	iq.mu.RLock()
	defer iq.mu.RUnlock()

	if ch, exists := iq.queue[selectedDebrid]; exists {
		select {
		case req := <-ch:
			return req, nil
		case <-iq.ctx.Done():
			return nil, fmt.Errorf("queue is shutting down")
		default:
			return nil, fmt.Errorf("no import request available for %s", selectedDebrid)
		}
	}
	return nil, fmt.Errorf("no queue exists for %s", selectedDebrid)
}

func (iq *ImportQueue) Size(selectedDebrid string) int {
	iq.mu.RLock()
	defer iq.mu.RUnlock()

	if ch, exists := iq.queue[selectedDebrid]; exists {
		return len(ch)
	}
	return 0
}

func (iq *ImportQueue) Close() {
	iq.cancel()
	iq.mu.Lock()
	defer iq.mu.Unlock()

	for _, ch := range iq.queue {
		// Drain remaining items before closing
		for {
			select {
			case <-ch:
				// Discard remaining items
			default:
				close(ch)
				goto nextChannel
			}
		}
	nextChannel:
	}
	iq.queue = make(map[string]chan *ImportRequest)
}
