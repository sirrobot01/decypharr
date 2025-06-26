package realdebrid

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"io"
	"net/http"
	gourl "net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/rar"
)

type RealDebrid struct {
	name string
	Host string `json:"host"`

	APIKey   string
	accounts *types.Accounts

	DownloadUncached      bool
	client                *request.Client
	downloadClient        *request.Client
	repairClient          *request.Client
	autoExpiresLinksAfter time.Duration

	MountPath string
	logger    zerolog.Logger
	UnpackRar bool

	rarSemaphore    chan struct{}
	checkCached     bool
	addSamples      bool
	Profile         *types.Profile
	minimumFreeSlot int // Minimum number of active pots to maintain (used for cached stuffs, etc.)

}

func New(dc config.Debrid) (*RealDebrid, error) {
	rl := request.ParseRateLimit(dc.RateLimit)
	repairRl := request.ParseRateLimit(dc.RepairRateLimit)
	downloadRl := request.ParseRateLimit(dc.DownloadRateLimit)

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
	}
	_log := logger.New(dc.Name)

	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	r := &RealDebrid{
		name:                  "realdebrid",
		Host:                  "https://api.real-debrid.com/rest/1.0",
		APIKey:                dc.APIKey,
		accounts:              types.NewAccounts(dc),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		UnpackRar:             dc.UnpackRar,
		client: request.New(
			request.WithHeaders(headers),
			request.WithRateLimiter(rl),
			request.WithLogger(_log),
			request.WithMaxRetries(10),
			request.WithRetryableStatus(429, 502),
			request.WithProxy(dc.Proxy),
		),
		downloadClient: request.New(
			request.WithRateLimiter(downloadRl),
			request.WithLogger(_log),
			request.WithMaxRetries(10),
			request.WithRetryableStatus(429, 447, 502),
			request.WithProxy(dc.Proxy),
		),
		repairClient: request.New(
			request.WithRateLimiter(repairRl),
			request.WithHeaders(headers),
			request.WithLogger(_log),
			request.WithMaxRetries(4),
			request.WithRetryableStatus(429, 502),
			request.WithProxy(dc.Proxy),
		),
		MountPath:       dc.Folder,
		logger:          logger.New(dc.Name),
		rarSemaphore:    make(chan struct{}, 2),
		checkCached:     dc.CheckCached,
		addSamples:      dc.AddSamples,
		minimumFreeSlot: dc.MinimumFreeSlot,
	}

	if _, err := r.GetProfile(); err != nil {
		return nil, err
	} else {
		return r, nil
	}
}

func (r *RealDebrid) Name() string {
	return r.name
}

func (r *RealDebrid) Logger() zerolog.Logger {
	return r.logger
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
		r.logger.Warn().Msgf("More files than links available: %d files, %d links for %s", len(selectedFiles), len(data.Links), t.Name)
	}

	for i, f := range selectedFiles {
		if i < len(data.Links) {
			f.Link = data.Links[i]
			files[f.Name] = f
		} else {
			r.logger.Warn().Str("file", f.Name).Msg("No link available for file")
		}
	}

	return files, nil
}

