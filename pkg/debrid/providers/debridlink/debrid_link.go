package debridlink

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
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
	// statusFinished is the seedbox status of a finished torrent.
	statusFinished = 100
	// floodPause is the documented wait after floodDetected ("~1h").
	floodPause = time.Hour

	profileCacheDuration = time.Hour
)

type DebridLink struct {
	Host             string `json:"host"`
	APIKey           string
	accountsManager  *account.Manager
	DownloadUncached bool
	client           *request.Client
	repairClient     *request.Client
	throttle         *request.Throttle

	autoExpiresLinksAfter time.Duration
	logger                zerolog.Logger
	config                config.Debrid

	Profile        *types.Profile `json:"profile,omitempty"`
	profileFetched time.Time
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*DebridLink, error) {
	cfg := config.Get()
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	}
	log := logger.New(dc.Name)

	accounts := account.NewManager(dc, ratelimits["download"], log)
	throttle := accounts.Throttle(dc.APIKey)

	opts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithThrottle(throttle),
		request.WithMaxRetries(cfg.Retries),
		// No 429 here: a retry into a flood refusal earns the next one.
		request.WithRetryableStatus(http.StatusBadGateway),
	}
	if dc.Proxy != "" {
		opts = append(opts, request.WithProxy(dc.Proxy))
	}
	repairOpts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["repair"]),
		request.WithThrottle(throttle),
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
		accountsManager:       accounts,
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		repairClient:          request.New(repairOpts...),
		throttle:              throttle,
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

// apiError maps a Debrid-Link error code, which can arrive with any HTTP status.
func (dl *DebridLink) apiError(code, description string) error {
	switch code {
	case "floodDetected":
		dl.throttle.Trip(floodPause)
		dl.logger.Warn().Dur("pause", floodPause).Msg("Debrid-Link flood limit reached, pausing API requests")
		return &request.ThrottledError{RetryAfter: floodPause}
	case "badId":
		return customerror.TorrentNotFoundError
	case "maxTorrent", "maxTransfer":
		return customerror.TooManyActiveDownloadsError
	case "torrentTooBig", "notAddTorrent":
		return fmt.Errorf("debridlink refused the torrent: %s: %w", code, customerror.TorrentFailedError)
	case "fileNotAvailable":
		return customerror.TorrentBlockedError
	case "maxData", "maxDataHost", "maxLink", "maxLinkHost":
		return customerror.TrafficExceededError
	default:
		return fmt.Errorf("debridlink API error %s: %s", code, description)
	}
}

// read decodes a response into out. Every provider error goes through here, so
// a flood refusal stops the key no matter which call received it.
func (dl *DebridLink) read(resp *http.Response, out any) (*http.Response, error) {
	defer request.DrainAndClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, err
	}
	var failure errorResponse
	if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		return resp, dl.apiError(failure.Error, failure.Description)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return resp, &request.ThrottledError{RetryAfter: request.RetryAfter(resp)}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}
	if out == nil {
		return resp, nil
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return resp, fmt.Errorf("empty response from debridlink API")
	}
	return resp, json.Unmarshal(body, out)
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
	return dl.read(resp, result)
}

// IsAvailable reports that Debrid-Link has no cache check. It removed
// /seedbox/cached, so only an add can show whether a torrent is cached.
func (dl *DebridLink) IsAvailable(hashes []string) (map[string]bool, error) {
	return nil, types.ErrAvailabilityUnsupported
}

// GetTorrent reads a torrent through /seedbox/list, the documented way to fetch
// one torrent.
func (dl *DebridLink) GetTorrent(torrentId string) (*types.Torrent, error) {
	torrent := &types.Torrent{Id: torrentId, Debrid: dl.config.Name}
	if err := dl.UpdateTorrent(torrent); err != nil {
		return nil, err
	}
	return torrent, nil
}

// torrentStatus maps a torrent. A seeding torrent (status 8) is complete, and
// only downloadPercent says so for every status Debrid-Link reports.
func torrentStatus(t _torrentInfo) types.TorrentStatus {
	if t.Status == statusFinished || t.DownloadPercent >= 100 {
		return types.TorrentStatusDownloaded
	}
	return types.TorrentStatusDownloading
}

