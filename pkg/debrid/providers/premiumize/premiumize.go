package premiumize

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
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
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/version"
	"go.uber.org/ratelimit"
)

const (
	defaultHost          = "https://www.premiumize.me"
	profileCacheDuration = time.Hour
)

var _ common.Client = (*Premiumize)(nil)

type Premiumize struct {
	Host                  string `json:"host"`
	APIKey                string
	client                *request.Client
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration
	logger                zerolog.Logger
	config                config.Debrid
	profile               *types.Profile
	profileLastFetched    time.Time
	isFileAllowed         func(string, int64) error
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Premiumize, error) {
	cfg := config.Get()
	_log := logger.New(dc.Name)
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	} else {
		headers["User-Agent"] = fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH)
	}

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	opts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithLogger(_log),
		request.WithMaxRetries(cfg.Retries),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithRetryableStatus(http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout),
	}
	if dc.Proxy != "" {
		opts = append(opts, request.WithProxy(dc.Proxy))
	}

	return &Premiumize{
		Host:                  defaultHost,
		APIKey:                dc.APIKey,
		client:                request.New(opts...),
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		logger:                _log,
		config:                dc,
		isFileAllowed:         config.Get().IsFileAllowed,
	}, nil
}

func (pm *Premiumize) Logger() zerolog.Logger {
	return pm.logger
}

func (pm *Premiumize) Config() config.Debrid {
	return pm.config
}

func (pm *Premiumize) endpoint(apiPath string) string {
	if strings.HasPrefix(apiPath, "/") {
		return pm.Host + apiPath
	}
	return pm.Host + "/" + apiPath
}

func (pm *Premiumize) do(req *http.Request, out any) (*http.Response, error) {
	resp, err := pm.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, fmt.Errorf("reading response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, fmt.Errorf("premiumize API error: Status: %d || Body: %s", resp.StatusCode, string(body))
	}

	if len(bytes.TrimSpace(body)) == 0 || out == nil {
		return resp, nil
	}

	if err := json.Unmarshal(body, out); err != nil {
		return resp, err
	}

	var envelope apiError
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Status == "error" {
		return resp, fmt.Errorf("premiumize API error: %s (%s)", envelope.Message, envelope.Code)
	}

	return resp, nil
}

func (pm *Premiumize) doForm(ctx context.Context, method, apiPath string, values url.Values, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, pm.endpoint(apiPath), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return pm.do(req, out)
}

func (pm *Premiumize) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	if t.Magnet == nil {
		return nil, fmt.Errorf("missing magnet")
	}
	if t.Magnet.IsTorrent() {
		return pm.addTorrent(t)
	}
	return pm.addMagnet(t)
}

func (pm *Premiumize) addMagnet(t *types.Torrent) (*types.Torrent, error) {
	src := t.Magnet.Link
	if src == "" && t.InfoHash != "" {
		src = utils.ConstructMagnet(t.InfoHash, t.Name).Link
	}
	var data transferCreateResponse
	_, err := pm.doForm(context.Background(), http.MethodPost, "/api/transfer/create", url.Values{"src": {src}}, &data)
	if err != nil {
		return nil, err
	}
	pm.applySubmittedTorrent(t, data)
	return t, nil
}

func (pm *Premiumize) addTorrent(t *types.Torrent) (*types.Torrent, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("src", cmp.Or(t.Filename, t.Magnet.Name, "upload.torrent"))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(t.Magnet.File); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, pm.endpoint("/api/transfer/create"), &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	var data transferCreateResponse
	if _, err := pm.do(req, &data); err != nil {
		return nil, err
	}
	pm.applySubmittedTorrent(t, data)
	return t, nil
}

func (pm *Premiumize) applySubmittedTorrent(t *types.Torrent, data transferCreateResponse) {
	t.Id = data.ID
	t.Debrid = pm.config.Name
	t.Status = types.TorrentStatusQueued
	if data.Name != "" {
		t.Name = data.Name
		t.Filename = data.Name
	}
	if t.OriginalFilename == "" {
		t.OriginalFilename = cmp.Or(t.Magnet.Name, t.Name)
	}
}

func (pm *Premiumize) CheckStatus(t *types.Torrent) (*types.Torrent, error) {
	return pm.UpdateAndReturnTorrent(t)
}

func (pm *Premiumize) UpdateAndReturnTorrent(t *types.Torrent) (*types.Torrent, error) {
	if err := pm.UpdateTorrent(t); err != nil {
		return t, err
	}
	if t.Status == types.TorrentStatusDownloaded {
		return t, nil
	}
	if t.Status == types.TorrentStatusDownloading || t.Status == types.TorrentStatusQueued {
		if !t.DownloadUncached {
			return t, fmt.Errorf("torrent: %s not cached", t.Name)
		}
		return t, nil
	}
	return t, fmt.Errorf("torrent: %s has error status: %s", t.Name, t.Status)
}

