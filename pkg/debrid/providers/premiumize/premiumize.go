package premiumize

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/common"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"
)

// Premiumize implements common.Client against the premiumize.me API.
//
// Premiumize differs from the torrent-centric debrid services (Real-Debrid,
// Debrid-Link) in that completed transfers land in cloud "folders" rather than
// addressable torrents. We lean on /transfer/directdl, which for a *cached*
// magnet returns the file tree with direct links in a single call — mirroring
// the inline-link model the other providers use. Uncached magnets fall back to
// /transfer/create (queued) when download_uncached is enabled.
type Premiumize struct {
	Host             string
	APIKey           string
	accountsManager  *account.Manager
	DownloadUncached bool
	client           *request.Client

	autoExpiresLinksAfter time.Duration
	logger                zerolog.Logger
	config                config.Debrid

	Profile *types.Profile
}

// Compile-time guarantee that *Premiumize satisfies the debrid client interface.
var _ common.Client = (*Premiumize)(nil)

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Premiumize, error) {
	cfg := config.Get()
	headers := map[string]string{}
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

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	p := &Premiumize{
		Host:                  "https://www.premiumize.me/api",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], log),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		logger:                log,
		config:                dc,
	}
	return p, nil
}

func (p *Premiumize) Config() config.Debrid    { return p.config }
func (p *Premiumize) Logger() zerolog.Logger    { return p.logger }
func (p *Premiumize) SupportsCheck() bool        { return true }
func (p *Premiumize) AccountManager() *account.Manager { return p.accountsManager }

