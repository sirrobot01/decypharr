package debrid_link

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"strconv"
	"time"

	"net/http"
	"strings"
)

type DebridLink struct {
	Name             string
	Host             string `json:"host"`
	APIKey           string
	accounts         map[string]types.Account
	DownloadUncached bool
	client           *request.Client

	MountPath   string
	logger      zerolog.Logger
	checkCached bool
	addSamples  bool
}

func New(dc config.Debrid) (*DebridLink, error) {
	rl := request.ParseRateLimit(dc.RateLimit)

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"Content-Type":  "application/json",
	}
	_log := logger.New(dc.Name)
	client := request.New(
		request.WithHeaders(headers),
		request.WithLogger(_log),
		request.WithRateLimiter(rl),
		request.WithProxy(dc.Proxy),
	)

	accounts := make(map[string]types.Account)
	for idx, key := range dc.DownloadAPIKeys {
		id := strconv.Itoa(idx)
		accounts[id] = types.Account{
			Name:  key,
			ID:    id,
			Token: key,
		}
	}
	return &DebridLink{
		Name:             "debridlink",
		Host:             "https://debrid-link.com/api/v2",
		APIKey:           dc.APIKey,
		accounts:         accounts,
		DownloadUncached: dc.DownloadUncached,
		client:           client,
		MountPath:        dc.Folder,
		logger:           logger.New(dc.Name),
		checkCached:      dc.CheckCached,
		addSamples:       dc.AddSamples,
	}, nil
}

func (dl *DebridLink) GetProfile() (*types.Profile, error) {
	return nil, nil
}

func (dl *DebridLink) GetName() string {
	return dl.Name
}

func (dl *DebridLink) GetLogger() zerolog.Logger {
	return dl.logger
}

func (dl *DebridLink) IsAvailable(hashes []string) map[string]bool {
	// Check if the infohashes are available in the local cache
	result := make(map[string]bool)

	// Divide hashes into groups of 100
	for i := 0; i < len(hashes); i += 100 {
		end := i + 100
		if end > len(hashes) {
			end = len(hashes)
		}

		// Filter out empty strings
		validHashes := make([]string, 0, end-i)
		for _, hash := range hashes[i:end] {
			if hash != "" {
				validHashes = append(validHashes, hash)
			}
		}

		// If no valid hashes in this batch, continue to the next batch
		if len(validHashes) == 0 {
			continue
		}

		hashStr := strings.Join(validHashes, ",")
		url := fmt.Sprintf("%s/seedbox/cached/%s", dl.Host, hashStr)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := dl.client.MakeRequest(req)
		if err != nil {
			dl.logger.Info().Msgf("Error checking availability: %v", err)
			return result
		}
		var data AvailableResponse
		err = json.Unmarshal(resp, &data)
		if err != nil {
			dl.logger.Info().Msgf("Error marshalling availability: %v", err)
			return result
		}
		if data.Value == nil {
			return result
		}
		value := *data.Value
		for _, h := range hashes[i:end] {
			_, exists := value[h]
			if exists {
				result[h] = true
			}
		}
	}
	return result
}

func (dl *DebridLink) GetTorrent(torrentId string) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/%s", dl.Host, torrentId)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error getting torrent")
	}
	data := *res.Value

	if len(data) == 0 {
		return nil, fmt.Errorf("torrent not found")
	}
	t := data[0]
	name := utils.RemoveInvalidChars(t.Name)
	torrent := &types.Torrent{
		Id:               t.ID,
		Name:             name,
		Bytes:            t.TotalSize,
		Status:           "downloaded",
		Filename:         name,
		OriginalFilename: name,
		MountPath:        dl.MountPath,
		Debrid:           dl.Name,
		Added:            time.Unix(t.Created, 0).Format(time.RFC3339),
	}
	cfg := config.Get()
	for _, f := range t.Files {
		if !cfg.IsSizeAllowed(f.Size) {
			continue
		}
		file := types.File{
			TorrentId: t.ID,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			DownloadLink: &types.DownloadLink{
				Filename:     f.Name,
				Link:         f.DownloadURL,
				DownloadLink: f.DownloadURL,
				Generated:    time.Now(),
				AccountId:    "0",
			},
			Link: f.DownloadURL,
		}
		torrent.Files[file.Name] = file
	}

	return torrent, nil
}

func (dl *DebridLink) UpdateTorrent(t *types.Torrent) error {
	url := fmt.Sprintf("%s/seedbox/list?ids=%s", dl.Host, t.Id)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("error getting torrent")
	}
	if res.Value == nil {
		return fmt.Errorf("torrent not found")
	}
	dt := *res.Value

	if len(dt) == 0 {
		return fmt.Errorf("torrent not found")
	}
	data := dt[0]
	status := "downloading"
	if data.Status == 100 {
		status = "downloaded"
	}
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Folder = name
	t.Progress = data.DownloadPercent
	t.Status = status
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.Added = time.Unix(data.Created, 0).Format(time.RFC3339)
	cfg := config.Get()
	for _, f := range data.Files {
		if !cfg.IsSizeAllowed(f.Size) {
			continue
		}
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			DownloadLink: &types.DownloadLink{
				Filename:     f.Name,
				Link:         f.DownloadURL,
				DownloadLink: f.DownloadURL,
				Generated:    time.Now(),
				AccountId:    "0",
			},
			Link: f.DownloadURL,
		}
		t.Files[f.Name] = file
	}
	return nil
}