func (pm *Premiumize) GetTorrent(torrentID string) (*types.Torrent, error) {
	transfers, err := pm.listTransfers()
	if err != nil {
		return nil, err
	}
	for _, tr := range transfers {
		if tr.ID == torrentID {
			return pm.transferToTorrent(tr, "")
		}
	}
	return nil, customerror.TorrentNotFoundError
}

func (pm *Premiumize) UpdateTorrent(t *types.Torrent) error {
	transfers, err := pm.listTransfers()
	if err != nil {
		return err
	}
	for _, tr := range transfers {
		if tr.ID == t.Id {
			updated, err := pm.transferToTorrent(tr, t.InfoHash)
			if err != nil {
				return err
			}
			t.Name = updated.Name
			t.Filename = updated.Filename
			t.OriginalFilename = updated.OriginalFilename
			t.Bytes = updated.Bytes
			t.Size = updated.Size
			t.Progress = updated.Progress
			t.Status = updated.Status
			t.Files = updated.Files
			t.Links = updated.Links
			t.Debrid = updated.Debrid
			t.InfoHash = cmp.Or(updated.InfoHash, t.InfoHash)
			return nil
		}
	}
	return customerror.TorrentNotFoundError
}

func (pm *Premiumize) DeleteTorrent(torrentID string) error {
	_, err := pm.doForm(context.Background(), http.MethodPost, "/api/transfer/delete", url.Values{"id": {torrentID}}, nil)
	return err
}

func (pm *Premiumize) IsAvailable(infohashes []string) map[string]bool {
	result := make(map[string]bool, len(infohashes))
	const batchSize = 100
	for i := 0; i < len(infohashes); i += batchSize {
		end := min(i+batchSize, len(infohashes))
		values := url.Values{}
		hashByItem := make(map[string]string, end-i)
		for _, hash := range infohashes[i:end] {
			if hash == "" {
				continue
			}
			item := utils.ConstructMagnet(hash, "").Link
			values.Add("items[]", item)
			hashByItem[item] = hash
		}
		if len(values) == 0 {
			continue
		}
		var data cacheCheckResponse
		if _, err := pm.doForm(context.Background(), http.MethodPost, "/api/cache/check", values, &data); err != nil {
			pm.logger.Error().Err(err).Msg("Error checking Premiumize availability")
			continue
		}
		items := values["items[]"]
		for idx, available := range data.Response {
			if idx < len(items) && available {
				result[hashByItem[items[idx]]] = true
			}
		}
	}
	return result
}

func (pm *Premiumize) GetTorrents() ([]*types.Torrent, error) {
	transfers, err := pm.listTransfers()
	if err != nil {
		return nil, err
	}
	torrents := make([]*types.Torrent, 0, len(transfers))
	for _, tr := range transfers {
		torrent, err := pm.transferToTorrent(tr, "")
		if err != nil {
			pm.logger.Warn().Err(err).Str("transfer_id", tr.ID).Msg("Skipping Premiumize transfer")
			continue
		}
		if torrent.Status == types.TorrentStatusDownloaded {
			torrents = append(torrents, torrent)
		}
	}
	return torrents, nil
}

func (pm *Premiumize) listTransfers() ([]premiumizeTransfer, error) {
	var data transferListResponse
	req, err := http.NewRequest(http.MethodGet, pm.endpoint("/api/transfer/list"), nil)
	if err != nil {
		return nil, err
	}
	if _, err := pm.do(req, &data); err != nil {
		return nil, err
	}
	return data.Transfers, nil
}

func (pm *Premiumize) transferToTorrent(tr premiumizeTransfer, fallbackInfoHash string) (*types.Torrent, error) {
	status := mapStatus(tr.Status)
	files := make(map[string]types.File)
	var links []string
	if status == types.TorrentStatusDownloaded {
		var err error
		var ready bool
		files, links, ready, err = pm.filesForTransfer(tr)
		if err != nil {
			return nil, err
		}
		// Premiumize can mark a transfer finished before its folder tree has
		// usable file links. Keep it active until linked files are visible.
		if !ready {
			status = types.TorrentStatusDownloading
		}
	}
	size := int64(0)
	for _, file := range files {
		size += file.Size
	}
	name := cmp.Or(tr.Name, tr.ID)
	added := time.Unix(tr.Created.Unix, 0)
	if tr.Created.Unix == 0 {
		added = time.Time{}
	}
	return &types.Torrent{
		Id:               tr.ID,
		InfoHash:         cmp.Or(utils.ExtractInfoHash(tr.Src), fallbackInfoHash),
		Name:             name,
		Filename:         name,
		OriginalFilename: name,
		Bytes:            size,
		Size:             size,
		Files:            files,
		Status:           status,
		Progress:         normalizeProgress(tr.Progress),
		Links:            links,
		Debrid:           pm.config.Name,
		Added:            added,
	}, nil
}