// handleRarArchive processes RAR archives with multiple files
func (r *RealDebrid) handleRarArchive(t *types.Torrent, data torrentInfo, selectedFiles []types.File) (map[string]types.File, error) {
	// This will block if 2 RAR operations are already in progress
	r.rarSemaphore <- struct{}{}
	defer func() {
		<-r.rarSemaphore
	}()

	files := make(map[string]types.File)

	if !r.UnpackRar {
		r.logger.Debug().Msgf("RAR file detected, but unpacking is disabled: %s", t.Name)
		// Create a single file representing the RAR archive
		file := types.File{
			TorrentId: t.Id,
			Id:        "0",
			Name:      t.Name + ".rar",
			Size:      0,
			IsRar:     true,
			ByteRange: nil,
			Path:      t.Name + ".rar",
			Link:      data.Links[0],
			Generated: time.Now(),
		}
		files[file.Name] = file
		return files, nil
	}

	r.logger.Info().Msgf("RAR file detected, unpacking: %s", t.Name)
	linkFile := &types.File{TorrentId: t.Id, Link: data.Links[0]}
	downloadLinkObj, err := r.GetDownloadLink(t, linkFile)

	if err != nil {
		return nil, fmt.Errorf("failed to get download link for RAR file: %w", err)
	}

	dlLink := downloadLinkObj.DownloadLink
	reader, err := rar.NewReader(dlLink)

	if err != nil {
		return nil, fmt.Errorf("failed to create RAR reader: %w", err)
	}

	rarFiles, err := reader.GetFiles()

	if err != nil {
		return nil, fmt.Errorf("failed to read RAR files: %w", err)
	}

	// Create lookup map for faster matching
	fileMap := make(map[string]*types.File)
	for i := range selectedFiles {
		// RD converts special chars to '_' for RAR file paths
		// @TODO: there might be more special chars to replace
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

	return files, nil
}

// getTorrentFiles returns a list of torrent files from the torrent info
// validate is used to determine if the files should be validated
// if validate is false, selected files will be returned
func (r *RealDebrid) getTorrentFiles(t *types.Torrent, data torrentInfo) map[string]types.File {
	files := make(map[string]types.File)
	cfg := config.Get()
	idx := 0

	for _, f := range data.Files {
		name := filepath.Base(f.Path)
		if !r.addSamples && utils.IsSampleFile(f.Path) {
			// Skip sample files
			continue
		}

		if !cfg.IsAllowedFile(name) {
			continue
		}
		if !cfg.IsSizeAllowed(f.Bytes) {
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
	// Check if the infohashes are available in the local cache
	result := make(map[string]bool)

	// Divide hashes into groups of 100
	for i := 0; i < len(hashes); i += 200 {
		end := i + 200
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

		hashStr := strings.Join(validHashes, "/")
		url := fmt.Sprintf("%s/torrents/instantAvailability/%s", r.Host, hashStr)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := r.client.MakeRequest(req)
		if err != nil {
			r.logger.Error().Err(err).Msgf("Error checking availability")
			return result
		}
		var data AvailabilityResponse
		err = json.Unmarshal(resp, &data)
		if err != nil {
			r.logger.Error().Err(err).Msgf("Error marshalling availability")
			return result
		}
		for _, h := range hashes[i:end] {
			hosters, exists := data[strings.ToLower(h)]
			if exists && len(hosters.Rd) > 0 {
				result[h] = true
			}
		}
	}
	return result
}

func (r *RealDebrid) SubmitMagnet(t *types.Torrent) (*types.Torrent, error) {
	if t.Magnet.IsTorrent() {
		return r.addTorrent(t)
	}
	return r.addMagnet(t)
}

func (r *RealDebrid) addTorrent(t *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/torrents/addTorrent", r.Host)
	var data AddMagnetSchema
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(t.Magnet.File))

	if err != nil {
		return nil, err
	}
	req.Header.Add("Content-Type", "application/x-bittorrent")
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// Handle multiple_downloads

		if resp.StatusCode == 509 {
			return nil, utils.TooManyActiveDownloadsError
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("realdebrid API error: Status: %d || Body: %s", resp.StatusCode, string(bodyBytes))
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if err = json.Unmarshal(bodyBytes, &data); err != nil {
		return nil, err
	}
	t.Id = data.Id
	t.Debrid = r.name
	t.MountPath = r.MountPath
	return t, nil
}

func (r *RealDebrid) addMagnet(t *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/torrents/addMagnet", r.Host)
	payload := gourl.Values{
		"magnet": {t.Magnet.Link},
	}
	var data AddMagnetSchema
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(payload.Encode()))
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// Handle multiple_downloads

		if resp.StatusCode == 509 {
			return nil, utils.TooManyActiveDownloadsError
		}

		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("realdebrid API error: Status: %d || Body: %s", resp.StatusCode, string(bodyBytes))
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if err = json.Unmarshal(bodyBytes, &data); err != nil {
		return nil, err
	}
	t.Id = data.Id
	t.Debrid = r.name
	t.MountPath = r.MountPath
	return t, nil
}

func (r *RealDebrid) GetTorrent(torrentId string) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/torrents/info/%s", r.Host, torrentId)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return nil, utils.TorrentNotFoundError
		}
		return nil, fmt.Errorf("realdebrid API error: Status: %d || Body: %s", resp.StatusCode, string(bodyBytes))
	}
	var data torrentInfo
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		return nil, err
	}
	t := &types.Torrent{
		Id:               data.ID,
		Name:             data.Filename,
		Bytes:            data.Bytes,
		Folder:           data.OriginalFilename,
		Progress:         data.Progress,
		Speed:            data.Speed,
		Seeders:          data.Seeders,
		Added:            data.Added,
		Status:           data.Status,
		Filename:         data.Filename,
		OriginalFilename: data.OriginalFilename,
		Links:            data.Links,
		Debrid:           r.name,
		MountPath:        r.MountPath,
	}
	t.Files = r.getTorrentFiles(t, data) // Get selected files
	return t, nil
}

