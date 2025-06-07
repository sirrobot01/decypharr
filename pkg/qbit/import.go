package qbit

import (
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid"
	"github.com/sirrobot01/decypharr/pkg/service"

	"github.com/google/uuid"
	"github.com/sirrobot01/decypharr/pkg/arr"
)

type ImportRequest struct {
	ID               string        `json:"id"`
	Path             string        `json:"path"`
	Magnet           *utils.Magnet `json:"magnet"`
	Arr              *arr.Arr      `json:"arr"`
	IsSymlink        bool          `json:"isSymlink"`
	SeriesId         int           `json:"series"`
	Seasons          []int         `json:"seasons"`
	Episodes         []string      `json:"episodes"`
	DownloadUncached bool          `json:"downloadUncached"`
	SkipExisting     bool          `json:"skipExisting"`

	Failed      bool      `json:"failed"`
	FailedAt    time.Time `json:"failedAt"`
	Reason      string    `json:"reason"`
	Completed   bool      `json:"completed"`
	CompletedAt time.Time `json:"completedAt"`
	Async       bool      `json:"async"`
}

type ManualImportResponseSchema struct {
	Priority            string    `json:"priority"`
	Status              string    `json:"status"`
	Result              string    `json:"result"`
	Queued              time.Time `json:"queued"`
	Trigger             string    `json:"trigger"`
	SendUpdatesToClient bool      `json:"sendUpdatesToClient"`
	UpdateScheduledTask bool      `json:"updateScheduledTask"`
	Id                  int       `json:"id"`
}

func NewImportRequest(magnet *utils.Magnet, arr *arr.Arr, isSymlink, downloadUncached, skipExisting bool) *ImportRequest {
	return &ImportRequest{
		ID:               uuid.NewString(),
		Magnet:           magnet,
		Arr:              arr,
		Failed:           false,
		Completed:        false,
		Async:            false,
		IsSymlink:        isSymlink,
		DownloadUncached: downloadUncached,
		SkipExisting:     skipExisting,
	}
}

func (i *ImportRequest) Fail(reason string) {
	i.Failed = true
	i.FailedAt = time.Now()
	i.Reason = reason
}

func (i *ImportRequest) Complete() {
	i.Completed = true
	i.CompletedAt = time.Now()
}

func (i *ImportRequest) Process(q *QBit) (err error) {
	// Use this for now.
	// This sends the torrent to the arr
	svc := service.GetService()

	// Check if we should skip existing torrents
	if i.SkipExisting {
		if exists, err := i.checkTorrentExists(svc.Debrid); err != nil {
			return err
		} else if exists {
			i.Reason = "Torrent already exists in debrid provider"
			i.Complete() // Mark as completed since we're skipping it
			return nil   // Skip this torrent, but don't fail the import
		}
	}

	torrent := createTorrentFromMagnet(i.Magnet, i.Arr.Name, "manual")
	debridTorrent, err := debrid.ProcessTorrent(svc.Debrid, i.Magnet, i.Arr, i.IsSymlink, i.DownloadUncached)
	if err != nil {
		return err
	}
	torrent = q.UpdateTorrentMin(torrent, debridTorrent)
	q.Storage.AddOrUpdate(torrent)
	go q.ProcessFiles(torrent, debridTorrent, i.Arr, i.IsSymlink)
	return nil
}

// checkTorrentExists checks if a torrent with the same InfoHash already exists in any debrid provider
func (i *ImportRequest) checkTorrentExists(engine *debrid.Engine) (bool, error) {
	if i.Magnet == nil || i.Magnet.InfoHash == "" {
		return false, nil
	}

	targetHash := strings.ToLower(i.Magnet.InfoHash)

	// Check all debrid clients
	for _, client := range engine.Clients {
		torrents, err := client.GetTorrents()
		if err != nil {
			// Log the error but continue checking other clients
			logger := client.GetLogger()
			logger.Warn().Err(err).Msgf("Failed to get torrents from %s", client.GetName())
			continue
		}

		// Check if any existing torrent has the same hash
		for _, torrent := range torrents {
			if torrent.InfoHash != "" && strings.ToLower(torrent.InfoHash) == targetHash {
				logger := client.GetLogger()
				logger.Info().Msgf("Torrent with hash %s already exists in %s", targetHash, client.GetName())
				return true, nil
			}
		}
	}

	return false, nil
}
