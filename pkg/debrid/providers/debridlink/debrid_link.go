package debridlink

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

type DebridLink struct {
	Host             string `json:"host"`
	APIKey           string
	accountsManager  *account.Manager
	DownloadUncached bool
	client           *request.Client
	repairClient     *request.Client

	autoExpiresLinksAfter time.Duration
	logger                zerolog.Logger
	config                config.Debrid

	Profile *types.Profile `json:"profile,omitempty"`
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*DebridLink, error) {
	cfg := config.Get()
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"Content-Type":  "application/json",
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	}
	log := logger.New(dc.Name)

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
	dbl := &DebridLink{
		Host:                  "https://debrid-link.com/api/v2",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], log),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		repairClient:          request.New(repairOpts...),
		logger:                log,
		config:                dc,
	}
	return dbl, nil
}

func (dl *DebridLink) Config() config.Debrid {
	return dl.config
}

func (dl *DebridLink) Logger() zerolog.Logger {
	return dl.logger
}

// doGet performs a GET request and unmarshals the response
func (dl *DebridLink) doGet(endpoint string, queryParams map[string]string, result any) (*http.Response, error) {
	u, err := url.Parse(dl.Host + endpoint)
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

	resp, err := dl.client.Do(req)
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

func (dl *DebridLink) IsAvailable(hashes []string) map[string]bool {
	result := make(map[string]bool)

	for i := 0; i < len(hashes); i += 100 {
		end := min(i+100, len(hashes))

		validHashes := make([]string, 0, end-i)
		for _, hash := range hashes[i:end] {
			if hash != "" {
				validHashes = append(validHashes, hash)
			}
		}

		if len(validHashes) == 0 {
			continue
		}

		hashStr := strings.Join(validHashes, ",")
		endpoint := fmt.Sprintf("/seedbox/cached/%s", hashStr)
		var data AvailableResponse

		resp, err := dl.doGet(endpoint, nil, &data)
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
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
	endpoint := fmt.Sprintf("/seedbox/%s", torrentId)
	var res torrentInfo

	resp, err := dl.doGet(endpoint, nil, &res)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
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
		Debrid:           dl.config.Name,
		Added:            time.Unix(t.Created, 0),
	}
	cfg := config.Get()
	for _, f := range t.Files {
		if err := cfg.IsFileAllowed(f.Name, f.Size); err != nil {
			continue
		}
		file := types.File{
			TorrentId: t.ID,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
		}
		torrent.Files[file.Name] = file
	}

	return torrent, nil
}

func (dl *DebridLink) UpdateTorrent(t *types.Torrent) error {
	var res torrentInfo

	resp, err := dl.doGet("/seedbox/list", map[string]string{"ids": t.Id}, &res)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
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
	status := types.TorrentStatusDownloading
	if data.Status == 100 {
		status = types.TorrentStatusDownloaded
	}
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Progress = data.DownloadPercent
	t.Status = status
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	if data.HashString != "" {
		t.InfoHash = data.HashString
	}
	t.Added = time.Unix(data.Created, 0)
	cfg := config.Get()
	now := time.Now()
	for _, f := range data.Files {
		if err := cfg.IsFileAllowed(f.Name, f.Size); err != nil {
			continue
		}
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
		}
		link := types.DownloadLink{
			Debrid:       dl.config.Name,
			Token:        dl.APIKey,
			Filename:     f.Name,
			Link:         f.DownloadURL,
			DownloadLink: f.DownloadURL,
			Generated:    now,
			ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
		}
		file.DownloadLink = link
		t.Files[f.Name] = file
		dl.accountsManager.StoreDownloadLink(link)
	}

	return nil
}

func (dl *DebridLink) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	payload := map[string]string{"url": t.Magnet.Link}
	var res SubmitTorrentInfo

	dt, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	body := bytes.NewReader(dt)

	req, err := http.NewRequest(http.MethodPost, dl.Host+"/seedbox/add", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := dl.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bd, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("error adding torrent(status %d): %s", resp.StatusCode, string(bd))
	}
	if resp.ContentLength == 0 {
		return nil, fmt.Errorf("empty response from debridlink API")
	}
	if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	data := *res.Value
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Progress = data.DownloadPercent
	t.Status = types.TorrentStatusDownloading
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.Debrid = dl.config.Name
	t.Added = time.Unix(data.Created, 0)
	now := time.Now()
	for _, f := range data.Files {
		file := types.File{
			TorrentId: t.Id,
			Id:        f.ID,
			Name:      f.Name,
			Size:      f.Size,
			Path:      f.Name,
			Link:      f.DownloadURL,
			Generated: now,
		}
		link := types.DownloadLink{
			Debrid:       dl.config.Name,
			Token:        dl.APIKey,
			Filename:     f.Name,
			Link:         f.DownloadURL,
			DownloadLink: f.DownloadURL,
			Generated:    now,
			ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
		}
		file.DownloadLink = link
		t.Files[f.Name] = file
		dl.accountsManager.StoreDownloadLink(link)
	}

	return t, nil
}

