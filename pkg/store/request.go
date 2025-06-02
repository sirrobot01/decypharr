package store

import (
	"bytes"
	"encoding/json"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"net/http"
	"net/url"
	"time"
)

type ImportType string

const (
	ImportTypeQBitTorrent ImportType = "qbit"
	ImportTypeAPI         ImportType = "api"
)

func NewImportRequest(debrid string, downloadFolder string, magnet *utils.Magnet, arr *arr.Arr, isSymlink, downloadUncached bool, callBackUrl string, importType ImportType) *ImportRequest {
	return &ImportRequest{
		Status:           "started",
		DownloadFolder:   downloadFolder,
		Debrid:           debrid,
		Magnet:           magnet,
		Arr:              arr,
		IsSymlink:        isSymlink,
		DownloadUncached: downloadUncached,
		CallBackUrl:      callBackUrl,
		Type:             importType,
	}
}

type ImportRequest struct {
	DownloadFolder   string        `json:"downloadFolder"`
	Debrid           string        `json:"debrid"`
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
