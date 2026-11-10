package highway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"runtime"
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
	"github.com/sirrobot01/decypharr/pkg/version"
	"go.uber.org/ratelimit"
)

const (
	defaultHost = "https://cloud.high-way.me/center-api"
	// Signed download links from the API are valid for about 24 hours.
	maxLinkLifetime = 23 * time.Hour
	// The API checks up to 100 hashes per request.
	checkBatchSize = 100
	listPageSize   = 500
)

var (
	queueIdPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	infoHashHex    = regexp.MustCompile(`^[a-fA-F0-9]{40}$`)

	errNotFound = errors.New("highway: not found")
)

// Highway talks to the High-Way transfer API (https://cloud.high-way.me/center-api/transfers.php).
//
// Transfers that are not cached yet wait in a queue on High-Way's side and get a
// temporary 32-hex id; once they start they get a numeric id, which is looked up
// again by info hash.
type Highway struct {
	Host                  string
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration
	client                *request.Client
	logger                zerolog.Logger
	config                config.Debrid
	Profile               *types.Profile
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Highway, error) {
	cfg := config.Get()
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	} else {
		headers["User-Agent"] = fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH)
	}
	_log := logger.New(dc.Name)

	mainRL := ratelimits["main"]
	if mainRL == nil {
		mainRL = ratelimit.New(120, ratelimit.Per(time.Minute), ratelimit.WithSlack(10))
	}
	downloadRL := ratelimits["download"]
	if downloadRL == nil {
		downloadRL = mainRL
	}

	opts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithRateLimiter(mainRL),
		request.WithMaxRetries(cfg.Retries),
		request.WithRetryableStatus(http.StatusTooManyRequests, http.StatusBadGateway),
		request.WithLogger(_log),
	}
	if dc.Proxy != "" {
		opts = append(opts, request.WithProxy(dc.Proxy))
	}

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter <= 0 || autoExpiresLinksAfter > maxLinkLifetime || err != nil {
		autoExpiresLinksAfter = maxLinkLifetime
	}

	return &Highway{
		Host:                  defaultHost,
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, downloadRL, _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		logger:                _log,
		config:                dc,
	}, nil
}

func (hw *Highway) Config() config.Debrid {
	return hw.config
}

func (hw *Highway) Logger() zerolog.Logger {
	return hw.logger
}

func (hw *Highway) AccountManager() *account.Manager {
	return hw.accountsManager
}

func (hw *Highway) SupportsCheck() bool {
	return true
}

// ------------------------------------------------------------------------------------------- HTTP

func (hw *Highway) endpoint(query url.Values) string {
	u := hw.Host + "/transfers.php"
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// do sends the request and decodes a 2xx JSON body into out. Error bodies are not
// logged or returned: they may contain signed links or account details.
func (hw *Highway) do(client *request.Client, req *http.Request, out any) error {
	if client == nil {
		client = hw.client
	}
	resp, err := client.DoJSON(req, out)
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("highway: invalid or revoked API token (HTTP 401)")
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("highway: API token lacks the required scope (HTTP 403)")
	default:
		return fmt.Errorf("highway API error: HTTP %d", resp.StatusCode)
	}
}

func (hw *Highway) get(ctx context.Context, client *request.Client, query url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hw.endpoint(query), nil)
	if err != nil {
		return err
	}
	return hw.do(client, req, out)
}

// ------------------------------------------------------------------------------------ Availability

func (hw *Highway) IsAvailable(hashes []string) (map[string]bool, error) {
	result := make(map[string]bool, len(hashes))
	valid := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		if hash == "" {
			continue
		}
		if !infoHashHex.MatchString(hash) {
			// The API only knows hex info hashes.
			result[hash] = false
			continue
		}
		valid = append(valid, hash)
	}

	for i := 0; i < len(valid); i += checkBatchSize {
		batch := valid[i:min(i+checkBatchSize, len(valid))]
		var res checkResponse
		query := url.Values{"check": {strings.Join(batch, ",")}, "service": {"torrent"}}
		if err := hw.get(context.Background(), nil, query, &res); err != nil {
			return result, fmt.Errorf("check availability: %w", err)
		}
		for _, hash := range batch {
			result[hash] = res.Results[strings.ToLower(hash)].Ready
		}
	}
	return result, nil
}

// ------------------------------------------------------------------------------------- Submitting