// doRequest performs an API call. Premiumize authenticates via the `apikey`
// query parameter on every request (GET and POST alike), so params are always
// carried in the query string with an empty body.
func (p *Premiumize) doRequest(method, endpoint string, params url.Values, result interface{}) (*http.Response, error) {
	u, err := url.Parse(p.Host + endpoint)
	if err != nil {
		return nil, err
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("apikey", p.APIKey)
	u.RawQuery = params.Encode()

	req, err := http.NewRequest(method, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
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

func (p *Premiumize) IsAvailable(hashes []string) map[string]bool {
	result := make(map[string]bool)
	for i := 0; i < len(hashes); i += 100 {
		end := i + 100
		if end > len(hashes) {
			end = len(hashes)
		}
		params := url.Values{}
		valid := make([]string, 0, end-i)
		for _, h := range hashes[i:end] {
			if h != "" {
				params.Add("items[]", h)
				valid = append(valid, h)
			}
		}
		if len(valid) == 0 {
			continue
		}
		var res cacheCheckResponse
		resp, err := p.doRequest(http.MethodGet, "/cache/check", params, &res)
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || res.Status != "success" {
			continue
		}
		for idx, h := range valid {
			if idx < len(res.Response) && res.Response[idx] {
				result[h] = true
			}
		}
	}
	return result
}

// populateFromDirectDL fills a torrent's files from a cached /transfer/directdl response.
func (p *Premiumize) populateFromDirectDL(t *types.Torrent, dd directDLResponse) {
	if t.Files == nil {
		t.Files = make(map[string]types.File)
	}
	if t.InfoHash == "" && t.Magnet != nil {
		t.InfoHash = t.Magnet.InfoHash
	}
	if t.Id == "" {
		t.Id = t.InfoHash
	}
	// directdl returns no clean torrent name — prefer the shared content root
	// folder, then the magnet display-name, then whatever the caller set.
	name := contentRoot(dd.Content)
	if name == "" {
		switch {
		case t.Magnet != nil && t.Magnet.Name != "":
			name = t.Magnet.Name
		case t.Name != "":
			name = t.Name
		case len(dd.Content) == 1:
			name = path.Base(dd.Content[0].Path)
		default:
			name = dd.Filename
		}
	}
	name = utils.RemoveInvalidChars(name)
	t.Name = name
	t.Filename = name
	t.OriginalFilename = name
	t.Status = types.TorrentStatusDownloaded
	t.Debrid = p.config.Name
	if t.Added.IsZero() {
		t.Added = time.Now()
	}

	cfg := config.Get()
	now := time.Now()
	var total int64
	for _, f := range dd.Content {
		fname := path.Base(f.Path)
		if fname == "" || fname == "." || fname == "/" {
			fname = f.Path
		}
		if f.Link == "" {
			continue
		}
		if err := cfg.IsFileAllowed(fname, int64(f.Size)); err != nil {
			continue
		}
		file := types.File{
			TorrentId: t.Id,
			Id:        fname,
			Name:      fname,
			Size:      int64(f.Size),
			Path:      f.Path,
			Link:      f.Link,
			Generated: now,
		}
		link := p.newDownloadLink(fname, f.Link, int64(f.Size), now)
		file.DownloadLink = link
		t.Files[fname] = file
		p.accountsManager.StoreDownloadLink(link)
		total += int64(f.Size)
	}
	t.Bytes = total
	if t.Size == 0 {
		t.Size = total
	}
}

func (p *Premiumize) newDownloadLink(filename, link string, size int64, now time.Time) types.DownloadLink {
	return types.DownloadLink{
		Debrid:       p.config.Name,
		Token:        p.APIKey,
		Filename:     filename,
		Link:         link,
		DownloadLink: link,
		Size:         size,
		Generated:    now,
		ExpiresAt:    now.Add(p.autoExpiresLinksAfter),
	}
}

func (p *Premiumize) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	if t.Magnet == nil || t.Magnet.Link == "" {
		return nil, fmt.Errorf("missing magnet link")
	}
	if t.Files == nil {
		t.Files = make(map[string]types.File)
	}

	// Instant cached path: directdl returns the file tree with direct links.
	params := url.Values{}
	params.Set("src", t.Magnet.Link)
	var dd directDLResponse
	resp, err := p.doRequest(http.MethodPost, "/transfer/directdl", params, &dd)
	if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && dd.Status == "success" && len(dd.Content) > 0 {
		p.populateFromDirectDL(t, dd)
		return t, nil
	}

	// Not instantly cached.
	if !t.DownloadUncached {
		return nil, fmt.Errorf("torrent: %s not cached", t.Name)
	}

	// Queue an uncached transfer.
	cParams := url.Values{}
	cParams.Set("src", t.Magnet.Link)
	var cr transferCreateResponse
	resp, err = p.doRequest(http.MethodPost, "/transfer/create", cParams, &cr)
	if err != nil {
		return nil, err
	}
	if cr.Status != "success" {
		return nil, fmt.Errorf("error adding torrent: %s", cmp.Or(cr.Message, "unknown error"))
	}
	t.Id = cr.ID
	name := utils.RemoveInvalidChars(cmp.Or(cr.Name, t.Name))
	t.Name = name
	t.Filename = name
	t.OriginalFilename = name
	t.Status = types.TorrentStatusDownloading
	t.Debrid = p.config.Name
	t.Added = time.Now()
	return t, nil
}

func (p *Premiumize) findTransfer(id string) (*transferItem, error) {
	var res transferListResponse
	resp, err := p.doRequest(http.MethodGet, "/transfer/list", nil, &res)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("premiumize API error: status %d", resp.StatusCode)
	}
	for i := range res.Transfers {
		if res.Transfers[i].ID == id {
			return &res.Transfers[i], nil
		}
	}
	return nil, nil
}

// populateFromFolder recursively walks a Premiumize folder, collecting files.
func (p *Premiumize) populateFromFolder(t *types.Torrent, folderID string) {
	if t.Files == nil {
		t.Files = make(map[string]types.File)
	}
	cfg := config.Get()
	now := time.Now()
	var total int64

	var walk func(id, prefix string)
	walk = func(id, prefix string) {
		params := url.Values{}
		params.Set("id", id)
		var res folderListResponse
		resp, err := p.doRequest(http.MethodGet, "/folder/list", params, &res)
		if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return
		}
		for _, it := range res.Content {
			if it.Type == "folder" {
				walk(it.ID, path.Join(prefix, it.Name))
				continue
			}
			if it.Link == "" {
				continue
			}
			if err := cfg.IsFileAllowed(it.Name, int64(it.Size)); err != nil {
				continue
			}
			file := types.File{
				TorrentId: t.Id,
				Id:        it.ID,
				Name:      it.Name,
				Size:      int64(it.Size),
				Path:      path.Join(prefix, it.Name),
				Link:      it.Link,
				Generated: now,
			}
			link := p.newDownloadLink(it.Name, it.Link, int64(it.Size), now)
			file.DownloadLink = link
			t.Files[it.Name] = file
			p.accountsManager.StoreDownloadLink(link)
			total += int64(it.Size)
		}
	}
	walk(folderID, "")
	if total > 0 {
		t.Bytes = total
		if t.Size == 0 {
			t.Size = total
		}
	}
}

