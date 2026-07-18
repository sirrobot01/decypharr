package realdebrid

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/common/rar"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"go.uber.org/ratelimit"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
)

const (
	profileCacheDuration = 1 * time.Hour
)

type RealDebrid struct {
	Host string `json:"host"`

	APIKey                string
	accountsManager       *account.Manager
	client                *request.Client
	repairClient          *request.Client
	autoExpiresLinksAfter time.Duration
	logger                zerolog.Logger

	rarSemaphore       chan struct{}
	Profile            *types.Profile
	profileLastFetched time.Time
	config             config.Debrid
	retries            int
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*RealDebrid, error) {
	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	if dc.UserAgent != "" {
		headers["User-Agent"] = dc.UserAgent
	}
	_log := logger.New(dc.Name)

	autoExpiresLinksAfter, err := utils.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	cfg := config.Get()

	opts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithMaxRetries(cfg.Retries),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithRetryableStatus(http.StatusTooManyRequests),
		request.WithProxy(dc.Proxy),
	}

	repairOpts := []request.ClientOption{
		request.WithHeaders(headers),
		request.WithLogger(_log),
		request.WithMaxRetries(4),
		request.WithRetryableStatus(429),
		request.WithRateLimiter(ratelimits["repair"]),
		request.WithProxy(dc.Proxy),
	}

	r := &RealDebrid{
		Host:                  "https://api.real-debrid.com/rest/1.0",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                request.New(opts...),
		repairClient:          request.New(repairOpts...),
		logger:                logger.New(dc.Name),
		rarSemaphore:          make(chan struct{}, 2),
		config:                dc,
		retries:               cfg.Retries,
	}

	go func() {
		_, err = r.GetProfile()
		if err != nil {
			r.logger.Error().Err(err).Msg("Failed to get RealDebrid profile")
		}
	}()
	return r, nil
}

func (r *RealDebrid) Logger() zerolog.Logger {
	return r.logger
}