func (dl *DebridLink) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	for {
		err := dl.UpdateTorrent(torrent)
		if err != nil || torrent == nil {
			return torrent, err
		}
		switch torrent.Status {
		case types.TorrentStatusDownloading:
			if !torrent.DownloadUncached {
				return torrent, fmt.Errorf("torrent: %s not cached", torrent.Name)
			}
			return torrent, nil
		case types.TorrentStatusDownloaded:
			dl.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			return torrent, nil
		default:
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		}
	}
}

func (dl *DebridLink) DeleteTorrent(torrentId string) error {
	endpoint := fmt.Sprintf("/seedbox/%s/remove", torrentId)

	req, err := http.NewRequest(http.MethodDelete, dl.Host+endpoint, nil)
	if err != nil {
		return err
	}

	resp, err := dl.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}

	dl.logger.Info().Msgf("Torrent: %s deleted from DebridLink", torrentId)
	return nil
}

func (dl *DebridLink) fetchDownloadLink(account *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	now := time.Now()
	link := types.DownloadLink{
		Debrid:       dl.config.Name,
		Token:        dl.APIKey,
		Filename:     file.Name,
		Link:         file.Link,
		DownloadLink: file.Link,
		Generated:    now,
		ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
	}
	return link, nil
}

func (dl *DebridLink) GetDownloadLink(id string, file *types.File) (types.DownloadLink, error) {
	return dl.accountsManager.GetDownloadLink(id, file, dl.fetchDownloadLink)
}

func (dl *DebridLink) GetDownloadUncached() bool {
	return dl.DownloadUncached
}

func (dl *DebridLink) GetTorrents() ([]*types.Torrent, error) {
	page := 0
	perPage := 100
	torrents := make([]*types.Torrent, 0)
	var fetchErr error
	for {
		t, err := dl.getTorrents(page, perPage)
		if err != nil {
			fetchErr = err
			break
		}
		if len(t) == 0 {
			break
		}
		torrents = append(torrents, t...)
		page++
	}
	if fetchErr != nil {
		return torrents, fetchErr
	}
	return torrents, nil
}

func (dl *DebridLink) fetchDownloadLinks(account *account.Account) ([]types.DownloadLink, error) {
	links := make([]types.DownloadLink, 0)
	limit := 100
	page := 0
	for {
		data, err := dl._fetchDownloadLinks(account, page, limit)
		if err != nil {
			return links, err
		}
		links = append(links, data...)
		if len(data) < limit {
			break
		}
		page++
	}
	return links, nil
}

func (dl *DebridLink) _fetchDownloadLinks(account *account.Account, page, limit int) ([]types.DownloadLink, error) {
	links := make([]types.DownloadLink, 0)

	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/downloader/list?page=%d&perPage=%d", dl.Host, page, limit), nil)
	if err != nil {
		return links, err
	}

	resp, err := account.Client().Do(req)
	if err != nil {
		return links, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return links, fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}
	var res DownloadLinksResponse

	if resp.ContentLength == 0 {
		return links, fmt.Errorf("empty response from debridlink API")
	}
	if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(&res); err != nil {
		return links, err
	}
	if !res.Success || res.Value == nil {
		return links, fmt.Errorf("error getting download links")
	}
	data := *res.Value
	if len(data) == 0 {
		return links, nil
	}
	for _, l := range data {
		created := time.Unix(l.Created, 0)
		if created.IsZero() {
			continue
		}
		// Then check if created has expired
		if time.Since(created) > dl.autoExpiresLinksAfter {
			continue
		}
		link := types.DownloadLink{
			Debrid:       dl.config.Name,
			Id:           l.Id,
			Token:        dl.APIKey,
			Filename:     l.Name,
			Link:         l.Url,
			DownloadLink: l.DownloadUrl,
			Generated:    created,
			ExpiresAt:    created.Add(dl.autoExpiresLinksAfter),
		}
		links = append(links, link)
	}
	return links, nil
}