func (pm *Premiumize) filesForTransfer(tr premiumizeTransfer) (map[string]types.File, []string, bool, error) {
	files := make(map[string]types.File)
	links := make([]string, 0)
	if fileID := tr.FileID.String(); fileID != "" {
		item, err := pm.itemDetails(fileID)
		if err != nil {
			return nil, nil, false, err
		}
		pm.addFile(files, &links, tr.ID, item.Name, item.Name, item.Size, item.ID, item.Link)
		return files, links, item.Link != "", nil
	}
	if folderID := tr.FolderID.String(); folderID != "" {
		linkedFiles, err := pm.addFolderFiles(files, &links, tr.ID, folderID, "")
		if err != nil {
			return nil, nil, false, err
		}
		return files, links, linkedFiles > 0, nil
	}
	return files, links, false, nil
}

func (pm *Premiumize) itemDetails(id string) (*itemDetailsResponse, error) {
	var data itemDetailsResponse
	req, err := http.NewRequest(http.MethodGet, pm.endpoint("/api/item/details?id="+url.QueryEscape(id)), nil)
	if err != nil {
		return nil, err
	}
	if _, err := pm.do(req, &data); err != nil {
		return nil, err
	}
	return &data, nil
}

func (pm *Premiumize) addFolderFiles(files map[string]types.File, links *[]string, transferID, folderID, prefix string) (int, error) {
	var data folderListResponse
	req, err := http.NewRequest(http.MethodGet, pm.endpoint("/api/folder/list?id="+url.QueryEscape(folderID)), nil)
	if err != nil {
		return 0, err
	}
	if _, err := pm.do(req, &data); err != nil {
		return 0, err
	}
	// Count links from the full recursive tree, not just files that pass
	// Decypharr's extension/size filters, so readiness reflects Premiumize.
	linkedFiles := 0
	for _, item := range data.Content {
		itemPath := path.Join(prefix, item.Name)
		if item.Type == "folder" {
			n, err := pm.addFolderFiles(files, links, transferID, item.ID, itemPath)
			if err != nil {
				return 0, err
			}
			linkedFiles += n
			continue
		}
		if item.Link != "" {
			linkedFiles++
		}
		pm.addFile(files, links, transferID, item.Name, itemPath, item.Size, item.ID, item.Link)
	}
	return linkedFiles, nil
}

func (pm *Premiumize) addFile(files map[string]types.File, links *[]string, transferID, name, itemPath string, size int64, id, link string) {
	if link == "" {
		return
	}
	if itemPath == "" {
		itemPath = name
	}
	if pm.isFileAllowed != nil {
		if err := pm.isFileAllowed(itemPath, size); err != nil {
			return
		}
	} else if filepath.Ext(itemPath) == "" {
		return
	}
	fileName := filepath.Base(itemPath)
	if _, exists := files[fileName]; exists {
		fileName = itemPath
	}
	files[fileName] = types.File{
		TorrentId: transferID,
		Id:        id,
		Name:      fileName,
		Path:      itemPath,
		Size:      size,
		Link:      link,
	}
	*links = append(*links, link)
}

func (pm *Premiumize) GetDownloadLink(id string, file *types.File) (types.DownloadLink, error) {
	return pm.accountsManager.GetDownloadLink(id, file, pm.fetchDownloadLink)
}

func (pm *Premiumize) fetchDownloadLink(acc *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	link := file.Link
	size := file.Size
	filename := file.Name
	if link == "" && file.Id != "" {
		item, err := pm.itemDetails(file.Id)
		if err != nil {
			return types.DownloadLink{}, err
		}
		link = item.Link
		size = item.Size
		filename = item.Name
	}
	if link == "" {
		return types.DownloadLink{}, customerror.HosterUnavailableError
	}
	now := time.Now()
	return types.DownloadLink{
		Debrid:       pm.config.Name,
		Token:        acc.Token,
		Filename:     filename,
		Size:         size,
		Link:         link,
		DownloadLink: link,
		Generated:    now,
		ExpiresAt:    now.Add(pm.autoExpiresLinksAfter),
		Id:           file.Id,
	}, nil
}

func (pm *Premiumize) RefreshDownloadLinks() error {
	return pm.accountsManager.RefreshLinks(func(account *account.Account) ([]types.DownloadLink, error) {
		return []types.DownloadLink{}, nil
	})
}