func (hw *Highway) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	if !torrent.DownloadUncached && torrent.InfoHash != "" {
		available, err := hw.IsAvailable([]string{torrent.InfoHash})
		if err != nil {
			return nil, err
		}
		if !available[torrent.InfoHash] {
			return nil, fmt.Errorf("torrent %s: %w", torrent.Name, customerror.TorrentNotCachedError)
		}
	}

	req, err := hw.submitRequest(torrent.Magnet)
	if err != nil {
		return nil, err
	}
	var data addResponse
	if err := hw.do(nil, req, &data); err != nil {
		return nil, fmt.Errorf("add torrent: %w", err)
	}
	if data.Id == "" {
		return nil, fmt.Errorf("add torrent: empty id in response")
	}
	torrent.Id = data.Id
	torrent.Debrid = hw.config.Name
	torrent.Added = time.Now()
	return torrent, nil
}

func (hw *Highway) submitRequest(magnet *utils.Magnet) (*http.Request, error) {
	if magnet == nil {
		return nil, fmt.Errorf("add torrent: no magnet or torrent file")
	}
	if magnet.IsTorrent() && len(magnet.File) > 0 {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "upload.torrent")
		if err != nil {
			return nil, err
		}
		if _, err := part.Write(magnet.File); err != nil {
			return nil, err
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		req, err := http.NewRequest(http.MethodPost, hw.endpoint(nil), &body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", writer.FormDataContentType())
		return req, nil
	}
	form := url.Values{"magnet": {magnet.Link}}
	req, err := http.NewRequest(http.MethodPost, hw.endpoint(nil), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// ---------------------------------------------------------------------------------------- Status

func status(state string) types.TorrentStatus {
	switch state {
	case "completed":
		return types.TorrentStatusDownloaded
	case "queued", "downloading":
		return types.TorrentStatusDownloading
	default:
		return types.TorrentStatusError
	}
}

// fetchTransfer loads one transfer. A queue id that is gone (the transfer has
// started in the meantime) is resolved to the numeric id by info hash.
func (hw *Highway) fetchTransfer(t *types.Torrent) (*transfer, error) {
	query := url.Values{"id": {t.Id}}
	if !queueIdPattern.MatchString(t.Id) {
		query.Set("service", "torrent")
	}
	var data transfer
	err := hw.get(context.Background(), nil, query, &data)
	if errors.Is(err, errNotFound) && queueIdPattern.MatchString(t.Id) && t.InfoHash != "" {
		id, lookupErr := hw.transferIdByHash(t.InfoHash)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if id == "" {
			return nil, fmt.Errorf("torrent %s: %w", t.Id, customerror.TorrentNotFoundError)
		}
		t.Id = id
		data = transfer{}
		err = hw.get(context.Background(), nil, url.Values{"id": {id}, "service": {"torrent"}}, &data)
	}
	if errors.Is(err, errNotFound) {
		return nil, fmt.Errorf("torrent %s: %w", t.Id, customerror.TorrentNotFoundError)
	}
	if err != nil {
		return nil, err
	}
	return &data, nil
}

func (hw *Highway) transferIdByHash(hash string) (string, error) {
	var res checkResponse
	if err := hw.get(context.Background(), nil, url.Values{"check": {hash}, "service": {"torrent"}}, &res); err != nil {
		return "", err
	}
	if item, ok := res.Results[strings.ToLower(hash)]; ok && item.Id != nil {
		return *item.Id, nil
	}
	return "", nil
}

// apply copies a transfer (with or without files) onto t.
func (hw *Highway) apply(t *types.Torrent, data *transfer, withFiles bool) {
	if data.Id != "" {
		t.Id = data.Id
	}
	t.Name = data.Name
	t.Filename = data.Name
	t.OriginalFilename = data.Name
	t.Bytes = data.Size
	t.Progress = data.Progress
	t.Status = status(data.State)
	t.Seeders = data.Seeder
	t.Debrid = hw.config.Name
	if data.Hash != nil && *data.Hash != "" {
		t.InfoHash = *data.Hash
	}
	if data.AddedAt != nil && *data.AddedAt > 0 {
		t.Added = time.Unix(*data.AddedAt, 0)
	}
	if !withFiles {
		return
	}

	cfg := config.Get()
	t.Files = make(map[string]types.File, len(data.Files))
	for _, f := range data.Files {
		if f.Partial {
			continue
		}
		if err := cfg.ValidateFileAllowed(f.Path, f.Size); err != nil {
			continue
		}
		name := path.Base(f.Path)
		file := types.File{
			TorrentId: t.Id,
			Id:        f.Id,
			Name:      name,
			Size:      f.Size,
			Path:      f.Path,
		}
		if data.State == "completed" {
			file.Link = fileLink(t.Id, f.Id)
		}
		t.Files[name] = file
	}
	if len(data.Files) > 0 {
		t.OriginalFilename = strings.Split(path.Clean(data.Files[0].Path), "/")[0]
	}
}

func fileLink(torrentId, fileId string) string {
	return fmt.Sprintf("highway://%s/%s", torrentId, fileId)
}

func parseFileLink(link string) (torrentId, fileId string, ok bool) {
	rest, found := strings.CutPrefix(link, "highway://")
	if !found {
		return "", "", false
	}
	torrentId, fileId, ok = strings.Cut(rest, "/")
	return torrentId, fileId, ok && torrentId != "" && fileId != ""
}

func (hw *Highway) GetTorrent(torrentId string) (*types.Torrent, error) {
	t := &types.Torrent{Id: torrentId}
	if err := hw.UpdateTorrent(t); err != nil {
		return nil, err
	}
	return t, nil
}

func (hw *Highway) UpdateTorrent(t *types.Torrent) error {
	data, err := hw.fetchTransfer(t)
	if err != nil {
		return err
	}
	hw.apply(t, data, true)
	return nil
}

func (hw *Highway) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	if err := hw.UpdateTorrent(torrent); err != nil {
		return torrent, err
	}
	switch torrent.Status {
	case types.TorrentStatusDownloaded:
		hw.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
		return torrent, nil
	case types.TorrentStatusDownloading:
		if !torrent.DownloadUncached {
			return torrent, fmt.Errorf("torrent %s: %w", torrent.Name, customerror.TorrentNotCachedError)
		}
		return torrent, nil
	default:
		return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
	}
}

func (hw *Highway) GetTorrents() ([]*types.Torrent, error) {
	torrents := make([]*types.Torrent, 0)
	for offset := 0; ; {
		var res listResponse
		query := url.Values{
			"service": {"torrent"},
			"limit":   {strconv.Itoa(listPageSize)},
			"offset":  {strconv.Itoa(offset)},
		}
		if err := hw.get(context.Background(), nil, query, &res); err != nil {
			return nil, fmt.Errorf("get HighWay torrents at offset %d: %w", offset, err)
		}
		for i := range res.Transfers {
			data := &res.Transfers[i]
			// Queued torrents have no hash yet; they show up once they start.
			if data.Hash == nil || *data.Hash == "" {
				continue
			}
			t := &types.Torrent{}
			hw.apply(t, data, false)
			torrents = append(torrents, t)
		}
		offset += len(res.Transfers)
		if len(res.Transfers) == 0 || offset >= res.Total {
			break
		}
	}
	return torrents, nil
}

func (hw *Highway) DeleteTorrent(torrentId string) error {
	query := url.Values{"id": {torrentId}}
	if !queueIdPattern.MatchString(torrentId) {
		query.Set("service", "torrent")
	}
	req, err := http.NewRequest(http.MethodDelete, hw.endpoint(query), nil)
	if err != nil {
		return err
	}
	var res removeResponse
	if err := hw.do(nil, req, &res); err != nil {
		return err
	}
	if !res.Removed {
		hw.logger.Debug().Msgf("Torrent %s was already gone on HighWay", torrentId)
		return nil
	}
	hw.logger.Info().Msgf("Torrent %s deleted from HighWay", torrentId)
	return nil
}

// ---------------------------------------------------------------------------------------- Links

func (hw *Highway) GetDownloadLink(ctx context.Context, torrentId string, file *types.File) (types.DownloadLink, error) {
	return hw.accountsManager.GetDownloadLink(ctx, torrentId, file, hw.fetchDownloadLink)
}

func (hw *Highway) fetchFile(ctx context.Context, client *request.Client, torrentId, fileId string) (*fileResponse, error) {
	query := url.Values{"id": {torrentId}, "service": {"torrent"}, "file": {fileId}}
	var res fileResponse
	if err := hw.get(ctx, client, query, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

func (hw *Highway) fetchDownloadLink(ctx context.Context, acc *account.Account, torrentId string, file *types.File) (types.DownloadLink, error) {
	res, err := hw.fetchFile(ctx, acc.Client(), torrentId, file.Id)
	if errors.Is(err, errNotFound) {
		return types.DownloadLink{}, customerror.HosterUnavailableError
	}
	if err != nil {
		return types.DownloadLink{}, err
	}
	if res.File.DownloadURL == nil || *res.File.DownloadURL == "" {
		return types.DownloadLink{}, fmt.Errorf("highway: no download link for file %s", file.Id)
	}
	now := time.Now()
	return types.DownloadLink{
		Filename:     file.Name,
		Size:         file.Size,
		Token:        acc.Token,
		Link:         file.Link,
		DownloadLink: *res.File.DownloadURL,
		Debrid:       hw.config.Name,
		Id:           file.Id,
		Generated:    now,
		ExpiresAt:    now.Add(hw.autoExpiresLinksAfter),
	}, nil
}

func (hw *Highway) RefreshDownloadLinks() error {
	// Links are signed per request; there is no list of generated links to refresh.
	return hw.accountsManager.RefreshLinks(func(*account.Account) ([]types.DownloadLink, error) {
		return []types.DownloadLink{}, nil
	})
}

func (hw *Highway) DeleteLink(downloadLink types.DownloadLink) error {
	return hw.accountsManager.DeleteDownloadLink(downloadLink, func(*account.Account, types.DownloadLink) error {
		return nil
	})
}

// CheckFile reports whether a file of a finished transfer is still available.
func (hw *Highway) CheckFile(ctx context.Context, infohash, link string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	torrentId, fileId, ok := parseFileLink(link)
	if !ok {
		return customerror.HosterUnavailableError
	}
	_, err := hw.fetchFile(ctx, nil, torrentId, fileId)
	if errors.Is(err, errNotFound) {
		return customerror.HosterUnavailableError
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// --------------------------------------------------------------------------------------- Account

func (hw *Highway) stats(ctx context.Context) (*statsResponse, error) {
	var res statsResponse
	if err := hw.get(ctx, nil, url.Values{"stats": {"1"}}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// GetAvailableSlots returns the free transfer slots. HighWay queues transfers
// above the limit instead of rejecting them.
func (hw *Highway) GetAvailableSlots() (int, error) {
	res, err := hw.stats(context.Background())
	if err != nil {
		return 0, err
	}
	return res.Transfers.Available, nil
}

func (hw *Highway) GetProfile() (*types.Profile, error) {
	if hw.Profile != nil {
		return hw.Profile, nil
	}
	res, err := hw.stats(context.Background())
	if err != nil {
		return nil, err
	}
	profile := &types.Profile{
		Name:     hw.config.Name,
		Id:       res.UserId,
		Username: res.Username,
		Type:     res.Kind,
	}
	if res.PlanExpiresAt != nil {
		profile.Expiration = time.Unix(*res.PlanExpiresAt, 0)
	}
	if res.Premium && res.PlanExpiresAt != nil {
		profile.Premium = max(int64(time.Until(profile.Expiration).Seconds()), 0)
	}
	hw.Profile = profile
	return profile, nil
}

func (hw *Highway) SyncAccounts() {
	hw.accountsManager.Sync(func(*account.Account) error { return nil })
}

// SpeedTest measures API latency and, if a link is cached, download speed.
func (hw *Highway) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: hw.config.Name,
		TestedAt: time.Now(),
	}

	start := time.Now()
	if _, err := hw.stats(ctx); err != nil {
		result.Error = fmt.Sprintf("latency test failed: %v", err)
		return result
	}
	result.LatencyMs = time.Since(start).Milliseconds()

	current := hw.accountsManager.Current()
	if current == nil {
		return result
	}
	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result
	}

	const downloadSize = 1 * 1024 * 1024
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.DownloadLink, nil)
	if err != nil {
		return result
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", downloadSize-1))

	downloadStart := time.Now()
	resp, err := current.Client().Do(req)
	if err != nil {
		return result
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	duration := time.Since(downloadStart)
	if err != nil || len(data) == 0 {
		return result
	}
	result.BytesRead = int64(len(data))
	if duration.Seconds() > 0 {
		result.SpeedMBps = float64(result.BytesRead) / duration.Seconds() / (1024 * 1024)
	}
	return result
}