func (r *RealDebrid) UpdateTorrent(t *types.Torrent) error {
	url := fmt.Sprintf("%s/torrents/info/%s", r.Host, t.Id)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound {
			return utils.TorrentNotFoundError
		}
		return fmt.Errorf("realdebrid API error: Status: %d || Body: %s", resp.StatusCode, string(bodyBytes))
	}
	var data torrentInfo
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		return err
	}
	t.Name = data.Filename
	t.Bytes = data.Bytes
	t.Folder = data.OriginalFilename
	t.Progress = data.Progress
	t.Status = data.Status
	t.Speed = data.Speed
	t.Seeders = data.Seeders
	t.Filename = data.Filename
	t.OriginalFilename = data.OriginalFilename
	t.Links = data.Links
	t.MountPath = r.MountPath
	t.Debrid = r.name
	t.Added = data.Added
	t.Files, _ = r.getSelectedFiles(t, data) // Get selected files

	return nil
}

func (r *RealDebrid) CheckStatus(t *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/torrents/info/%s", r.Host, t.Id)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for {
		resp, err := r.client.MakeRequest(req)
		if err != nil {
			r.logger.Info().Msgf("ERROR Checking file: %v", err)
			return t, err
		}
		var data torrentInfo
		if err = json.Unmarshal(resp, &data); err != nil {
			return t, err
		}
		status := data.Status
		t.Name = data.Filename // Important because some magnet changes the name
		t.Folder = data.OriginalFilename
		t.Filename = data.Filename
		t.OriginalFilename = data.OriginalFilename
		t.Bytes = data.Bytes
		t.Progress = data.Progress
		t.Speed = data.Speed
		t.Seeders = data.Seeders
		t.Links = data.Links
		t.Status = status
		t.Debrid = r.name
		t.MountPath = r.MountPath
		if status == "waiting_files_selection" {
			t.Files = r.getTorrentFiles(t, data)
			if len(t.Files) == 0 {
				return t, fmt.Errorf("no video files found")
			}
			filesId := make([]string, 0)
			for _, f := range t.Files {
				filesId = append(filesId, f.Id)
			}
			p := gourl.Values{
				"files": {strings.Join(filesId, ",")},
			}
			payload := strings.NewReader(p.Encode())
			req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("%s/torrents/selectFiles/%s", r.Host, t.Id), payload)
			res, err := r.client.Do(req)
			if err != nil {
				return t, err
			}
			if res.StatusCode != http.StatusNoContent {
				if res.StatusCode == 509 {
					return nil, utils.TooManyActiveDownloadsError
				}
				return t, fmt.Errorf("realdebrid API error: Status: %d", res.StatusCode)
			}
		} else if status == "downloaded" {
			t.Files, err = r.getSelectedFiles(t, data) // Get selected files
			if err != nil {
				return t, err
			}

			r.logger.Info().Msgf("Torrent: %s downloaded to RD", t.Name)
			return t, nil
		} else if utils.Contains(r.GetDownloadingStatus(), status) {
			if !t.DownloadUncached {
				return t, fmt.Errorf("torrent: %s not cached", t.Name)
			}
			return t, nil
		} else {
			return t, fmt.Errorf("torrent: %s has error: %s", t.Name, status)
		}

	}
}

func (r *RealDebrid) DeleteTorrent(torrentId string) error {
	url := fmt.Sprintf("%s/torrents/delete/%s", r.Host, torrentId)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	if _, err := r.client.MakeRequest(req); err != nil {
		return err
	}
	r.logger.Info().Msgf("Torrent: %s deleted from RD", torrentId)
	return nil
}

func (r *RealDebrid) GetFileDownloadLinks(t *types.Torrent) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	files := make(map[string]types.File)
	links := make(map[string]*types.DownloadLink)

	_files := t.GetFiles()
	wg.Add(len(_files))

	for _, f := range _files {
		go func(file types.File) {
			defer wg.Done()

			link, err := r.GetDownloadLink(t, &file)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			if link == nil {
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
			links[link.Link] = link
			mu.Unlock()
		}(f)
	}

	wg.Wait()

	if firstErr != nil {
		return firstErr
	}

	// Add links to cache
	r.accounts.SetDownloadLinks(links)
	t.Files = files
	return nil
}