// torrentFiles converts the files of a torrent and caches their links. Each
// file carries its download URL, so no link needs a separate call.
func (dl *DebridLink) torrentFiles(t _torrentInfo) map[string]types.File {
	files := make(map[string]types.File, len(t.Files))
	cfg := config.Get()
	now := time.Now()
	for _, f := range t.Files {
		if err := cfg.ValidateFileAllowed(f.Name, f.Size); err != nil {
			continue
		}
		link := types.DownloadLink{
			Debrid:       dl.config.Name,
			Token:        dl.APIKey,
			Filename:     f.Name,
			Size:         f.Size,
			Link:         f.DownloadURL,
			DownloadLink: f.DownloadURL,
			Generated:    now,
			ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
		}
		files[f.Name] = types.File{
			TorrentId:    t.ID,
			Id:           f.ID,
			Name:         f.Name,
			Size:         f.Size,
			Path:         f.Name,
			Link:         f.DownloadURL,
			Generated:    now,
			DownloadLink: link,
		}
		dl.accountsManager.StoreDownloadLink(link)
	}
	return files
}

func (dl *DebridLink) applyTorrent(t *types.Torrent, data _torrentInfo) {
	name := utils.RemoveInvalidChars(data.Name)
	t.Id = data.ID
	t.Name = name
	t.Bytes = data.TotalSize
	t.Progress = data.DownloadPercent
	t.Status = torrentStatus(data)
	t.Speed = data.DownloadSpeed
	t.Seeders = data.PeersConnected
	t.Filename = name
	t.OriginalFilename = name
	t.Debrid = dl.config.Name
	if data.HashString != "" {
		t.InfoHash = data.HashString
	}
	t.Added = time.Unix(data.Created, 0)
	t.Files = dl.torrentFiles(data)
}

func (dl *DebridLink) UpdateTorrent(t *types.Torrent) error {
	var res torrentInfo

	if _, err := dl.doGet("/seedbox/list", map[string]string{"ids": t.Id}, &res); err != nil {
		return err
	}
	if !res.Success {
		return fmt.Errorf("error getting torrent")
	}
	if res.Value == nil || len(*res.Value) == 0 {
		return customerror.TorrentNotFoundError
	}
	dl.applyTorrent(t, (*res.Value)[0])
	return nil
}

// addRequest builds a /seedbox/add request. A .torrent upload goes first,
// because a private torrent cannot be recreated from its magnet.
func (dl *DebridLink) addRequest(t *types.Torrent) (*http.Request, error) {
	if !t.Magnet.IsTorrent() {
		body, err := json.Marshal(map[string]string{"url": t.Magnet.Link})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, dl.Host+"/seedbox/add", bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", cmp.Or(t.Filename, t.Magnet.Name, "upload")+".torrent")
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(t.Magnet.File); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, dl.Host+"/seedbox/add", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req, nil
}

func (dl *DebridLink) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	req, err := dl.addRequest(t)
	if err != nil {
		return nil, err
	}
	resp, err := dl.client.Do(req)
	if err != nil {
		return nil, err
	}

	var res SubmitTorrentInfo
	if _, err := dl.read(resp, &res); err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	dl.applyTorrent(t, *res.Value)
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
				return torrent, fmt.Errorf("torrent %s: %w", torrent.Name, customerror.TorrentNotCachedError)
			}
			return torrent, nil
		case types.TorrentStatusDownloaded:
			dl.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			return torrent, nil
		default:
			return torrent, fmt.Errorf("torrent %s: %w", torrent.Name, customerror.TorrentFailedError)
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
	defer request.DrainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink API error: Status: %d", resp.StatusCode)
	}

	dl.logger.Info().Msgf("Torrent: %s deleted from DebridLink", torrentId)
	return nil
}

func (dl *DebridLink) fetchDownloadLink(ctx context.Context, account *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	now := time.Now()
	link := types.DownloadLink{
		Debrid:       dl.config.Name,
		Token:        account.Token,
		Filename:     file.Name,
		Size:         file.Size,
		Link:         file.Link,
		DownloadLink: file.Link,
		Generated:    now,
		ExpiresAt:    now.Add(dl.autoExpiresLinksAfter),
	}
	return link, nil
}