func (dl *DebridLink) RefreshDownloadLinks() error {
	return dl.accountsManager.RefreshLinks(dl.fetchDownloadLinks)
}

func (dl *DebridLink) getTorrents(page, perPage int) ([]*types.Torrent, error) {
	torrents := make([]*types.Torrent, 0)
	var res torrentInfo

	params := map[string]string{
		"page":    fmt.Sprintf("%d", page),
		"perPage": fmt.Sprintf("%d", perPage),
	}

	resp, err := dl.doGet("/seedbox/list", params, &res)
	if err != nil {
		return torrents, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return torrents, fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
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
			Debrid:           dl.config.Name,
			Added:            time.Unix(t.Created, 0),
		}
		cfg := config.Get()
		now := time.Now()
		for _, f := range t.Files {
			if err := cfg.IsFileAllowed(f.Name, f.Size); err != nil {
				continue
			}
			file := types.File{
				TorrentId: torrent.Id,
				Id:        f.ID,
				Name:      f.Name,
				Size:      f.Size,
				Path:      f.Name,
				Link:      f.DownloadURL,
			}
			link := types.DownloadLink{
				Debrid:       dl.config.Name,
				Token:        dl.APIKey,
				Filename:     f.Name,
				Link:         f.DownloadURL,
				DownloadLink: f.DownloadURL,
				Generated:    now,
				ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
			}
			file.DownloadLink = link
			torrent.Files[f.Name] = file
			dl.accountsManager.StoreDownloadLink(link)
		}
		torrents = append(torrents, torrent)
	}

	return torrents, nil
}

func (dl *DebridLink) CheckFile(ctx context.Context, _, link string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Range", "bytes=0-0")

	resp, err := dl.repairClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return customerror.HosterUnavailableError
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink file check error: Status: %d", resp.StatusCode)
	}
	return nil
}

func (dl *DebridLink) GetAvailableSlots() (int, error) {
	// AllDebrid does not provide available slots info
	return config.DefaultAvailableSlots, nil
}

func (dl *DebridLink) GetProfile() (*types.Profile, error) {
	if dl.Profile != nil {
		return dl.Profile, nil
	}
	var res UserInfo

	resp, err := dl.doGet("/account/infos", nil, &res)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error getting user info")
	}
	data := *res.Value
	expiration := time.Unix(data.PremiumLeft, 0)
	profile := &types.Profile{
		Id:         1,
		Username:   data.Username,
		Name:       dl.config.Name,
		Email:      data.Email,
		Points:     data.Points,
		Premium:    data.PremiumLeft,
		Expiration: expiration,
	}
	if expiration.IsZero() {
		profile.Expiration = time.Now().AddDate(1, 0, 0)
	}
	if data.PremiumLeft > 0 {
		profile.Type = "premium"
	} else {
		profile.Type = "free"
	}
	dl.Profile = profile
	return profile, nil
}

func (dl *DebridLink) AccountManager() *account.Manager {
	return dl.accountsManager
}

func (dl *DebridLink) syncAccount(account *account.Account) error {
	// Currently no account-specific data to sync
	return nil
}

func (dl *DebridLink) SyncAccounts() {
	dl.accountsManager.Sync(dl.syncAccount)
}

func (dl *DebridLink) deleteDownloadLink(account *account.Account, downloadLink types.DownloadLink) error {
	deleteURL := fmt.Sprintf("%s/downloader/%s/remove", dl.Host, downloadLink.Id)
	req, err := http.NewRequest(http.MethodDelete, deleteURL, nil)
	if err != nil {
		return err
	}

	resp, err := account.Client().Do(req)
	if err != nil {
		return err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}

	dl.logger.Info().Msgf("Download link: %s deleted from DebridLink", downloadLink.Filename)
	return nil
}

func (dl *DebridLink) DeleteLink(downloadLink types.DownloadLink) error {
	return dl.accountsManager.DeleteDownloadLink(downloadLink, dl.deleteDownloadLink)
}

// SpeedTest measures API latency and download speed using cached links
func (dl *DebridLink) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: dl.config.Name,
		TestedAt: time.Now(),
	}

	start := time.Now()
	resp, err := dl.doGet("/account/infos", nil, nil)
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
	current := dl.accountsManager.Current()
	if current == nil {
		return result
	}

	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result
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

func (dl *DebridLink) SupportsCheck() bool {
	return true
}
