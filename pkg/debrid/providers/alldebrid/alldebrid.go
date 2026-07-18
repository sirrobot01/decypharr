package alldebrid

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"
)

const (
	allDebridAPIHost                  = "https://api.alldebrid.com"
	allDebridMagnetUploadEndpoint     = "/v4/magnet/upload"
	allDebridMagnetUploadFileEndpoint = "/v4/magnet/upload/file"
	allDebridMagnetStatusEndpoint     = "/v4.1/magnet/status"
	allDebridMagnetDeleteEndpoint     = "/v4/magnet/delete"
	allDebridLinkUnlockEndpoint       = "/v4/link/unlock"
	allDebridLinkInfosEndpoint        = "/v4/link/infos"
	allDebridUserEndpoint             = "/v4/user"
	allDebridUserLinksDeleteEndpoint  = "/v4/user/links/delete"
)

type AllDebrid struct {
	Host                  string `json:"host"`
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration
	client                *request.Client
	repairClient          *request.Client
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
	repairOpts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["repair"]),
		request.WithMaxRetries(4),
		request.WithRetryableStatus(http.StatusTooManyRequests),
	}
	if dc.Proxy != "" {
		repairOpts = append(repairOpts, request.WithProxy(dc.Proxy))
	}

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}
	ad := &AllDebrid{
		Host:                  allDebridAPIHost,
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		repairClient:          request.New(repairOpts...),
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

func newAllDebridAPIError(apiErr *errorResponse) error {
	if apiErr == nil {
		return fmt.Errorf("alldebrid API error: provider returned an error")
	}

	switch {
	case apiErr.Code != "" && apiErr.Message != "":
		return fmt.Errorf("alldebrid API error: %s: %s", apiErr.Code, apiErr.Message)
	case apiErr.Code != "":
		return fmt.Errorf("alldebrid API error: %s", apiErr.Code)
	case apiErr.Message != "":
		return fmt.Errorf("alldebrid API error: %s", apiErr.Message)
	default:
		return fmt.Errorf("alldebrid API error: provider returned an error")
	}
}

func decodeAllDebridResponse(resp *http.Response, result any) error {
	if resp == nil {
		return fmt.Errorf("alldebrid API error: empty HTTP response")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("alldebrid API error: reading response: %w", err)
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		if resp.StatusCode == http.StatusNoContent {
			return nil
		}
		if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
			return fmt.Errorf("alldebrid API error: HTTP status %d", resp.StatusCode)
		}
		return fmt.Errorf("alldebrid API error: empty response")
	}

	var envelope apiResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("alldebrid API error: decoding response: %w", err)
	}
	if envelope.Status == "error" || envelope.Error != nil {
		return newAllDebridAPIError(envelope.Error)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("alldebrid API error: HTTP status %d", resp.StatusCode)
	}
	if envelope.Status != "success" {
		return fmt.Errorf("alldebrid API error: unexpected response status %q", envelope.Status)
	}
	if result != nil {
		if err := json.Unmarshal(body, result); err != nil {
			return fmt.Errorf("alldebrid API error: decoding response data: %w", err)
		}
	}
	return nil
}

func (ad *AllDebrid) doAccountRequest(account *account.Account, endpoint string, queryParams map[string]string, result any) (*http.Response, error) {
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

	if err := decodeAllDebridResponse(resp, result); err != nil {
		return resp, err
	}

	return resp, nil
}

// doRequest performs a GET request and unmarshals the response
func (ad *AllDebrid) doRequest(endpoint string, queryParams map[string]string, result any) (*http.Response, error) {
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

	if err := decodeAllDebridResponse(resp, result); err != nil {
		return resp, err
	}

	return resp, nil
}

func (ad *AllDebrid) IsAvailable(hashes []string) map[string]bool {
	result := make(map[string]bool)
	// AllDebrid does not support checking cached infohashes
	return result
}

func (ad *AllDebrid) doPostFile(endpoint string, fileData []byte, result any) (*http.Response, error) {
	u, err := url.Parse(ad.Host + endpoint)
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("files[]", "torrent.torrent")
	if err != nil {
		return nil, err
	}
	if _, err = part.Write(fileData); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, u.String(), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := ad.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if err := decodeAllDebridResponse(resp, result); err != nil {
		return resp, err
	}
	return resp, nil
}

func (ad *AllDebrid) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	if torrent.Magnet.IsTorrent() {
		return ad.addTorrentFile(torrent)
	}
	return ad.addMagnetLink(torrent)
}

func (ad *AllDebrid) addTorrentFile(torrent *types.Torrent) (*types.Torrent, error) {
	var data UploadFileResponse

	_, err := ad.doPostFile(allDebridMagnetUploadFileEndpoint, torrent.Magnet.File, &data)
	if err != nil {
		return nil, err
	}

	files := data.Data.Files
	if len(files) == 0 {
		return nil, fmt.Errorf("error adding torrent file: no files returned")
	}
	f := files[0]
	if f.Error != nil {
		return nil, fmt.Errorf("alldebrid file upload: %w", newAllDebridAPIError(f.Error))
	}
	torrent.Id = strconv.Itoa(f.ID)
	torrent.Added = time.Now()
	return torrent, nil
}