func (dl *DebridLink) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/add", dl.Host)
	payload := map[string]string{"url": t.Magnet.Link}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewBuffer(jsonPayload))
	resp, err := dl.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var res SubmitTorrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	data := *res.Value
	status := "downloading"
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Folder = name
	t.Progress = data.DownloadPercent
	t.Status = status
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.MountPath = dl.MountPath
	t.Debrid = dl.Name
	t.Added = time.Unix(data.Created, 0).Format(time.RFC3339)
	for _, f := range data.Files {
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
			DownloadLink: &types.DownloadLink{
				Filename:     f.Name,
				Link:         f.DownloadURL,
				DownloadLink: f.DownloadURL,
				Generated:    time.Now(),
				AccountId:    "0",
			},
			Generated: time.Now(),
		}
		t.Files[f.Name] = file
	}

	return t, nil
}

func (dl *DebridLink) CheckStatus(torrent *types.Torrent, isSymlink bool) (*types.Torrent, error) {
	for {
		err := dl.UpdateTorrent(torrent)
		if err != nil || torrent == nil {
			return torrent, err
		}
		status := torrent.Status
		if status == "downloaded" {
			dl.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			err = dl.GenerateDownloadLinks(torrent)
			if err != nil {
				return torrent, err
			}
			break
		} else if utils.Contains(dl.GetDownloadingStatus(), status) {
			if !torrent.DownloadUncached {
				return torrent, fmt.Errorf("torrent: %s not cached", torrent.Name)
			}
			// Break out of the loop if the torrent is downloading.
			// This is necessary to prevent infinite loop since we moved to sync downloading and async processing
			return torrent, nil
		} else {
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		}

	}
	return torrent, nil
}

func (dl *DebridLink) DeleteTorrent(torrentId string) error {
	url := fmt.Sprintf("%s/seedbox/%s/remove", dl.Host, torrentId)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	if _, err := dl.client.MakeRequest(req); err != nil {
		return err
	}
	dl.logger.Info().Msgf("Torrent: %s deleted from DebridLink", torrentId)
	return nil
}

func (dl *DebridLink) GenerateDownloadLinks(t *types.Torrent) error {
	// Download links are already generated
	return nil
}

func (dl *DebridLink) GetDownloads() (map[string]types.DownloadLink, error) {
	return nil, nil
}

func (dl *DebridLink) GetDownloadLink(t *types.Torrent, file *types.File) (*types.DownloadLink, error) {
	return file.DownloadLink, nil
}

func (dl *DebridLink) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

func (dl *DebridLink) GetCheckCached() bool {
	return dl.checkCached
}

func (dl *DebridLink) GetDownloadUncached() bool {
	return dl.DownloadUncached
}

func (dl *DebridLink) GetTorrents() ([]*types.Torrent, error) {
	page := 0
	perPage := 100
	torrents := make([]*types.Torrent, 0)
	for {
		t, err := dl.getTorrents(page, perPage)
		if err != nil {
			break
		}
		if len(t) == 0 {
			break
		}
		torrents = append(torrents, t...)
		page++
	}
	return torrents, nil
}

func (dl *DebridLink) getTorrents(page, perPage int) ([]*types.Torrent, error) {
	url := fmt.Sprintf("%s/seedbox/list?page=%d&perPage=%d", dl.Host, page, perPage)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := dl.client.MakeRequest(req)
	torrents := make([]*types.Torrent, 0)
	if err != nil {
		return torrents, err
	}
	var res torrentInfo
	err = json.Unmarshal(resp, &res)
	if err != nil {
		dl.logger.Info().Msgf("Error unmarshalling torrent info: %s", err)
		return torrents, err
	}

	data := *res.Value

	if len(data) == 0 {
		return torrents, nil
	}
	for _, t := range data {
		if t.Status != 100 {
			continue
		}
		torrent := &types.Torrent{
			Id:               t.ID,
			Name:             t.Name,
			Bytes:            t.TotalSize,
			Status:           "downloaded",
			Filename:         t.Name,
			OriginalFilename: t.Name,
			InfoHash:         t.HashString,
			Files:            make(map[string]types.File),
			Debrid:           dl.Name,
			MountPath:        dl.MountPath,
			Added:            time.Unix(t.Created, 0).Format(time.RFC3339),
		}
		cfg := config.Get()
		for _, f := range t.Files {
			if !cfg.IsSizeAllowed(f.Size) {
				continue
			}
			file := types.File{
				TorrentId: torrent.Id,
				Id:        f.ID,
				Name:      f.Name,
				Size:      f.Size,
				Path:      f.Name,
				DownloadLink: &types.DownloadLink{
					Filename:     f.Name,
					Link:         f.DownloadURL,
					DownloadLink: f.DownloadURL,
					Generated:    time.Now(),
					AccountId:    "0",
				},
				Link: f.DownloadURL,
			}
			torrent.Files[f.Name] = file
		}
		torrents = append(torrents, torrent)
	}
	return torrents, nil
}

func (dl *DebridLink) CheckLink(link string) error {
	return nil
}

func (dl *DebridLink) GetMountPath() string {
	return dl.MountPath
}

func (dl *DebridLink) DisableAccount(accountId string) {
}

func (dl *DebridLink) ResetActiveDownloadKeys() {
}

func (dl *DebridLink) DeleteDownloadLink(linkId string) error {
	return nil
}