func (p *Premiumize) populateFromFile(t *types.Torrent, fileID string) {
	if t.Files == nil {
		t.Files = make(map[string]types.File)
	}
	params := url.Values{}
	params.Set("id", fileID)
	var it folderItem
	resp, err := p.doRequest(http.MethodGet, "/item/details", params, &it)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 || it.Link == "" {
		return
	}
	now := time.Now()
	file := types.File{
		TorrentId: t.Id,
		Id:        it.ID,
		Name:      it.Name,
		Size:      int64(it.Size),
		Path:      it.Name,
		Link:      it.Link,
		Generated: now,
	}
	link := p.newDownloadLink(it.Name, it.Link, int64(it.Size), now)
	file.DownloadLink = link
	t.Files[it.Name] = file
	p.accountsManager.StoreDownloadLink(link)
	t.Bytes = int64(it.Size)
	if t.Size == 0 {
		t.Size = int64(it.Size)
	}
}

func (p *Premiumize) UpdateTorrent(t *types.Torrent) error {
	// Cached path: re-derive files/links via directdl from the stored magnet.
	if t.Magnet != nil && t.Magnet.Link != "" {
		params := url.Values{}
		params.Set("src", t.Magnet.Link)
		var dd directDLResponse
		resp, err := p.doRequest(http.MethodPost, "/transfer/directdl", params, &dd)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 && dd.Status == "success" && len(dd.Content) > 0 {
			p.populateFromDirectDL(t, dd)
			return nil
		}
	}

	// Otherwise inspect the queued transfer by id.
	if t.Id != "" {
		ti, err := p.findTransfer(t.Id)
		if err != nil {
			return err
		}
		if ti != nil {
			t.Name = utils.RemoveInvalidChars(cmp.Or(ti.Name, t.Name))
			t.Progress = ti.Progress * 100 // Premiumize reports 0..1
			switch ti.Status {
			case "finished", "seeding":
				t.Status = types.TorrentStatusDownloaded
				if ti.FolderID != "" {
					p.populateFromFolder(t, ti.FolderID)
				} else if ti.FileID != "" {
					p.populateFromFile(t, ti.FileID)
				}
			case "error", "timeout", "deleted", "banned":
				t.Status = types.TorrentStatusError
			default:
				t.Status = types.TorrentStatusDownloading
			}
			return nil
		}
	}
	return fmt.Errorf("torrent not found")
}

func (p *Premiumize) CheckStatus(t *types.Torrent) (*types.Torrent, error) {
	err := p.UpdateTorrent(t)
	if err != nil || t == nil {
		return t, err
	}
	switch t.Status {
	case types.TorrentStatusDownloading:
		if !t.DownloadUncached {
			return t, fmt.Errorf("torrent: %s not cached", t.Name)
		}
		return t, nil
	case types.TorrentStatusDownloaded:
		p.logger.Info().Msgf("Torrent: %s downloaded", t.Name)
		return t, nil
	default:
		return t, fmt.Errorf("torrent: %s has error", t.Name)
	}
}

func (p *Premiumize) GetTorrent(torrentId string) (*types.Torrent, error) {
	ti, err := p.findTransfer(torrentId)
	if err != nil {
		return nil, err
	}
	if ti == nil {
		return nil, fmt.Errorf("torrent not found")
	}
	name := utils.RemoveInvalidChars(ti.Name)
	t := &types.Torrent{
		Id:               ti.ID,
		Name:             name,
		Filename:         name,
		OriginalFilename: name,
		Status:           types.TorrentStatusDownloaded,
		Debrid:           p.config.Name,
		Files:            make(map[string]types.File),
		Added:            time.Now(),
	}
	if ti.FolderID != "" {
		p.populateFromFolder(t, ti.FolderID)
	} else if ti.FileID != "" {
		p.populateFromFile(t, ti.FileID)
	}
	return t, nil
}