// doGet performs a GET request using the main client
func (r *RealDebrid) doGet(endpoint string, result any) (*http.Response, error) {
	u, err := url.Parse(r.Host + endpoint)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := r.client.Do(req)
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

// doPost performs a POST request with form data
func (r *RealDebrid) doPostForm(endpoint string, formData map[string]string, result any) (*http.Response, error) {
	form := url.Values{}
	for k, v := range formData {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, r.Host+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := r.client.Do(req)
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

// doPut performs a PUT request with body
func (r *RealDebrid) doPut(endpoint string, body []byte, contentType string, result any) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequest(http.MethodPut, r.Host+endpoint, bodyReader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	resp, err := r.client.Do(req)
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

// doGetWithClient performs a GET using a specific client
func (r *RealDebrid) doGetWithClient(client *request.Client, fullURL string, queryParams map[string]string, result any) (*http.Response, error) {
	u, err := url.Parse(fullURL)
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

	resp, err := client.Do(req)
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

// doPostFormWithClient performs a POST with form data using a specific client
func (r *RealDebrid) doPostFormWithClient(client *request.Client, fullURL string, formData map[string]string, result any, errorResult any) (*http.Response, error) {
	form := url.Values{}
	for k, v := range formData {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, fullURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if result != nil && resp.ContentLength != 0 {
			if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(result); err != nil {
				return resp, err
			}
		}
	} else {
		if errorResult != nil && resp.ContentLength != 0 {
			if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(errorResult); err != nil {
				return resp, err
			}
		}
	}

	return resp, nil
}

func (r *RealDebrid) getSelectedFiles(t *types.Torrent, data torrentInfo) (map[string]types.File, error) {
	files := make(map[string]types.File)
	selectedFiles := make([]types.File, 0)

	for _, f := range data.Files {
		if f.Selected == 1 {
			selectedFiles = append(selectedFiles, types.File{
				TorrentId: t.Id,
				Name:      filepath.Base(f.Path),
				Path:      filepath.Base(f.Path),
				Size:      f.Bytes,
				Id:        strconv.Itoa(f.ID),
			})
		}
	}

	if len(selectedFiles) == 0 {
		return files, nil
	}

	// Handle RARed torrents (single link, multiple files)
	if len(data.Links) == 1 && len(selectedFiles) > 1 {
		return r.handleRarArchive(t, data, selectedFiles)
	}

	// Standard case - map files to links
	if len(selectedFiles) > len(data.Links) {
		return files, nil
	}

	for i, f := range selectedFiles {
		if i < len(data.Links) {
			f.Link = data.Links[i]
			files[f.Name] = f
		}
	}

	return files, nil
}

func (r *RealDebrid) handleRarFallback(t *types.Torrent, data torrentInfo) map[string]types.File {
	files := make(map[string]types.File)
	file := types.File{
		TorrentId: t.Id,
		Id:        "0",
		Name:      t.Name + ".rar",
		Size:      data.Bytes,
		IsRar:     true,
		ByteRange: nil,
		Path:      t.Name + ".rar",
		Link:      data.Links[0],
		Generated: time.Now(),
	}
	files[file.Name] = file
	return files
}

// handleRarArchive processes RAR archives with multiple files
func (r *RealDebrid) handleRarArchive(t *types.Torrent, data torrentInfo, selectedFiles []types.File) (map[string]types.File, error) {
	// This will block if 2 RAR operations are already in progress
	r.rarSemaphore <- struct{}{}
	defer func() {
		<-r.rarSemaphore
	}()

	files := make(map[string]types.File)

	if !r.config.UnpackRar {
		r.logger.Debug().Msgf("RAR file detected, but unpacking is disabled: %s. Falling back to single file representation.", t.Name)
		return r.handleRarFallback(t, data), nil
	}

	r.logger.Info().Msgf("RAR file detected, unpacking: %s", t.Name)
	linkFile := &types.File{TorrentId: t.Id, Link: data.Links[0]}
	downloadLinkObj, err := r.GetDownloadLink(t.Id, linkFile)

	if err != nil {
		r.logger.Debug().Err(err).Msgf("Error getting download link for RAR file: %s. Falling back to single file representation.", t.Name)
		return r.handleRarFallback(t, data), nil
	}

	dlLink := downloadLinkObj.DownloadLink
	reader, err := rar.NewReader(dlLink)

	if err != nil {
		r.logger.Debug().Err(err).Msgf("Error creating RAR reader for %s. Falling back to single file representation.", t.Name)
		return r.handleRarFallback(t, data), nil
	}

	rarFiles, err := reader.GetFiles()

	if err != nil {
		r.logger.Debug().Err(err).Msgf("Error reading RAR files for %s. Falling back to single file representation.", t.Name)
		return r.handleRarFallback(t, data), nil
	}

	// Create lookup map for faster matching
	fileMap := make(map[string]*types.File)
	for i := range selectedFiles {
		// RD converts special chars to '_' for RAR file paths
		safeName := strings.NewReplacer("|", "_", "\"", "_", "\\", "_", "?", "_", "*", "_", ":", "_", "<", "_", ">", "_").Replace(selectedFiles[i].Name)
		fileMap[safeName] = &selectedFiles[i]
	}

	now := time.Now()

	for _, rarFile := range rarFiles {
		if file, exists := fileMap[rarFile.Name()]; exists {
			file.IsRar = true
			file.ByteRange = rarFile.ByteRange()
			file.Link = data.Links[0]
			file.Generated = now
			files[file.Name] = *file
		} else if !rarFile.IsDirectory {
			r.logger.Warn().Msgf("RAR file %s not found in torrent files", rarFile.Name())
		}
	}
	if len(files) == 0 {
		r.logger.Warn().Msgf("No valid files found in RAR archive for torrent: %s", t.Name)
		return r.handleRarFallback(t, data), nil
	}
	r.logger.Info().Msgf("Unpacked RAR archive for torrent: %s with %d files", t.Name, len(files))
	return files, nil
}

func (r *RealDebrid) getTorrentFiles(t *types.Torrent, data torrentInfo) map[string]types.File {
	files := make(map[string]types.File)
	cfg := config.Get()
	idx := 0

	for _, f := range data.Files {
		name := filepath.Base(f.Path)
		if err := cfg.IsFileAllowed(name, f.Bytes); err != nil {
			continue
		}

		file := types.File{
			TorrentId: t.Id,
			Name:      name,
			Path:      name,
			Size:      f.Bytes,
			Id:        strconv.Itoa(f.ID),
		}
		files[name] = file
		idx++
	}
	return files
}

func (r *RealDebrid) IsAvailable(hashes []string) map[string]bool {
	result := make(map[string]bool)

	for i := 0; i < len(hashes); i += 200 {
		end := min(i+200, len(hashes))

		validHashes := make([]string, 0, end-i)
		for _, hash := range hashes[i:end] {
			if hash != "" {
				validHashes = append(validHashes, hash)
			}
		}

		if len(validHashes) == 0 {
			continue
		}

		hashStr := strings.Join(validHashes, "/")
		var data AvailabilityResponse

		resp, err := r.doGet(fmt.Sprintf("/torrents/instantAvailability/%s", hashStr), &data)
		if err != nil {
			r.logger.Error().Err(err).Msg("Error checking availability")
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			for _, h := range hashes[i:end] {
				hosters, exists := data[strings.ToLower(h)]
				if exists && len(hosters.Rd) > 0 {
					result[h] = true
				}
			}
		}
	}
	return result
}

func (r *RealDebrid) SupportsInstantAvailability() bool {
	return true
}

func (r *RealDebrid) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	if t.Magnet.IsTorrent() {
		return r.addTorrent(t)
	}
	return r.addMagnet(t)
}

func (r *RealDebrid) addTorrent(t *types.Torrent) (*types.Torrent, error) {
	var data AddMagnetSchema

	resp, err := r.doPut("/torrents/addTorrent", t.Magnet.File, "application/x-bittorrent", &data)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == 509 {
			return nil, customerror.TooManyActiveDownloadsError
		}
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	t.Id = data.Id
	t.Debrid = r.config.Name
	t.Added = time.Now()

	return t, nil
}

func (r *RealDebrid) addMagnet(t *types.Torrent) (*types.Torrent, error) {
	var data AddMagnetSchema

	formData := map[string]string{"magnet": t.Magnet.Link}
	resp, err := r.doPostForm("/torrents/addMagnet", formData, &data)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		t.Id = data.Id
		t.Debrid = r.config.Name
		t.Added = time.Now()
		return t, nil

	case 509:
		return nil, customerror.TooManyActiveDownloadsError

	default:
		return nil, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}
}

func (r *RealDebrid) GetTorrent(torrentId string) (*types.Torrent, error) {
	var data torrentInfo

	resp, err := r.doGet(fmt.Sprintf("/torrents/info/%s", torrentId), &data)
	if err != nil {
		return nil, err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		addedOn := data.Added
		if addedOn.IsZero() {
			addedOn = time.Now()
		}
		t := &types.Torrent{
			Id:               data.ID,
			Name:             data.Filename,
			Bytes:            data.Bytes,
			Progress:         data.Progress,
			Speed:            data.Speed,
			Seeders:          data.Seeders,
			Added:            addedOn,
			Status:           types.TorrentStatus(data.Status),
			Filename:         data.Filename,
			OriginalFilename: data.OriginalFilename,
			Links:            data.Links,
			Debrid:           r.config.Name,
		}

		t.Files = r.getTorrentFiles(t, data)
		return t, nil
	case http.StatusNotFound:
		return nil, customerror.TorrentNotFoundError

	default:
		return nil, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}
}

func (r *RealDebrid) GetDownloadingStatus() []string {
	return []string{"downloading", "magnet_conversion", "queued", "compressing", "uploading"}
}

func getStatus(status string) types.TorrentStatus {
	switch status {
	case "downloading", "magnet_conversion", "queued", "compressing", "uploading", "waiting_files_selection":
		return types.TorrentStatusDownloading
	case "downloaded":
		return types.TorrentStatusDownloaded
	default:
		return types.TorrentStatusError
	}
}

func (r *RealDebrid) UpdateTorrent(t *types.Torrent) error {
	var data torrentInfo

	resp, err := r.doGet(fmt.Sprintf("/torrents/info/%s", t.Id), &data)
	if err != nil {
		return err
	}

	switch resp.StatusCode {
	case http.StatusOK:
		t.Name = data.Filename
		t.Bytes = data.Bytes
		t.Progress = data.Progress
		t.Status = types.TorrentStatus(data.Status)
		t.Speed = data.Speed
		t.Seeders = data.Seeders
		t.Status = getStatus(data.Status)
		t.Filename = data.Filename
		t.OriginalFilename = data.OriginalFilename
		t.Links = data.Links
		t.Debrid = r.config.Name
		t.Files, _ = r.getSelectedFiles(t, data)

		return nil

	case http.StatusNotFound:
		return customerror.TorrentNotFoundError

	default:
		return fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}
}

func (r *RealDebrid) CheckStatus(t *types.Torrent) (*types.Torrent, error) {
	for {
		time.Sleep(2 * time.Second)

		var data torrentInfo

		resp, err := r.doGet(fmt.Sprintf("/torrents/info/%s", t.Id), &data)
		if err != nil {
			r.logger.Info().Msgf("ERROR Checking file: %v", err)
			return t, err
		}

		if resp.StatusCode != http.StatusOK {
			return t, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
		}

		debridStatus := data.Status
		t.Name = data.Filename
		t.Filename = data.Filename
		t.OriginalFilename = data.OriginalFilename
		t.Bytes = data.Bytes
		t.Progress = data.Progress

		t.Speed = data.Speed
		t.Seeders = data.Seeders
		t.Links = data.Links
		t.Status = getStatus(debridStatus)
		t.Debrid = r.config.Name
		t.Added = data.Added
		if data.Hash != "" {
			t.InfoHash = data.Hash
		}
		if debridStatus == "waiting_files_selection" {
			t.Status = types.TorrentStatusDownloading
			t.Files = r.getTorrentFiles(t, data)
			if len(t.Files) == 0 {
				return t, fmt.Errorf("no valid files found")
			}
			filesId := make([]string, 0)
			for _, f := range t.Files {
				filesId = append(filesId, f.Id)
			}

			selectURL := fmt.Sprintf("/torrents/selectFiles/%s", t.Id)
			selectResp, err := r.doPostForm(selectURL, map[string]string{"files": strings.Join(filesId, ",")}, nil)
			if err != nil {
				return t, err
			}

			if selectResp.StatusCode != http.StatusNoContent {
				if selectResp.StatusCode == 509 {
					return nil, customerror.TooManyActiveDownloadsError
				}
				return t, fmt.Errorf("realdebrid API error: Status: %d", selectResp.StatusCode)
			}
			continue
		} else if debridStatus == "downloaded" {
			t.Status = types.TorrentStatusDownloaded
			t.Files, err = r.getSelectedFiles(t, data)
			if err != nil {
				return t, err
			}

			r.logger.Info().Msgf("Torrent: %s downloaded to RD", t.Name)
			return t, nil
		} else if t.Status == types.TorrentStatusDownloading {
			if !t.DownloadUncached {
				return t, fmt.Errorf("torrent: %s not cached", t.Name)
			}
			return t, nil
		} else {
			r.logger.Warn().
				Str("torrent_id", t.Id).
				Str("debrid_status", debridStatus).
				Str("mapped_status", string(t.Status)).
				Msg("Unexpected debrid status, treating as error")
			return t, fmt.Errorf("torrent: %s has error status: %s", t.Name, debridStatus)
		}
	}
}

func (r *RealDebrid) DeleteTorrent(torrentId string) error {
	req, err := http.NewRequest(http.MethodDelete, r.Host+fmt.Sprintf("/torrents/delete/%s", torrentId), nil)
	if err != nil {
		return err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}
	r.logger.Info().Msgf("Torrent: %s deleted from RD", torrentId)
	return nil
}

func (r *RealDebrid) GetFileDownloadLinks(t *types.Torrent) (map[string]types.DownloadLink, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	files := make(map[string]types.File)
	links := make(map[string]types.DownloadLink)

	_files := t.GetFiles()
	wg.Add(len(_files))

	for _, f := range _files {
		go func(file types.File) {
			defer wg.Done()
			link, err := r.GetDownloadLink(t.Id, &file)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if link.Empty() {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("realdebrid API error: download link not found for file %s", file.Name)
				}
				mu.Unlock()
				return
			}

			file.DownloadLink = link
			mu.Lock()
			files[file.Name] = file
			links[file.Name] = link
			mu.Unlock()
		}(f)
	}

	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}

	t.Files = files
	return links, nil
}

func (r *RealDebrid) CheckFile(ctx context.Context, infohash, link string) error {
	formData := map[string]string{"link": link}

	form := url.Values{}
	for k, v := range formData {
		form.Set(k, v)
	}

	req, err := http.NewRequest(http.MethodPost, r.Host+"/unrestrict/check", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := r.repairClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return customerror.HosterUnavailableError
	}

	return nil
}

func (r *RealDebrid) fetchDownloadLink(account *account.Account, id string, file *types.File) (types.DownloadLink, error) {
	emptyLink := types.DownloadLink{}
	link := file.Link
	if strings.HasPrefix(file.Link, "https://real-debrid.com/d/") && len(file.Link) > 39 {
		link = file.Link[0:39]
	}

	formData := map[string]string{"link": link}
	var errResp ErrorResponse
	var data UnrestrictResponse

	resp, err := r.doPostFormWithClient(account.Client(), fmt.Sprintf("%s/unrestrict/link/", r.Host), formData, &data, &errResp)
	if err != nil {
		return emptyLink, err
	}
	if resp.StatusCode != http.StatusOK {
		switch errResp.ErrorCode {
		case 19, 24, 35:
			return emptyLink, customerror.HosterUnavailableError
		case 23, 34, 36:
			return emptyLink, customerror.TrafficExceededError
		default:
			return emptyLink, fmt.Errorf("realdebrid API error: Status: %d || Code: %d", resp.StatusCode, errResp.ErrorCode)
		}
	}
	if data.Download == "" {
		return emptyLink, fmt.Errorf("realdebrid API error: download link not found")
	}
	now := time.Now()
	dl := types.DownloadLink{
		Debrid:       r.config.Name,
		Token:        account.Token,
		Filename:     data.Filename,
		Size:         data.Filesize,
		Link:         data.Link,
		DownloadLink: data.Download,
		Generated:    now,
		ExpiresAt:    now.Add(r.autoExpiresLinksAfter),
	}
	return dl, nil
}

func (r *RealDebrid) GetDownloadLink(id string, file *types.File) (types.DownloadLink, error) {
	return r.accountsManager.GetDownloadLink(id, file, r.fetchDownloadLink)
}

func (r *RealDebrid) getTorrents(offset int, limit int) (int, []*types.Torrent, error) {
	torrents := make([]*types.Torrent, 0)

	queryParams := make(map[string]string)
	if offset > 0 {
		queryParams["offset"] = fmt.Sprintf("%d", offset)
	}
	if limit > 0 {
		queryParams["limit"] = fmt.Sprintf("%d", limit)
	}

	// Need to get headers, so we create request manually
	u, err := url.Parse(r.Host + "/torrents")
	if err != nil {
		return 0, torrents, err
	}
	q := u.Query()
	for k, v := range queryParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, torrents, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return 0, torrents, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return 0, torrents, nil
	}

	if resp.StatusCode != http.StatusOK {
		return 0, torrents, fmt.Errorf("realdebrid API error: %d", resp.StatusCode)
	}

	var data []TorrentsResponse
	if err := json.ConfigDefault.NewDecoder(resp.Body).Decode(&data); err != nil {
		return 0, torrents, err
	}

	totalItems, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	for _, t := range data {
		if t.Status != "downloaded" {
			continue
		}
		t := &types.Torrent{
			Id:               t.Id,
			Name:             t.Filename,
			Bytes:            t.Bytes,
			Progress:         t.Progress,
			Status:           types.TorrentStatusDownloaded,
			Filename:         t.Filename,
			OriginalFilename: t.Filename,
			Links:            t.Links,
			Files:            make(map[string]types.File),
			InfoHash:         t.Hash,
			Debrid:           r.config.Name,
			Added:            t.Added,
		}
		for _, f := range t.Files {
			t.Files[f.Name] = f
		}
		torrents = append(torrents, t)
	}
	return totalItems, torrents, nil
}