func (dl *DebridLink) GetDownloadLink(ctx context.Context, id string, file *types.File) (types.DownloadLink, error) {
	return dl.accountsManager.GetDownloadLink(ctx, id, file, dl.fetchDownloadLink)
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
		t, listed, err := dl.getTorrents(page, perPage)
		if err != nil {
			fetchErr = err
			break
		}
		torrents = append(torrents, t...)
		// A page can hold only unfinished torrents, so the filtered result
		// says nothing about whether another page exists.
		if listed < perPage {
			break
		}
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

	// This runs on a timer, so a refusal waits for the next run. A retry here
	// would only walk into the flood limit.
	resp, err := account.Client().DoOnce(req)
	if err != nil {
		return links, err
	}
	var res DownloadLinksResponse
	if _, err := dl.read(resp, &res); err != nil {
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

// getTorrents returns the finished torrents on a page, and how many torrents
// the page listed before that filter.
func (dl *DebridLink) getTorrents(page, perPage int) ([]*types.Torrent, int, error) {
	var res torrentInfo

	params := map[string]string{
		"page":    strconv.Itoa(page),
		"perPage": strconv.Itoa(perPage),
	}

	if _, err := dl.doGet("/seedbox/list", params, &res); err != nil {
		return nil, 0, err
	}
	if !res.Success || res.Value == nil {
		return nil, 0, fmt.Errorf("error getting torrents")
	}

	data := *res.Value
	torrents := make([]*types.Torrent, 0, len(data))
	for _, t := range data {
		if torrentStatus(t) != types.TorrentStatusDownloaded {
			continue
		}
		torrent := &types.Torrent{InfoHash: t.HashString}
		dl.applyTorrent(torrent, t)
		torrents = append(torrents, torrent)
	}

	return torrents, len(data), nil
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
	defer request.DrainAndClose(resp.Body)

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return customerror.HosterUnavailableError
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("debridlink file check error: Status: %d", resp.StatusCode)
	}
	return nil
}

func (dl *DebridLink) GetAvailableSlots() (int, error) {
	// Debrid-Link does not provide available slots info
	return config.DefaultAvailableSlots, nil
}

func (dl *DebridLink) GetProfile() (*types.Profile, error) {
	if dl.Profile != nil && time.Since(dl.profileFetched) < profileCacheDuration {
		return dl.Profile, nil
	}
	profile, err := dl.getClientProfile(dl.client)
	if err != nil {
		return nil, err
	}
	dl.Profile = profile
	dl.profileFetched = time.Now()
	return profile, nil
}

func (dl *DebridLink) getClientProfile(client *request.Client) (*types.Profile, error) {
	req, err := http.NewRequest(http.MethodGet, dl.Host+"/account/infos", nil)
	if err != nil {
		return nil, err
	}
	// This runs on a timer, so a refusal waits for the next run.
	resp, err := client.DoOnce(req)
	if err != nil {
		return nil, err
	}
	var res UserInfo
	if _, err := dl.read(resp, &res); err != nil {
		return nil, err
	}
	if !res.Success || res.Value == nil {
		return nil, fmt.Errorf("error getting user info")
	}
	data := *res.Value
	profile := &types.Profile{
		Id:       1,
		Username: data.Username,
		Name:     dl.config.Name,
		Email:    data.Email,
		Points:   data.Points,
		Premium:  data.PremiumLeft,
		Type:     "free",
	}
	if data.PremiumLeft > 0 {
		// premiumLeft counts seconds, not a date.
		profile.Expiration = time.Now().Add(time.Duration(data.PremiumLeft) * time.Second)
		profile.Type = "premium"
	}
	return profile, nil
}

func (dl *DebridLink) AccountManager() *account.Manager {
	return dl.accountsManager
}

func (dl *DebridLink) syncAccount(acc *account.Account) error {
	profile, err := dl.getClientProfile(acc.Client())
	if err != nil {
		return err
	}
	acc.Username = profile.Username
	// A free account has no paid period to run out.
	if profile.Type != "free" {
		acc.Expiration = profile.Expiration
	}
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
	defer request.DrainAndClose(resp.Body)

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