func (r *RealDebrid) CheckLink(link string) error {
	url := fmt.Sprintf("%s/unrestrict/check", r.Host)
	payload := gourl.Values{
		"link": {link},
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(payload.Encode()))
	resp, err := r.repairClient.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return utils.HosterUnavailableError // File has been removed
	}
	return nil
}

func (r *RealDebrid) _getDownloadLink(file *types.File) (*types.DownloadLink, error) {
	url := fmt.Sprintf("%s/unrestrict/link/", r.Host)
	_link := file.Link
	if strings.HasPrefix(file.Link, "https://real-debrid.com/d/") && len(file.Link) > 39 {
		_link = file.Link[0:39]
	}
	payload := gourl.Values{
		"link": {_link},
	}
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(payload.Encode()))
	resp, err := r.downloadClient.Do(req)

	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// Read the response body to get the error message
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var data ErrorResponse
		if err = json.Unmarshal(b, &data); err != nil {
			return nil, fmt.Errorf("error unmarshalling %d || %s \n %s", resp.StatusCode, err, string(b))
		}
		switch data.ErrorCode {
		case 19:
			return nil, utils.HosterUnavailableError // File has been removed
		case 23:
			return nil, utils.TrafficExceededError
		case 24:
			return nil, utils.HosterUnavailableError // Link has been nerfed
		case 34:
			return nil, utils.TrafficExceededError // traffic exceeded
		case 35:
			return nil, utils.HosterUnavailableError
		case 36:
			return nil, utils.TrafficExceededError // traffic exceeded
		default:
			return nil, fmt.Errorf("realdebrid API error: Status: %d || Code: %d", resp.StatusCode, data.ErrorCode)
		}
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var data UnrestrictResponse
	if err = json.Unmarshal(b, &data); err != nil {
		return nil, fmt.Errorf("realdebrid API error: Error unmarshalling response: %w", err)
	}
	if data.Download == "" {
		return nil, fmt.Errorf("realdebrid API error: download link not found")
	}
	now := time.Now()
	return &types.DownloadLink{
		Filename:     data.Filename,
		Size:         data.Filesize,
		Link:         data.Link,
		DownloadLink: data.Download,
		Generated:    now,
		ExpiresAt:    now.Add(r.autoExpiresLinksAfter),
	}, nil

}

func (r *RealDebrid) GetDownloadLink(t *types.Torrent, file *types.File) (*types.DownloadLink, error) {

	accounts := r.accounts.All()

	for _, account := range accounts {
		r.downloadClient.SetHeader("Authorization", fmt.Sprintf("Bearer %s", account.Token))
		downloadLink, err := r._getDownloadLink(file)

		if err == nil {
			return downloadLink, nil
		}

		retries := 0
		if errors.Is(err, utils.TrafficExceededError) {
			// Retries generating
			retries = 5
		} else {
			// If the error is not traffic exceeded, return the error
			return nil, err
		}
		backOff := 1 * time.Second
		for retries > 0 {
			downloadLink, err = r._getDownloadLink(file)
			if err == nil {
				return downloadLink, nil
			}
			if !errors.Is(err, utils.TrafficExceededError) {
				return nil, err
			}
			// Add a delay before retrying
			time.Sleep(backOff)
			backOff *= 2 // Exponential backoff
			retries--
		}
	}
	return nil, fmt.Errorf("realdebrid API error: download link not found")
}

func (r *RealDebrid) getTorrents(offset int, limit int) (int, []*types.Torrent, error) {
	url := fmt.Sprintf("%s/torrents?limit=%d", r.Host, limit)
	torrents := make([]*types.Torrent, 0)
	if offset > 0 {
		url = fmt.Sprintf("%s&offset=%d", url, offset)
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := r.client.Do(req)

	if err != nil {
		return 0, torrents, err
	}

	if resp.StatusCode == http.StatusNoContent {
		return 0, torrents, nil
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return 0, torrents, fmt.Errorf("realdebrid API error: %d", resp.StatusCode)
	}

	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, torrents, err
	}
	totalItems, _ := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	var data []TorrentsResponse
	if err = json.Unmarshal(body, &data); err != nil {
		return 0, torrents, err
	}
	filenames := map[string]struct{}{}
	for _, t := range data {
		if t.Status != "downloaded" {
			continue
		}
		torrents = append(torrents, &types.Torrent{
			Id:               t.Id,
			Name:             t.Filename,
			Bytes:            t.Bytes,
			Progress:         t.Progress,
			Status:           t.Status,
			Filename:         t.Filename,
			OriginalFilename: t.Filename,
			Links:            t.Links,
			Files:            make(map[string]types.File),
			InfoHash:         t.Hash,
			Debrid:           r.name,
			MountPath:        r.MountPath,
			Added:            t.Added.Format(time.RFC3339),
		})
		filenames[t.Filename] = struct{}{}
	}
	return totalItems, torrents, nil
}