func (r *RealDebrid) GetTorrents() ([]*types.Torrent, error) {
	limit := 1000
	if r.config.Limit != 0 {
		limit = r.config.Limit
	}
	hardLimit := r.config.Limit

	allTorrents := make([]*types.Torrent, 0)
	var fetchError error
	offset := 0
	for {
		_, torrents, err := r.getTorrents(offset, limit)
		if err != nil {
			fetchError = err
			break
		}
		totalTorrents := len(torrents)
		if totalTorrents == 0 {
			break
		}
		allTorrents = append(allTorrents, torrents...)
		offset += totalTorrents
		if hardLimit != 0 && len(allTorrents) >= hardLimit {
			break
		}
	}

	if fetchError != nil {
		return nil, fetchError
	}

	return allTorrents, nil
}

func (r *RealDebrid) RefreshDownloadLinks() error {
	return r.accountsManager.RefreshLinks(r.fetchDownloadLinks)
}

func (r *RealDebrid) fetchDownloadLinks(acc *account.Account) ([]types.DownloadLink, error) {
	links := make([]types.DownloadLink, 0)
	limit := 1000
	offset := 0
	for {
		batchLinks, err := r._getDownloadLinks(acc, offset, limit)
		if err != nil {
			return nil, err
		}
		if len(batchLinks) == 0 {
			break
		}
		links = append(links, batchLinks...)
		offset += len(batchLinks)
	}
	return links, nil
}

