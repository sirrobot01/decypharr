package alldebrid

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"
)

type AllDebrid struct {
	Host                  string `json:"host"`
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration
	client                *request.Client
	Profile               *types.Profile `json:"profile"`
	logger                zerolog.Logger
	config                config.Debrid
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*AllDebrid, error) {
	cfg := config.Get()
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	}
	_log := logger.New(dc.Name)

	opts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithMaxRetries(cfg.Retries),
		request.WithRetryableStatus(http.StatusTooManyRequests, http.StatusBadGateway),
	}
	if dc.Proxy != "" {
		opts = append(opts, request.WithProxy(dc.Proxy))
	}

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}
	ad := &AllDebrid{
		Host:                  "http://api.alldebrid.com/v4.1",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		logger:                _log,
		config:                dc,
	}
	return ad, nil
}

func (ad *AllDebrid) Config() config.Debrid {
	return ad.config
}

func (ad *AllDebrid) Logger() zerolog.Logger {
	return ad.logger
}

func (ad *AllDebrid) doAccountRequest(account *account.Account, endpoint string, queryParams map[string]string, result interface{}) (*http.Response, error) {
	u, err := url.Parse(ad.Host + endpoint)
	if err != nil {
		return nil, err
	}

	if queryParams != nil {
		q := u.Query()
		for k, v := range queryParams {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := account.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if result != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength != 0 {
		if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp, err
		}
	}

	return resp, nil
}

// doRequest performs a GET request and unmarshals the response
func (ad *AllDebrid) doRequest(endpoint string, queryParams map[string]string, result interface{}) (*http.Response, error) {
	u, err := url.Parse(ad.Host + endpoint)
	if err != nil {
		return nil, err
	}

	if queryParams != nil {
		q := u.Query()
		for k, v := range queryParams {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := ad.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if result != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength != 0 {
		if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(result); err != nil {
			return resp, err
		}
	}

	return resp, nil
}

func (ad *AllDebrid) IsAvailable(hashes []string) map[string]bool {
	result := make(map[string]bool)
	// AllDebrid does not support checking cached infohashes
	return result
}

func (ad *AllDebrid) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	var data UploadMagnetResponse

	resp, err := ad.doRequest("/magnet/upload", map[string]string{"magnets[]": torrent.Magnet.Link}, &data)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	magnets := data.Data.Magnets
	if len(magnets) == 0 {
		return nil, fmt.Errorf("error adding torrent. No magnets returned")
	}
	magnet := magnets[0]
	torrentId := strconv.Itoa(magnet.ID)
	torrent.Id = torrentId
	torrent.Added = time.Now()

	return torrent, nil
}

func getAlldebridStatus(statusCode int) types.TorrentStatus {
	switch {
	case statusCode == 4:
		return types.TorrentStatusDownloaded
	case statusCode >= 0 && statusCode <= 3:
		return types.TorrentStatusDownloading
	default:
		return types.TorrentStatusError
	}
}

func (ad *AllDebrid) flattenFiles(torrentId string, files []MagnetFile, parentPath string, index *int) map[string]types.File {
	result := make(map[string]types.File)

	cfg := config.Get()

	for _, f := range files {
		currentPath := f.Name
		if parentPath != "" {
			currentPath = filepath.Join(parentPath, f.Name)
		}

		if f.Elements != nil {
			subFiles := ad.flattenFiles(torrentId, f.Elements, currentPath, index)
			for k, v := range subFiles {
				if _, ok := result[k]; ok {
					result[v.Path] = v
				} else {
					result[k] = v
				}
			}
		} else {
			fileName := filepath.Base(f.Name)

			if err := cfg.IsFileAllowed(f.Name, f.Size); err != nil {
				continue
			}

			*index++
			file := types.File{
				TorrentId: torrentId,
				Id:        strconv.Itoa(*index),
				Name:      fileName,
				Size:      f.Size,
				Path:      currentPath,
				Link:      f.Link,
			}
			result[file.Name] = file
		}
	}

	return result
}

func (ad *AllDebrid) GetTorrent(torrentId string) (*types.Torrent, error) {
	var res TorrentInfoResponse

	resp, err := ad.doRequest("/magnet/status", map[string]string{"id": torrentId}, &res)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	data := res.Data.Magnets
	status := getAlldebridStatus(data.StatusCode)
	name := data.Filename
	t := &types.Torrent{
		Id:               strconv.Itoa(data.Id),
		Name:             name,
		Status:           status,
		Filename:         name,
		OriginalFilename: name,
		Files:            make(map[string]types.File),
		InfoHash:         data.Hash,
		Debrid:           ad.config.Name,
		Added:            time.Unix(data.CompletionDate, 0),
	}
	t.Bytes = data.Size
	t.Seeders = data.Seeders
	if status == "downloaded" {
		t.Progress = 100
		index := -1
		files := ad.flattenFiles(t.Id, data.Files, "", &index)
		t.Files = files
	} else {
		if data.Size > 0 {
			t.Progress = float64(data.Downloaded) / float64(data.Size) * 100
		}
		t.Speed = data.DownloadSpeed
	}
	return t, nil
}

func (ad *AllDebrid) UpdateTorrent(t *types.Torrent) error {
	var res TorrentInfoResponse

	resp, err := ad.doRequest("/magnet/status", map[string]string{"id": t.Id}, &res)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	data := res.Data.Magnets
	status := getAlldebridStatus(data.StatusCode)
	name := data.Filename
	t.Name = name
	t.Status = status
	t.Filename = name
	t.OriginalFilename = name
	t.Debrid = ad.config.Name
	t.Bytes = data.Size
	t.Seeders = data.Seeders
	if data.Hash != "" {
		t.InfoHash = data.Hash
	}
	t.Added = time.Unix(data.CompletionDate, 0)
	if status == "downloaded" {
		t.Progress = 100
		index := -1
		files := ad.flattenFiles(t.Id, data.Files, "", &index)
		t.Files = files
	} else {
		if data.Size > 0 {
			t.Progress = float64(data.Downloaded) / float64(data.Size) * 100
		}
		t.Speed = data.DownloadSpeed
	}
	return nil
}

func (ad *AllDebrid) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	for {
		err := ad.UpdateTorrent(torrent)

		if err != nil || torrent == nil {
			return torrent, err
		}
		switch torrent.Status {
		case types.TorrentStatusDownloaded:
			ad.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			return torrent, nil
		case types.TorrentStatusDownloading:
			if !torrent.DownloadUncached {
				return torrent, fmt.Errorf("torrent: %s not cached", torrent.Name)
			}
			return torrent, nil
		case types.TorrentStatusError:
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		default:
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		}
	}
}

func (ad *AllDebrid) DeleteTorrent(torrentId string) error {
	resp, err := ad.doRequest("/magnet/delete", map[string]string{"id": torrentId}, nil)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	ad.logger.Info().Msgf("Torrent %s deleted from AD", torrentId)
	return nil
}

func (ad *AllDebrid) fetchDownloadLink(account *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	var data DownloadLink

	resp, err := ad.doAccountRequest(account, "/link/unlock", map[string]string{"link": file.Link}, &data)
	if err != nil {
		return types.DownloadLink{}, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return types.DownloadLink{}, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	if data.Error != nil {
		return types.DownloadLink{}, fmt.Errorf("error getting download link: %s", data.Error.Message)
	}
	link := data.Data.Link
	if link == "" {
		return types.DownloadLink{}, fmt.Errorf("download link is empty")
	}
	now := time.Now()
	dl := types.DownloadLink{
		Debrid:       ad.config.Name,
		Token:        ad.APIKey,
		Link:         file.Link,
		DownloadLink: link,
		Id:           data.Data.Id,
		Size:         file.Size,
		Filename:     file.Name,
		Generated:    now,
		ExpiresAt:    now.Add(ad.autoExpiresLinksAfter),
	}
	return dl, nil
}

func (ad *AllDebrid) GetDownloadLink(id string, file *types.File) (types.DownloadLink, error) {
	return ad.accountsManager.GetDownloadLink(id, file, ad.fetchDownloadLink)
}

func (ad *AllDebrid) GetTorrents() ([]*types.Torrent, error) {
	torrents := make([]*types.Torrent, 0)
	var res TorrentsListResponse

	resp, err := ad.doRequest("/magnet/status", map[string]string{"status": "ready"}, &res)
	if err != nil {
		return torrents, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return torrents, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	cfg := config.Get()

	for _, magnet := range res.Data.Magnets {
		t := &types.Torrent{
			Id:               strconv.Itoa(magnet.Id),
			Name:             magnet.Filename,
			Bytes:            magnet.Size,
			Status:           getAlldebridStatus(magnet.StatusCode),
			Filename:         magnet.Filename,
			OriginalFilename: magnet.Filename,
			Files:            make(map[string]types.File),
			InfoHash:         magnet.Hash,
			Debrid:           ad.config.Name,
			Added:            time.Unix(magnet.CompletionDate, 0),
		}
		for _, f := range magnet.Files {
			if err := cfg.IsFileAllowed(f.Name, f.Size); err != nil {
				continue
			}
			file := types.File{
				TorrentId: t.Id,
				Name:      f.Name,
				Size:      f.Size,
				Link:      f.Link,
			}
			t.Files[file.Name] = file
		}
		torrents = append(torrents, t)
	}

	return torrents, nil
}

func (ad *AllDebrid) fetchDownloadLinks(account *account.Account) ([]types.DownloadLink, error) {
	// AllDebrid does not support fetching all download links
	downloadLinks := make([]types.DownloadLink, 0)
	return downloadLinks, nil
}

func (ad *AllDebrid) RefreshDownloadLinks() error {
	return ad.accountsManager.RefreshLinks(ad.fetchDownloadLinks)
}

func (ad *AllDebrid) CheckFile(ctx context.Context, infohash, link string) error {
	return nil
}

func (ad *AllDebrid) GetAvailableSlots() (int, error) {
	// AllDebrid does not provide available slots info
	return config.DefaultAvailableSlots, nil
}

func (ad *AllDebrid) GetProfile() (*types.Profile, error) {
	if ad.Profile != nil {
		return ad.Profile, nil
	}
	var res UserProfileResponse

	resp, err := ad.doRequest("/user", nil, &res)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	if res.Status != "success" {
		message := "unknown error"
		if res.Error != nil {
			message = res.Error.Message
		}
		return nil, fmt.Errorf("error getting user profile: %s", message)
	}
	userData := res.Data.User
	expiration := time.Unix(userData.PremiumUntil, 0)
	profile := &types.Profile{
		Id:         1,
		Name:       ad.config.Name,
		Username:   userData.Username,
		Email:      userData.Email,
		Points:     userData.FidelityPoints,
		Premium:    userData.PremiumUntil,
		Expiration: expiration,
	}
	if userData.IsPremium {
		profile.Type = "premium"
	} else if userData.IsTrial {
		profile.Type = "trial"
	} else {
		profile.Type = "free"
	}
	ad.Profile = profile
	return profile, nil
}

func (ad *AllDebrid) AccountManager() *account.Manager {
	return ad.accountsManager
}

func (ad *AllDebrid) syncAccount(account *account.Account) error {
	return nil
}

func (ad *AllDebrid) SyncAccounts() {
	ad.accountsManager.Sync(ad.syncAccount)
}

func (ad *AllDebrid) deleteLink(account *account.Account, downloadLink types.DownloadLink) error {
	resp, err := ad.doAccountRequest(account, "/user/links/delete", map[string]string{"links": downloadLink.Link}, nil)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}
	return nil
}

func (ad *AllDebrid) DeleteLink(downloadLink types.DownloadLink) error {
	return ad.accountsManager.DeleteDownloadLink(downloadLink, ad.deleteLink)
}

// SpeedTest measures API latency and download speed using cached links
func (ad *AllDebrid) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: ad.config.Name,
		TestedAt: time.Now(),
	}

	start := time.Now()
	resp, err := ad.doRequest("/user", nil, nil)
	latency := time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("latency test failed: %v", err)
		return result
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("latency test unexpected status: %d", resp.StatusCode)
		return result
	}
	result.LatencyMs = latency.Milliseconds()

	// Try to measure download speed using a cached link
	current := ad.accountsManager.Current()
	if current == nil {
		return result // Latency only
	}

	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result // Latency only
	}

	// Download first 1MB to measure speed
	const downloadSize = 1 * 1024 * 1024 // 1MB
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.DownloadLink, nil)
	if err != nil {
		return result
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", downloadSize-1))

	downloadStart := time.Now()
	dlResp, err := current.Client().Do(req)
	if err != nil {
		return result
	}
	defer dlResp.Body.Close()

	data, err := io.ReadAll(dlResp.Body)
	downloadDuration := time.Since(downloadStart)

	if err != nil || len(data) == 0 {
		return result
	}

	result.BytesRead = int64(len(data))
	if downloadDuration.Seconds() > 0 {
		result.SpeedMBps = float64(result.BytesRead) / downloadDuration.Seconds() / (1024 * 1024)
	}

	return result
}

func (ad *AllDebrid) SupportsCheck() bool {
	return false
}
