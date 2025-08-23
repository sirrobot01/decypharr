package wire

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
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
	Id               string        `json:"id"`
	DownloadFolder   string        `json:"downloadFolder"`
	SelectedDebrid   string        `json:"debrid"`
	Magnet           *utils.Magnet `json:"magnet"`
	Arr              *arr.Arr      `json:"arr"`
	Action           string        `json:"action"`
	DownloadUncached bool          `json:"downloadUncached"`
	CallBackUrl      string        `json:"callBackUrl"`

	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completedAt,omitempty"`
	Error       error     `json:"error,omitempty"`

	Type  ImportType `json:"type"`
	Async bool       `json:"async"`
}

func NewImportRequest(debrid string, downloadFolder string, magnet *utils.Magnet, arr *arr.Arr, action string, downloadUncached bool, callBackUrl string, importType ImportType) *ImportRequest {
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
	queue  []*ImportRequest
	mu     sync.RWMutex
	ctx    context.Context
	cancel context.CancelFunc
	cond   *sync.Cond // For blocking operations
}

func NewImportQueue(ctx context.Context, capacity int) *ImportQueue {
	ctx, cancel := context.WithCancel(ctx)
	iq := &ImportQueue{
		queue:  make([]*ImportRequest, 0, capacity),
		ctx:    ctx,
		cancel: cancel,
	}
	iq.cond = sync.NewCond(&iq.mu)
	return iq
}

func (iq *ImportQueue) Push(req *ImportRequest) error {
	if req == nil {
		return fmt.Errorf("import request cannot be nil")
	}

	iq.mu.Lock()
	defer iq.mu.Unlock()

	select {
	case <-iq.ctx.Done():
		return fmt.Errorf("queue is shutting down")
	default:
	}

	if len(iq.queue) >= cap(iq.queue) {
		return fmt.Errorf("queue is full")
	}

	iq.queue = append(iq.queue, req)
	iq.cond.Signal() // Wake up any waiting Pop()
	return nil
}

func (iq *ImportQueue) Pop() (*ImportRequest, error) {
	iq.mu.Lock()
	defer iq.mu.Unlock()

	select {
	case <-iq.ctx.Done():
		return nil, fmt.Errorf("queue is shutting down")
	default:
	}

	if len(iq.queue) == 0 {
		return nil, fmt.Errorf("no import requests available")
	}

	req := iq.queue[0]
	iq.queue = iq.queue[1:]
	return req, nil
}

// Delete specific request by ID
func (iq *ImportQueue) Delete(requestID string) bool {
	iq.mu.Lock()
	defer iq.mu.Unlock()

	for i, req := range iq.queue {
		if req.Id == requestID {
			// Remove from slice
			iq.queue = append(iq.queue[:i], iq.queue[i+1:]...)
			return true
		}
	}
	return false
}

// DeleteWhere requests matching a condition
func (iq *ImportQueue) DeleteWhere(predicate func(*ImportRequest) bool) int {
	iq.mu.Lock()
	defer iq.mu.Unlock()

	deleted := 0
	for i := len(iq.queue) - 1; i >= 0; i-- {
		if predicate(iq.queue[i]) {
			iq.queue = append(iq.queue[:i], iq.queue[i+1:]...)
			deleted++
		}
	}
	return deleted
}

// Find request without removing it
func (iq *ImportQueue) Find(requestID string) *ImportRequest {
	iq.mu.RLock()
	defer iq.mu.RUnlock()

	for _, req := range iq.queue {
		if req.Id == requestID {
			return req
		}
	}
	return nil
}

func (iq *ImportQueue) Size() int {
	iq.mu.RLock()
	defer iq.mu.RUnlock()
	return len(iq.queue)
}

func (iq *ImportQueue) IsEmpty() bool {
	return iq.Size() == 0
}

// List all requests (copy to avoid race conditions)
func (iq *ImportQueue) List() []*ImportRequest {
	iq.mu.RLock()
	defer iq.mu.RUnlock()

	result := make([]*ImportRequest, len(iq.queue))
	copy(result, iq.queue)
	return result
}

func (iq *ImportQueue) Close() {
	iq.cancel()
	iq.cond.Broadcast()
}