func (r *RealDebrid) _getDownloadLinks(acc *account.Account, offset int, limit int) ([]types.DownloadLink, error) {
	var data []DownloadsResponse

	queryParams := map[string]string{
		"limit": fmt.Sprintf("%d", limit),
	}
	if offset > 0 {
		queryParams["offset"] = fmt.Sprintf("%d", offset)
	}

	resp, err := r.doGetWithClient(acc.Client(), fmt.Sprintf("%s/downloads", r.Host), queryParams, &data)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}
	links := make([]types.DownloadLink, 0)
	for _, d := range data {
		links = append(links, types.DownloadLink{
			Debrid:       r.config.Name,
			Token:        acc.Token,
			Filename:     d.Filename,
			Size:         d.Filesize,
			Link:         d.Link,
			DownloadLink: d.Download,
			Generated:    d.Generated,
			ExpiresAt:    d.Generated.Add(r.autoExpiresLinksAfter),
			Id:           d.Id,
		})
	}
	return links, nil
}

func (r *RealDebrid) Config() config.Debrid {
	return r.config
}

func (r *RealDebrid) getClientProfile(client *request.Client) (*types.Profile, error) {
	var data profileResponse

	resp, err := r.doGetWithClient(client, fmt.Sprintf("%s/user", r.Host), nil, &data)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}

	profile := &types.Profile{
		Name:       r.config.Name,
		Id:         data.Id,
		Username:   data.Username,
		Email:      data.Email,
		Points:     data.Points,
		Premium:    data.Premium,
		Expiration: data.Expiration,
		Type:       data.Type,
	}
	return profile, nil
}