func (r *RealDebrid) GetTorrents() ([]*types.Torrent, error) {
	limit := 5000

	// Get first batch and total count
	allTorrents := make([]*types.Torrent, 0)
	var fetchError error
	offset := 0
	for {
		// Fetch next batch of torrents
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
	}

	if fetchError != nil {
		return nil, fetchError
	}

	return allTorrents, nil
}

func (r *RealDebrid) GetDownloadLinks() (map[string]*types.DownloadLink, error) {
	links := make(map[string]*types.DownloadLink)
	offset := 0
	limit := 1000

	accounts := r.accounts.All()

	if len(accounts) < 1 {
		// No active download keys. It's likely that the key has reached bandwidth limit
		return links, fmt.Errorf("no active download keys")
	}
	activeAccount := accounts[0]
	r.downloadClient.SetHeader("Authorization", fmt.Sprintf("Bearer %s", activeAccount.Token))
	for {
		dl, err := r._getDownloads(offset, limit)
		if err != nil {
			break
		}
		if len(dl) == 0 {
			break
		}

		for _, d := range dl {
			if _, exists := links[d.Link]; exists {
				// This is ordered by date, so we can skip the rest
				continue
			}
			links[d.Link] = &d
		}

		offset += len(dl)
	}

	return links, nil
}

func (r *RealDebrid) _getDownloads(offset int, limit int) ([]types.DownloadLink, error) {
	url := fmt.Sprintf("%s/downloads?limit=%d", r.Host, limit)
	if offset > 0 {
		url = fmt.Sprintf("%s&offset=%d", url, offset)
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := r.downloadClient.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var data []DownloadsResponse
	if err = json.Unmarshal(resp, &data); err != nil {
		return nil, err
	}
	links := make([]types.DownloadLink, 0)
	for _, d := range data {
		links = append(links, types.DownloadLink{
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

func (r *RealDebrid) GetDownloadingStatus() []string {
	return []string{"downloading", "magnet_conversion", "queued", "compressing", "uploading"}
}

func (r *RealDebrid) GetDownloadUncached() bool {
	return r.DownloadUncached
}

func (r *RealDebrid) GetMountPath() string {
	return r.MountPath
}

func (r *RealDebrid) DeleteDownloadLink(linkId string) error {
	url := fmt.Sprintf("%s/downloads/delete/%s", r.Host, linkId)
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	if _, err := r.downloadClient.MakeRequest(req); err != nil {
		return err
	}
	return nil
}

func (r *RealDebrid) GetProfile() (*types.Profile, error) {
	if r.Profile != nil {
		return r.Profile, nil
	}
	url := fmt.Sprintf("%s/user", r.Host)
	req, _ := http.NewRequest(http.MethodGet, url, nil)

	resp, err := r.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var data profileResponse
	if json.Unmarshal(resp, &data) != nil {
		return nil, err
	}
	profile := &types.Profile{
		Id:         data.Id,
		Username:   data.Username,
		Email:      data.Email,
		Points:     data.Points,
		Premium:    data.Premium,
		Expiration: data.Expiration,
		Type:       data.Type,
	}
	r.Profile = profile
	return profile, nil
}

func (r *RealDebrid) GetAvailableSlots() (int, error) {
	url := fmt.Sprintf("%s/torrents/activeCount", r.Host)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := r.client.MakeRequest(req)
	if err != nil {
		return 0, nil
	}
	var data AvailableSlotsResponse
	if json.Unmarshal(resp, &data) != nil {
		return 0, fmt.Errorf("error unmarshalling available slots response: %w", err)
	}
	return data.TotalSlots - data.ActiveSlots - r.minimumFreeSlot, nil // Ensure we maintain minimum active pots
}

func (r *RealDebrid) Accounts() *types.Accounts {
	return r.accounts
}