func (ad *AllDebrid) addMagnetLink(torrent *types.Torrent) (*types.Torrent, error) {
	var data UploadMagnetResponse

	_, err := ad.doRequest(allDebridMagnetUploadEndpoint, map[string]string{"magnets[]": torrent.Magnet.Link}, &data)
	if err != nil {
		return nil, err
	}

	magnets := data.Data.Magnets
	if len(magnets) == 0 {
		return nil, fmt.Errorf("error adding torrent. No magnets returned")
	}
	magnet := magnets[0]
	torrent.Id = strconv.Itoa(magnet.ID)
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

func magnetForID(res *MagnetStatusResponse, torrentID string) (magnetInfo, error) {
	if len(res.Data.Magnets) != 1 {
		return magnetInfo{}, fmt.Errorf(
			"alldebrid magnet status for ID %q: expected exactly one magnet, got %d",
			torrentID,
			len(res.Data.Magnets),
		)
	}

	requestedID, err := strconv.Atoi(torrentID)
	if err != nil {
		return magnetInfo{}, fmt.Errorf("alldebrid magnet status: invalid requested ID %q: %w", torrentID, err)
	}
	magnet := res.Data.Magnets[0]
	if magnet.Id != requestedID {
		return magnetInfo{}, fmt.Errorf(
			"alldebrid magnet status for ID %q returned magnet ID %d",
			torrentID,
			magnet.Id,
		)
	}
	return magnet, nil
}

func (ad *AllDebrid) applyMagnetInfo(t *types.Torrent, data magnetInfo) {
	status := getAlldebridStatus(data.StatusCode)
	name := data.Filename
	if t.Files == nil {
		t.Files = make(map[string]types.File)
	}
	t.Id = strconv.Itoa(data.Id)
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
	if status == types.TorrentStatusDownloaded {
		t.Progress = 100
		index := -1
		t.Files = ad.flattenFiles(t.Id, data.Files, "", &index)
	} else {
		if data.Size > 0 {
			t.Progress = float64(data.Downloaded) / float64(data.Size) * 100
		}
		t.Speed = data.DownloadSpeed
	}
}

func (ad *AllDebrid) GetTorrent(torrentId string) (*types.Torrent, error) {
	var res MagnetStatusResponse

	_, err := ad.doRequest(allDebridMagnetStatusEndpoint, map[string]string{"id": torrentId}, &res)
	if err != nil {
		return nil, err
	}

	data, err := magnetForID(&res, torrentId)
	if err != nil {
		return nil, err
	}
	t := &types.Torrent{Files: make(map[string]types.File)}
	ad.applyMagnetInfo(t, data)
	return t, nil
}

func (ad *AllDebrid) UpdateTorrent(t *types.Torrent) error {
	if t == nil {
		return fmt.Errorf("alldebrid magnet status: cannot update a nil torrent")
	}
	var res MagnetStatusResponse

	_, err := ad.doRequest(allDebridMagnetStatusEndpoint, map[string]string{"id": t.Id}, &res)
	if err != nil {
		return err
	}

	data, err := magnetForID(&res, t.Id)
	if err != nil {
		return err
	}
	ad.applyMagnetInfo(t, data)
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
	_, err := ad.doRequest(allDebridMagnetDeleteEndpoint, map[string]string{"id": torrentId}, nil)
	if err != nil {
		return err
	}

	ad.logger.Info().Msgf("Torrent %s deleted from AD", torrentId)
	return nil
}

func (ad *AllDebrid) fetchDownloadLink(account *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	var data DownloadLink

	_, err := ad.doAccountRequest(account, allDebridLinkUnlockEndpoint, map[string]string{"link": file.Link}, &data)
	if err != nil {
		return types.DownloadLink{}, err
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
	var res MagnetStatusResponse

	_, err := ad.doRequest(allDebridMagnetStatusEndpoint, map[string]string{"status": "ready"}, &res)
	if err != nil {
		return torrents, err
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

func (ad *AllDebrid) CheckFile(ctx context.Context, _, link string) error {
	form := url.Values{}
	form.Add("link[]", link)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ad.Host+allDebridLinkInfosEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ad.repairClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var data LinkInfosResponse
	if err := decodeAllDebridResponse(resp, &data); err != nil {
		return err
	}
	if len(data.Data.Infos) != 1 {
		return fmt.Errorf("alldebrid API error: expected one link info, got %d", len(data.Data.Infos))
	}
	if linkErr := data.Data.Infos[0].Error; linkErr != nil {
		return fmt.Errorf("%w: %s: %s", customerror.HosterUnavailableError, linkErr.Code, linkErr.Message)
	}
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

	_, err := ad.doRequest(allDebridUserEndpoint, nil, &res)
	if err != nil {
		return nil, err
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
	_, err := ad.doAccountRequest(account, allDebridUserLinksDeleteEndpoint, map[string]string{"links": downloadLink.Link}, nil)
	if err != nil {
		return err
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
	_, err := ad.doRequest(allDebridUserEndpoint, nil, nil)
	latency := time.Since(start)

	if err != nil {
		result.Error = fmt.Sprintf("latency test failed: %v", err)
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
	return true
}