func (r *RealDebrid) GetProfile() (*types.Profile, error) {
	if r.Profile != nil && time.Since(r.profileLastFetched) < profileCacheDuration {
		return r.Profile, nil
	}
	profile, err := r.getClientProfile(r.client)
	if err != nil {
		return nil, err
	}
	r.Profile = profile
	r.profileLastFetched = time.Now()
	return profile, nil
}

func (r *RealDebrid) GetAvailableSlots() (int, error) {
	var data AvailableSlotsResponse

	resp, err := r.doGet("/torrents/activeCount", &data)
	if err != nil {
		return 0, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("realdebrid API error: Status: %d", resp.StatusCode)
	}

	return data.TotalSlots - data.ActiveSlots - r.config.MinimumFreeSlot, nil
}

func (r *RealDebrid) AccountManager() *account.Manager {
	return r.accountsManager
}

func (r *RealDebrid) SyncAccounts() {
	r.accountsManager.Sync(r.syncAccount)
}

func (r *RealDebrid) syncAccount(acc *account.Account) error {
	if acc.Token == "" {
		return fmt.Errorf("account %s has no token", acc.Username)
	}
	profile, err := r.getClientProfile(acc.Client())
	if err != nil {
		return fmt.Errorf("error syncing account %s: %w", acc.Username, err)
	}
	acc.Username = profile.Username
	acc.Expiration = profile.Expiration

	var trafficData TrafficResponse
	trafficResp, err := r.doGetWithClient(acc.Client(), fmt.Sprintf("%s/traffic/details", r.Host), nil, &trafficData)
	if err != nil {
		return nil
	}
	if trafficResp.StatusCode != http.StatusOK {
		return nil
	}

	if len(trafficData) == 0 {
		acc.TrafficUsed.Store(0)
	} else {
		today := time.Now().Format(time.DateOnly)
		if todayData, exists := trafficData[today]; exists {
			acc.TrafficUsed.Store(todayData.Bytes)
		}
	}
	return nil
}

