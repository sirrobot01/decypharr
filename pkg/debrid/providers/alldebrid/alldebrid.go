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

const allDebridNoPeerStatusCode = 7

type AllDebrid struct {
	Host                  string `json:"host"`
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration
	noPeerRetryBackoff    []time.Duration
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
		Host:                  "https://api.alldebrid.com/v4.1",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		noPeerRetryBackoff:    defaultNoPeerRetryBackoff(),
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
	defer request.DrainAndClose(resp.Body)

	if result != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength != 0 {
		if err := request.DecodeJSON(resp, result); err != nil {
			return resp, err
		}
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
	defer request.DrainAndClose(resp.Body)

	if result != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && resp.ContentLength != 0 {
		if err := request.DecodeJSON(resp, result); err != nil {
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
	writer.Close()

	req, err := http.NewRequest(http.MethodPost, u.String(), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := ad.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer request.DrainAndClose(resp.Body)

	if result != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := request.DecodeJSON(resp, result); err != nil {
			return resp, err
		}
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

	resp, err := ad.doPostFile("/magnet/upload/file", torrent.Magnet.File, &data)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	files := data.Data.Files
	if len(files) == 0 {
		return nil, fmt.Errorf("error adding torrent file: no files returned")
	}
	f := files[0]
	if f.Error != nil {
		return nil, fmt.Errorf("alldebrid file upload error: %s", f.Error.Message)
	}
	torrent.Id = strconv.Itoa(f.ID)
	torrent.Added = time.Now()
	return torrent, nil
}

func (ad *AllDebrid) addMagnetLink(torrent *types.Torrent) (*types.Torrent, error) {
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
	torrent.Id = strconv.Itoa(magnet.ID)
	torrent.Added = time.Now()
	return torrent, nil
}

func getAlldebridStatus(statusCode int) types.TorrentStatus {
	switch {
	case statusCode == 4:
		return types.TorrentStatusDownloaded
	case statusCode >= 0 && statusCode <= 3, statusCode == allDebridNoPeerStatusCode:
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

	data, err := findMagnet(res.Data.Magnets, torrentId)
	if err != nil {
		return nil, err
	}
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

func (ad *AllDebrid) updateTorrent(t *types.Torrent) (int, error) {
	var res TorrentInfoResponse

	resp, err := ad.doRequest("/magnet/status", map[string]string{"id": t.Id}, &res)
	if err != nil {
		return 0, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	data, err := findMagnet(res.Data.Magnets, t.Id)
	if err != nil {
		return 0, err
	}
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
	return data.StatusCode, nil
}

func (ad *AllDebrid) UpdateTorrent(t *types.Torrent) error {
	_, err := ad.updateTorrent(t)
	return err
}

func findMagnet(magnets Magnets, torrentId string) (magnetInfo, error) {
	for _, magnet := range magnets {
		if strconv.Itoa(magnet.Id) == torrentId {
			return magnet, nil
		}
	}

	return magnetInfo{}, customerror.TorrentNotFoundError
}

func (ad *AllDebrid) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	if torrent == nil {
		return nil, fmt.Errorf("torrent is required")
	}

	statusCode, err := ad.updateTorrent(torrent)
	if err != nil {
		return torrent, err
	}

	if statusCode == allDebridNoPeerStatusCode {
		statusCode, err = ad.restartNoPeerTorrent(torrent)
		if err != nil {
			return torrent, err
		}
	}

	switch torrent.Status {
	case types.TorrentStatusDownloaded:
		ad.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
		return torrent, nil
	case types.TorrentStatusDownloading:
		if !torrent.DownloadUncached {
			return torrent, fmt.Errorf("torrent %s: %w", torrent.Name, customerror.TorrentNotCachedError)
		}
		return torrent, nil
	case types.TorrentStatusError:
		return torrent, fmt.Errorf("torrent %s has AllDebrid status code %d", torrent.Name, statusCode)
	default:
		return torrent, fmt.Errorf("torrent %s has unknown AllDebrid status code %d", torrent.Name, statusCode)
	}
}

func defaultNoPeerRetryBackoff() []time.Duration {
	return []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}
}

func (ad *AllDebrid) restartNoPeerTorrent(torrent *types.Torrent) (int, error) {
	ad.logger.Warn().
		Str("torrent_id", torrent.Id).
		Str("name", torrent.Name).
		Msg("AllDebrid reported status code 7; restarting magnet")

	if err := ad.restartTorrent(torrent.Id); err != nil {
		return allDebridNoPeerStatusCode, fmt.Errorf("restart AllDebrid torrent %s after status code 7: %w", torrent.Id, err)
	}

	backoff := ad.noPeerRetryBackoff
	if len(backoff) == 0 {
		backoff = defaultNoPeerRetryBackoff()
	}
	for attempt, delay := range backoff {
		time.Sleep(delay)
		statusCode, err := ad.updateTorrent(torrent)
		if err != nil {
			return allDebridNoPeerStatusCode, fmt.Errorf("check AllDebrid torrent %s after restart: %w", torrent.Id, err)
		}
		if statusCode != allDebridNoPeerStatusCode {
			ad.logger.Info().
				Str("torrent_id", torrent.Id).
				Int("status_code", statusCode).
				Int("status_check", attempt+1).
				Msg("AllDebrid torrent resumed after restart")
			return statusCode, nil
		}
	}

	torrent.Status = types.TorrentStatusError
	return allDebridNoPeerStatusCode, fmt.Errorf(
		"AllDebrid torrent %s remained at status code 7 after restart and %d status checks",
		torrent.Id,
		len(backoff),
	)
}

func (ad *AllDebrid) restartTorrent(torrentID string) error {
	u, err := url.Parse(ad.Host)
	if err != nil {
		return err
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	if pathPrefix, ok := strings.CutSuffix(u.Path, "/v4.1"); ok {
		u.Path = pathPrefix + "/v4"
	}
	u.Path += "/magnet/restart"
	u.RawQuery = ""

	form := url.Values{"id": {torrentID}}
	req, err := http.NewRequest(http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ad.client.Do(req)
	if err != nil {
		return err
	}
	defer request.DrainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	var result restartMagnetResponse
	if err := request.DecodeJSON(resp, &result); err != nil {
		return fmt.Errorf("decode AllDebrid restart response: %w", err)
	}
	if result.Error != nil {
		if result.Error.Code == "MAGNET_PROCESSING" {
			return nil
		}
		return fmt.Errorf("alldebrid restart error %s: %s", result.Error.Code, result.Error.Message)
	}
	if result.Status != "success" {
		return fmt.Errorf("alldebrid restart returned status %q", result.Status)
	}
	return nil
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

func (ad *AllDebrid) CheckFile(ctx context.Context, _, link string) error {
	form := url.Values{}
	form.Add("link[]", link)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ad.Host+"/link/infos", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := ad.repairClient.Do(req)
	if err != nil {
		return err
	}
	defer request.DrainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("alldebrid API error: Status: %d", resp.StatusCode)
	}

	var data LinkInfosResponse
	if err := request.DecodeJSON(resp, &data); err != nil {
		return err
	}
	if data.Status != "success" {
		message := "unknown error"
		if data.Error != nil {
			message = data.Error.Message
		}
		return fmt.Errorf("alldebrid API error: %s", message)
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
	return true
}