// GetTorrents enumerates finished transfers. Note: instant (directdl) grabs are
// ephemeral and never become transfers, so decypharr's own storage remains the
// source of truth for those — this returns the queued/uncached library only.
func (p *Premiumize) GetTorrents() ([]*types.Torrent, error) {
	var res transferListResponse
	resp, err := p.doRequest(http.MethodGet, "/transfer/list", nil, &res)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("premiumize API error: status %d", resp.StatusCode)
	}
	torrents := make([]*types.Torrent, 0, len(res.Transfers))
	for _, ti := range res.Transfers {
		if ti.Status != "finished" && ti.Status != "seeding" {
			continue
		}
		name := utils.RemoveInvalidChars(ti.Name)
		t := &types.Torrent{
			Id:               ti.ID,
			Name:             name,
			Filename:         name,
			OriginalFilename: name,
			Status:           types.TorrentStatusDownloaded,
			Debrid:           p.config.Name,
			Files:            make(map[string]types.File),
			Added:            time.Now(),
		}
		if ti.FolderID != "" {
			p.populateFromFolder(t, ti.FolderID)
		} else if ti.FileID != "" {
			p.populateFromFile(t, ti.FileID)
		}
		torrents = append(torrents, t)
	}
	return torrents, nil
}

func (p *Premiumize) DeleteTorrent(torrentId string) error {
	// Best-effort: remove a queued/uncached transfer. Cached directdl grabs have
	// no Premiumize-side object (torrentId is the infohash) so this is a no-op there.
	params := url.Values{}
	params.Set("id", torrentId)
	var res statusResponse
	_, _ = p.doRequest(http.MethodPost, "/transfer/delete", params, &res)
	p.logger.Info().Msgf("Torrent: %s removed from Premiumize", torrentId)
	return nil
}

func (p *Premiumize) fetchDownloadLink(_ *account.Account, _ string, file *types.File) (types.DownloadLink, error) {
	link := file.Link
	if link == "" {
		link = file.DownloadLink.DownloadLink
	}
	if link == "" {
		return types.DownloadLink{}, fmt.Errorf("no download link for file: %s", file.Name)
	}
	return p.newDownloadLink(file.Name, link, file.Size, time.Now()), nil
}

func (p *Premiumize) GetDownloadLink(id string, file *types.File) (types.DownloadLink, error) {
	return p.accountsManager.GetDownloadLink(id, file, p.fetchDownloadLink)
}

// RefreshDownloadLinks is a no-op for Premiumize: direct links are derived
// per-file (via directdl / folder listing) rather than from a global link list,
// so there is nothing to bulk-refresh — and returning an empty set would wipe
// the in-memory store.
func (p *Premiumize) RefreshDownloadLinks() error {
	return nil
}

func (p *Premiumize) CheckFile(_ context.Context, _, _ string) error {
	return nil
}

func (p *Premiumize) GetAvailableSlots() (int, error) {
	// Premiumize has no per-account active-slot concept.
	return config.DefaultAvailableSlots, nil
}

func (p *Premiumize) GetProfile() (*types.Profile, error) {
	if p.Profile != nil {
		return p.Profile, nil
	}
	var res accountInfo
	resp, err := p.doRequest(http.MethodGet, "/account/info", nil, &res)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || res.Status != "success" {
		return nil, fmt.Errorf("premiumize API error getting account info: %s", res.Message)
	}
	expiration := time.Unix(int64(res.PremiumUntil), 0)
	profile := &types.Profile{
		Id:         1,
		Username:   res.CustomerID,
		Name:       p.config.Name,
		Email:      res.CustomerID,
		Premium:    int64(res.PremiumUntil),
		Expiration: expiration,
	}
	if res.PremiumUntil > 0 {
		profile.Type = "premium"
	} else {
		profile.Type = "free"
	}
	if expiration.IsZero() {
		profile.Expiration = time.Now().AddDate(1, 0, 0)
	}
	p.Profile = profile
	return profile, nil
}

func (p *Premiumize) syncAccount(_ *account.Account) error { return nil }

func (p *Premiumize) SyncAccounts() {
	p.accountsManager.Sync(p.syncAccount)
}

func (p *Premiumize) deleteDownloadLink(_ *account.Account, _ types.DownloadLink) error {
	// Premiumize direct links cannot be individually revoked.
	return nil
}

func (p *Premiumize) DeleteLink(dl types.DownloadLink) error {
	return p.accountsManager.DeleteDownloadLink(dl, p.deleteDownloadLink)
}

// SpeedTest measures API latency and download speed using a cached link.
func (p *Premiumize) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: p.config.Name,
		TestedAt: time.Now(),
	}

	start := time.Now()
	resp, err := p.doRequest(http.MethodGet, "/account/info", nil, nil)
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

	current := p.accountsManager.Current()
	if current == nil {
		return result
	}
	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result
	}

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