func (pm *Premiumize) CheckFile(ctx context.Context, infohash, fileID string) error {
	if strings.HasPrefix(fileID, "http://") || strings.HasPrefix(fileID, "https://") {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, fileID, nil)
		if err != nil {
			return err
		}
		resp, err := pm.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return customerror.HosterUnavailableError
		}
		if resp.StatusCode >= 400 {
			return fmt.Errorf("premiumize link check failed: Status: %d", resp.StatusCode)
		}
		return nil
	}
	if fileID == "" {
		return customerror.HosterUnavailableError
	}
	if _, err := pm.itemDetails(fileID); err != nil {
		return err
	}
	return nil
}

func (pm *Premiumize) GetProfile() (*types.Profile, error) {
	if pm.profile != nil && time.Since(pm.profileLastFetched) < profileCacheDuration {
		return pm.profile, nil
	}
	profile, err := pm.getClientProfile(pm.client)
	if err != nil {
		return nil, err
	}
	pm.profile = profile
	pm.profileLastFetched = time.Now()
	return profile, nil
}

func (pm *Premiumize) getClientProfile(client *request.Client) (*types.Profile, error) {
	var data accountInfoResponse
	req, err := http.NewRequest(http.MethodGet, pm.endpoint("/api/account/info"), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("premiumize API error: Status: %d || Body: %s", resp.StatusCode, string(body))
	}
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	if data.Status == "error" {
		return nil, fmt.Errorf("premiumize API error: %s (%s)", data.Message, data.Code)
	}
	expiration := time.Time{}
	premium := int64(0)
	accountType := "free"
	if data.PremiumUntil != nil {
		premium = *data.PremiumUntil
		expiration = time.Unix(*data.PremiumUntil, 0)
		if premium > time.Now().Unix() {
			accountType = "premium"
		}
	}
	return &types.Profile{
		Name:       pm.config.Name,
		Id:         data.customerIDInt64(),
		Username:   strconv.FormatInt(data.customerIDInt64(), 10),
		Points:     data.BoosterPoints,
		Premium:    premium,
		Expiration: expiration,
		Type:       accountType,
	}, nil
}

func (pm *Premiumize) GetAvailableSlots() (int, error) {
	// Premiumize does not provide active slot info without listing transfers.
	return config.DefaultAvailableSlots, nil
}

func (pm *Premiumize) AccountManager() *account.Manager {
	return pm.accountsManager
}

func (pm *Premiumize) SyncAccounts() {
	pm.accountsManager.Sync(pm.syncAccount)
}

func (pm *Premiumize) syncAccount(acc *account.Account) error {
	profile, err := pm.getClientProfile(acc.Client())
	if err != nil {
		return err
	}
	acc.Username = profile.Username
	acc.Expiration = profile.Expiration
	return nil
}

func (pm *Premiumize) DeleteLink(downloadLink types.DownloadLink) error {
	return pm.accountsManager.DeleteDownloadLink(downloadLink, func(account *account.Account, dl types.DownloadLink) error {
		// Premiumize exposes item and transfer deletion, but not a safe
		// generated-link deletion endpoint. Deleting dl.Id here would delete
		// the user's cloud file, not just invalidate this cached CDN link.
		return nil
	})
}

func (pm *Premiumize) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: pm.config.Name,
		TestedAt: time.Now(),
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pm.endpoint("/api/account/info"), nil)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	resp, err := pm.client.Do(req)
	latency := time.Since(start)
	if err != nil {
		result.Error = fmt.Sprintf("latency test failed: %v", err)
		return result
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		result.Error = fmt.Sprintf("latency test unexpected status: %d", resp.StatusCode)
		return result
	}
	result.LatencyMs = latency.Milliseconds()

	current := pm.accountsManager.Current()
	if current == nil {
		return result
	}
	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, link.DownloadLink, nil)
	if err != nil {
		return result
	}
	req.Header.Set("Range", "bytes=0-1048575")
	downloadStart := time.Now()
	dlResp, err := current.Client().Do(req)
	if err != nil {
		return result
	}
	defer dlResp.Body.Close()
	data, err := io.ReadAll(dlResp.Body)
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

func (pm *Premiumize) SupportsCheck() bool {
	return true
}

func mapStatus(status string) types.TorrentStatus {
	switch strings.ToLower(status) {
	case "finished", "seeding":
		return types.TorrentStatusDownloaded
	case "queued":
		return types.TorrentStatusQueued
	case "running":
		return types.TorrentStatusDownloading
	default:
		return types.TorrentStatusError
	}
}

func normalizeProgress(progress float64) float64 {
	if progress <= 1 {
		return progress * 100
	}
	return progress
}