func (r *RealDebrid) deleteDownloadLink(account *account.Account, downloadLink types.DownloadLink) error {
	req, err := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/downloads/delete/%s", r.Host, downloadLink.Id), nil)
	if err != nil {
		return err
	}

	if _, err = account.Client().Do(req); err != nil {
		return err
	}
	return nil
}

func (r *RealDebrid) DeleteLink(downloadLink types.DownloadLink) error {
	return r.accountsManager.DeleteDownloadLink(downloadLink, r.deleteDownloadLink)
}

// SpeedTest measures API latency and download speed using cached links
func (r *RealDebrid) SpeedTest(ctx context.Context) types.SpeedTestResult {
	result := types.SpeedTestResult{
		Provider: r.config.Name,
		TestedAt: time.Now(),
	}

	// Measure latency by hitting the user endpoint
	start := time.Now()
	resp, err := r.doGet("/user", nil)
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
	current := r.accountsManager.Current()
	if current == nil {
		return result // Latency only, no cached links
	}

	link, found := current.GetRandomLink()
	if !found || link.DownloadLink == "" {
		return result // Latency only, no cached links
	}

	// Download first 1MB to measure speed
	const downloadSize = 1 * 1024 * 1024 // 1MB
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.DownloadLink, nil)
	if err != nil {
		return result // Return latency, skip speed test
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=0-%d", downloadSize-1))

	downloadStart := time.Now()
	dlResp, err := current.Client().Do(req)
	if err != nil {
		return result // Return latency, skip speed test
	}
	defer dlResp.Body.Close()

	// Read all content
	data, err := io.ReadAll(dlResp.Body)
	downloadDuration := time.Since(downloadStart)

	if err != nil || len(data) == 0 {
		return result // Return latency, skip speed test
	}

	result.BytesRead = int64(len(data))
	if downloadDuration.Seconds() > 0 {
		result.SpeedMBps = float64(result.BytesRead) / downloadDuration.Seconds() / (1024 * 1024)
	}

	return result
}

func (r *RealDebrid) SupportsCheck() bool {
	return true
}
