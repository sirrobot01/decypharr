package torbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	gourl "net/url"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/version"
)

type Torbox struct {
	name                  string
	Host                  string `json:"host"`
	APIKey                string
	accounts              *types.Accounts
	autoExpiresLinksAfter time.Duration

	DownloadUncached bool
	client           *request.Client

	MountPath   string
	logger      zerolog.Logger
	checkCached bool
	addSamples  bool

	// Circuit breaker fields
	circuitBreakerOpen    bool
	circuitBreakerTime    time.Time
	circuitBreakerCount   int
	circuitBreakerWindow  time.Duration
	circuitBreakerTimeout time.Duration
	mu                    sync.RWMutex
}

func (tb *Torbox) GetProfile() (*types.Profile, error) {
	return nil, nil
}

func New(dc config.Debrid) (*Torbox, error) {
	rl := request.ParseRateLimit(dc.RateLimit)

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"User-Agent":    fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH),
	}
	_log := logger.New(dc.Name)
	client := request.New(
		request.WithHeaders(headers),
		request.WithRateLimiter(rl),
		request.WithLogger(_log),
		request.WithProxy(dc.Proxy),
		request.WithTimeout(3*time.Minute), // Increase timeout for Torbox API operations
	)
	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	return &Torbox{
		name:                  "torbox",
		Host:                  "https://api.torbox.app/v1",
		APIKey:                dc.APIKey,
		accounts:              types.NewAccounts(dc),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                client,
		MountPath:             dc.Folder,
		logger:                _log,
		checkCached:           dc.CheckCached,
		addSamples:            dc.AddSamples,
		circuitBreakerWindow:  5 * time.Minute,
		circuitBreakerTimeout: 30 * time.Second,
	}, nil
}

func (tb *Torbox) Name() string {
	return tb.name
}

func (tb *Torbox) Logger() zerolog.Logger {
	return tb.logger
}

func (tb *Torbox) IsAvailable(hashes []string) map[string]bool {
	// Check if the infohashes are available in the local cache
	result := make(map[string]bool)

	// Divide hashes into groups of 100
	for i := 0; i < len(hashes); i += 100 {
		end := i + 100
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

		hashStr := strings.Join(validHashes, ",")
		url := fmt.Sprintf("%s/api/torrents/checkcached?hash=%s", tb.Host, hashStr)
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := tb.client.MakeRequest(req)
		if err != nil {
			tb.logger.Error().Err(err).Msgf("Error checking availability")
			return result
		}
		var res AvailableResponse
		err = json.Unmarshal(resp, &res)
		if err != nil {
			tb.logger.Error().Err(err).Msgf("Error marshalling availability")
			return result
		}
		if res.Data == nil {
			return result
		}

		for h, c := range *res.Data {
			if c.Size > 0 {
				result[strings.ToUpper(h)] = true
			}
		}
	}
	return result
}

func (tb *Torbox) SubmitMagnet(torrent *types.Torrent) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/api/torrents/createtorrent", tb.Host)
	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	_ = writer.WriteField("magnet", torrent.Magnet.Link)
	err := writer.Close()
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequest(http.MethodPost, url, payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}
	var data AddMagnetResponse
	err = json.Unmarshal(resp, &data)
	if err != nil {
		return nil, err
	}
	if data.Data == nil {
		return nil, fmt.Errorf("error adding torrent")
	}
	dt := *data.Data
	torrentId := strconv.Itoa(dt.Id)
	torrent.Id = torrentId
	torrent.MountPath = tb.MountPath
	torrent.Debrid = tb.name

	return torrent, nil
}

func (tb *Torbox) getTorboxStatus(status string, finished bool) string {
	tb.logger.Trace().
		Str("download_state", status).
		Bool("download_finished", finished).
		Msg("Determining torrent status")

	if finished {
		tb.logger.Trace().
			Str("download_state", status).
			Bool("download_finished", finished).
			Str("determined_status", "downloaded").
			Msg("Status determined: downloaded (finished=true)")
		return "downloaded"
	}
	downloading := []string{"completed", "cached", "paused", "downloading", "uploading",
		"checkingResumeData", "metaDL", "pausedUP", "queuedUP", "checkingUP",
		"forcedUP", "allocating", "downloading", "metaDL", "pausedDL",
		"queuedDL", "checkingDL", "forcedDL", "checkingResumeData", "moving"}

	var determinedStatus string
	switch {
	case utils.Contains(downloading, status):
		determinedStatus = "downloading"
	default:
		determinedStatus = "error"
	}

	tb.logger.Trace().
		Str("download_state", status).
		Bool("download_finished", finished).
		Str("determined_status", determinedStatus).
		Strs("downloading_states", downloading).
		Bool("state_in_downloading", utils.Contains(downloading, status)).
		Msg("Status determined")

	return determinedStatus
}

func (tb *Torbox) GetTorrent(torrentId string) (*types.Torrent, error) {
	const maxRetries = 3
	const backoffBase = 1 * time.Second

	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, torrentId)

	var resp []byte
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err = tb.client.MakeRequest(req)
		if err != nil {
			if attempt == maxRetries-1 {
				tb.logger.Error().
					Err(err).
					Str("torrent_id", torrentId).
					Int("final_attempt", attempt+1).
					Msg("Failed to get torrent after all retries")
				return nil, err
			}

			if tb.isRetryableError(err) {
				backoffDuration := time.Duration(attempt+1) * backoffBase
				tb.logger.Warn().
					Err(err).
					Str("torrent_id", torrentId).
					Int("attempt", attempt+1).
					Dur("backoff", backoffDuration).
					Msg("Retryable error getting torrent, backing off")
				time.Sleep(backoffDuration)
				continue
			} else {
				tb.logger.Error().
					Err(err).
					Str("torrent_id", torrentId).
					Msg("Non-retryable error getting torrent")
				return nil, err
			}
		}
		break
	}

	var res InfoResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}
	data := res.Data
	if data == nil {
		return nil, fmt.Errorf("error getting torrent")
	}
	t := &types.Torrent{
		Id:               strconv.Itoa(data.Id),
		Name:             data.Name,
		Bytes:            data.Size,
		Folder:           data.Name,
		Progress:         data.Progress * 100,
		Status:           tb.getTorboxStatus(data.DownloadState, data.DownloadFinished),
		Speed:            data.DownloadSpeed,
		Seeders:          data.Seeds,
		Filename:         data.Name,
		OriginalFilename: data.Name,
		MountPath:        tb.MountPath,
		Debrid:           tb.name,
		Files:            make(map[string]types.File),
		Added:            data.CreatedAt.Format(time.RFC3339),
	}
	cfg := config.Get()

	// Debug logging for torrent status
	tb.logger.Info().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("download_state", data.DownloadState).
		Str("determined_status", t.Status).
		Float64("progress", data.Progress).
		Int("total_files", len(data.Files)).
		Msg("Processing torrent files")

	totalFiles := 0
	skippedSamples := 0
	skippedFileType := 0
	skippedSize := 0
	validFiles := 0
	filesWithLinks := 0

	// Log basic info before processing files
	tb.logger.Trace().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Int("api_files_count", len(data.Files)).
		Bool("download_finished", data.DownloadFinished).
		Msg("UpdateTorrent: Starting file processing")

	for _, f := range data.Files {
		totalFiles++
		fileName := filepath.Base(f.Name)

		if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
			skippedSamples++
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Str("file_path", f.Name).
				Str("file_extension", filepath.Ext(fileName)).
				Int64("file_size", f.Size).
				Msg("UpdateTorrent: Skipping sample file")
			continue
		}
		if !cfg.IsAllowedFile(fileName) {
			skippedFileType++
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Str("file_extension", filepath.Ext(fileName)).
				Int64("file_size", f.Size).
				Strs("allowed_file_types", cfg.AllowedExt).
				Msg("UpdateTorrent: Skipping file - not allowed file type")
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
			skippedSize++
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Str("file_extension", filepath.Ext(fileName)).
				Int64("file_size", f.Size).
				Int64("min_file_size", cfg.GetMinFileSize()).
				Int64("max_file_size", cfg.GetMaxFileSize()).
				Msg("UpdateTorrent: Skipping file - size not allowed")
			continue
		}

		validFiles++
		file := types.File{
			TorrentId: t.Id,
			Id:        strconv.Itoa(f.Id),
			Name:      fileName,
			Size:      f.Size,
			Path:      f.Name,
		}

		// For downloaded torrents, set a placeholder link to indicate file is available
		if data.DownloadFinished {
			file.Link = fmt.Sprintf("torbox://%s/%d", t.Id, f.Id)
			filesWithLinks++
		}

		t.Files[fileName] = file
		tb.logger.Trace().
			Str("torrent_id", t.Id).
			Str("file_name", fileName).
			Str("file_id", file.Id).
			Str("file_link", file.Link).
			Msg("UpdateTorrent: Added valid file")
	}

	// Summary debug log
	tb.logger.Trace().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("status", t.Status).
		Int("total_files", totalFiles).
		Int("skipped_samples", skippedSamples).
		Int("skipped_file_type", skippedFileType).
		Int("skipped_size", skippedSize).
		Int("valid_files", validFiles).
		Int("final_file_count", len(t.Files)).
		Int("files_with_links", filesWithLinks).
		Int64("min_file_size", cfg.GetMinFileSize()).
		Int64("max_file_size", cfg.GetMaxFileSize()).
		Strs("allowed_file_types", cfg.AllowedExt).
		Bool("add_samples", tb.addSamples).
		Msg("UpdateTorrent: Completed torrent refresh")
	var cleanPath string
	if len(t.Files) > 0 {
		cleanPath = path.Clean(data.Files[0].Name)
	} else {
		cleanPath = path.Clean(data.Name)
	}

	t.OriginalFilename = strings.Split(cleanPath, "/")[0]
	t.Debrid = tb.name

	return t, nil
}

func (tb *Torbox) UpdateTorrent(t *types.Torrent) error {
	const maxRetries = 3
	const backoffBase = 1 * time.Second

	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, t.Id)

	var resp []byte
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err = tb.client.MakeRequest(req)
		if err != nil {
			if attempt == maxRetries-1 {
				tb.logger.Error().
					Err(err).
					Str("torrent_id", t.Id).
					Int("final_attempt", attempt+1).
					Msg("Failed to update torrent after all retries")
				return err
			}

			if tb.isRetryableError(err) {
				backoffDuration := time.Duration(attempt+1) * backoffBase
				tb.logger.Warn().
					Err(err).
					Str("torrent_id", t.Id).
					Int("attempt", attempt+1).
					Dur("backoff", backoffDuration).
					Msg("Retryable error updating torrent, backing off")
				time.Sleep(backoffDuration)
				continue
			} else {
				tb.logger.Error().
					Err(err).
					Str("torrent_id", t.Id).
					Msg("Non-retryable error updating torrent")
				return err
			}
		}
		break
	}

	var res InfoResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return err
	}
	data := res.Data
	name := data.Name

	// Debug: Log the update torrent state
	tb.logger.Trace().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("download_state", data.DownloadState).
		Float64("progress", data.Progress).
		Int("total_files", len(data.Files)).
		Msg("UpdateTorrent: Refreshing torrent from Torbox")

	t.Name = name
	t.Bytes = data.Size
	t.Folder = name
	t.Progress = data.Progress * 100
	t.Status = tb.getTorboxStatus(data.DownloadState, data.DownloadFinished)
	t.Speed = data.DownloadSpeed
	t.Seeders = data.Seeds
	t.Filename = name
	t.OriginalFilename = name
	t.MountPath = tb.MountPath
	t.Debrid = tb.name

	// Clear existing files map to rebuild it
	t.Files = make(map[string]types.File)

	cfg := config.Get()
	validFiles := 0
	filesWithLinks := 0

	for _, f := range data.Files {
		fileName := filepath.Base(f.Name)

		if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Str("file_path", f.AbsolutePath).
				Msg("UpdateTorrent: Skipping sample file")
			continue
		}

		if !cfg.IsAllowedFile(fileName) {
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Msg("UpdateTorrent: Skipping disallowed file type")
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
			tb.logger.Trace().
				Str("torrent_id", t.Id).
				Str("file_name", fileName).
				Int64("file_size", f.Size).
				Int64("min_size", cfg.GetMinFileSize()).
				Msg("UpdateTorrent: Skipping file too small")
			continue
		}

		validFiles++
		file := types.File{
			TorrentId: t.Id,
			Id:        strconv.Itoa(f.Id),
			Name:      fileName,
			Size:      f.Size,
			Path:      fileName,
		}

		// For downloaded torrents, set a placeholder link to indicate file is available
		if data.DownloadFinished {
			file.Link = fmt.Sprintf("torbox://%s/%s", t.Id, strconv.Itoa(f.Id))
			filesWithLinks++
		}

		t.Files[fileName] = file
	}

	// Debug: Log the final update state
	tb.logger.Trace().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("status", t.Status).
		Int("valid_files", validFiles).
		Int("files_with_links", filesWithLinks).
		Int("final_file_count", len(t.Files)).
		Msg("UpdateTorrent: Completed torrent refresh")

	var cleanPath string
	if len(t.Files) > 0 {
		cleanPath = path.Clean(data.Files[0].Name)
	} else {
		cleanPath = path.Clean(data.Name)
	}

	t.OriginalFilename = strings.Split(cleanPath, "/")[0]
	t.Debrid = tb.name
	return nil
}

func (tb *Torbox) CheckStatus(torrent *types.Torrent) (*types.Torrent, error) {
	for {
		err := tb.UpdateTorrent(torrent)

		if err != nil || torrent == nil {
			return torrent, err
		}
		status := torrent.Status
		if status == "downloaded" {
			tb.logger.Info().Msgf("Torrent: %s downloaded", torrent.Name)
			return torrent, nil
		} else if utils.Contains(tb.GetDownloadingStatus(), status) {
			if !torrent.DownloadUncached {
				return torrent, fmt.Errorf("torrent: %s not cached", torrent.Name)
			}
			// Break out of the loop if the torrent is downloading.
			// This is necessary to prevent infinite loop since we moved to sync downloading and async processing
			return torrent, nil
		} else {
			return torrent, fmt.Errorf("torrent: %s has error", torrent.Name)
		}

	}
}

func (tb *Torbox) DeleteTorrent(torrentId string) error {
	url := fmt.Sprintf("%s/api/torrents/controltorrent/%s", tb.Host, torrentId)
	payload := map[string]string{"torrent_id": torrentId, "action": "Delete"}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodDelete, url, bytes.NewBuffer(jsonPayload))
	if _, err := tb.client.MakeRequest(req); err != nil {
		return err
	}
	tb.logger.Info().Msgf("Torrent %s deleted from Torbox", torrentId)
	return nil
}

func (tb *Torbox) GetFileDownloadLinks(t *types.Torrent) error {
	filesCh := make(chan types.File, len(t.Files))
	linkCh := make(chan *types.DownloadLink)
	errCh := make(chan error, len(t.Files))

	var wg sync.WaitGroup
	wg.Add(len(t.Files))
	for _, file := range t.Files {
		go func() {
			defer wg.Done()
			link, err := tb.GetDownloadLink(t, &file)
			if err != nil {
				errCh <- err
				return
			}
			if link != nil {
				linkCh <- link
				file.DownloadLink = link
			}
			filesCh <- file
		}()
	}
	go func() {
		wg.Wait()
		close(filesCh)
		close(linkCh)
		close(errCh)
	}()

	// Collect results
	files := make(map[string]types.File, len(t.Files))
	for file := range filesCh {
		files[file.Name] = file
	}

	// Collect download links
	for link := range linkCh {
		if link != nil {
			tb.accounts.SetDownloadLink(link.Link, link)
		}
	}

	// Check for errors
	for err := range errCh {
		if err != nil {
			return err // Return the first error encountered
		}
	}

	t.Files = files
	return nil
}

func (tb *Torbox) GetDownloadLink(t *types.Torrent, file *types.File) (*types.DownloadLink, error) {
	tb.logger.Trace().
		Str("torrent_id", t.Id).
		Str("file_id", file.Id).
		Str("file_name", file.Name).
		Str("file_link", file.Link).
		Msg("Generating download link for Torbox file")

	url := fmt.Sprintf("%s/api/torrents/requestdl/", tb.Host)
	query := gourl.Values{}
	query.Add("torrent_id", t.Id)
	query.Add("token", tb.APIKey)
	query.Add("file_id", file.Id)
	url += "?" + query.Encode()

	tb.logger.Trace().
		Str("api_url", url).
		Msg("Making request to Torbox API for download link")

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Failed to make request to Torbox API")
		return nil, err
	}

	var data DownloadLinksResponse
	if err = json.Unmarshal(resp, &data); err != nil {
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Str("response", string(resp)).
			Msg("Failed to unmarshal Torbox API response")
		return nil, err
	}

	if data.Data == nil {
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Bool("success", data.Success).
			Interface("error", data.Error).
			Str("detail", data.Detail).
			Msg("Torbox API returned no data")
		return nil, fmt.Errorf("error getting download links")
	}

	link := *data.Data
	if link == "" {
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Torbox API returned empty download link")
		return nil, fmt.Errorf("error getting download links")
	}

	now := time.Now()
	downloadLink := &types.DownloadLink{
		Link:         file.Link,
		DownloadLink: link,
		Id:           file.Id,
		Generated:    now,
		ExpiresAt:    now.Add(tb.autoExpiresLinksAfter),
	}

	tb.logger.Info().
		Str("torrent_id", t.Id).
		Str("file_id", file.Id).
		Str("file_name", file.Name).
		Str("download_url", link).
		Time("expires_at", downloadLink.ExpiresAt).
		Msg("Successfully generated Torbox download link")

	return downloadLink, nil
}

// isRetryableError determines if an error is retryable
func (tb *Torbox) isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	errStr := strings.ToLower(err.Error())

	// Network-level retryable errors
	retryableNetErrors := []string{
		"context deadline exceeded",
		"timeout",
		"connection reset",
		"unexpected eof",
		"i/o timeout",
		"tls handshake timeout",
		"connection refused",
		"no route to host",
		"network is unreachable",
	}

	for _, retryable := range retryableNetErrors {
		if strings.Contains(errStr, retryable) {
			tb.logger.Trace().
				Str("error", errStr).
				Str("matched_pattern", retryable).
				Msg("Error identified as retryable network error")
			return true
		}
	}

	// HTTP status code errors that are retryable
	retryableHttpErrors := []string{
		"http error 429", // rate limited
		"http error 500", // internal server error
		"http error 502", // bad gateway
		"http error 503", // service unavailable
		"http error 504", // gateway timeout
		"http error 520", // cloudflare error
		"http error 521", // web server is down
		"http error 522", // connection timed out
		"http error 523", // origin is unreachable
		"http error 524", // timeout occurred
	}

	for _, retryable := range retryableHttpErrors {
		if strings.Contains(errStr, retryable) {
			tb.logger.Debug().
				Str("error", errStr).
				Str("matched_pattern", retryable).
				Msg("Error identified as retryable HTTP error")
			return true
		}
	}

	// Check for auth errors - these are NOT retryable
	if strings.Contains(errStr, "auth_error") || strings.Contains(errStr, "unauthorized") {
		tb.logger.Debug().
			Str("error", errStr).
			Msg("Error identified as non-retryable auth error")
		return false
	}

	tb.logger.Debug().
		Str("error", errStr).
		Msg("Error not identified as retryable")
	return false
}

func (tb *Torbox) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

func (tb *Torbox) GetTorrents() ([]*types.Torrent, error) {
	// Check circuit breaker
	if tb.checkCircuitBreaker() {
		return nil, fmt.Errorf("torbox API temporarily unavailable due to circuit breaker")
	}

	const pageSize = 50 // Reduce page size to avoid timeouts
	const maxRetries = 3
	const backoffBase = 2 * time.Second

	var allTorrents []*types.Torrent
	offset := 0
	cfg := config.Get()

	tb.logger.Trace().
		Int("page_size", pageSize).
		Msg("Starting torrent list fetch with pagination")

	for {
		var resp []byte
		var err error

		// Retry logic with exponential backoff
		for attempt := 0; attempt < maxRetries; attempt++ {
			url := fmt.Sprintf("%s/api/torrents/mylist?limit=%d&offset=%d", tb.Host, pageSize, offset)
			req, _ := http.NewRequest(http.MethodGet, url, nil)

			tb.logger.Trace().
				Str("url", url).
				Int("attempt", attempt+1).
				Int("max_retries", maxRetries).
				Msg("Fetching torrent page")

			resp, err = tb.client.MakeRequest(req)
			if err != nil {
				tb.recordError(err) // Record error for circuit breaker

				if attempt == maxRetries-1 {
					tb.logger.Error().
						Err(err).
						Int("offset", offset).
						Int("final_attempt", attempt+1).
						Msg("Failed to fetch torrent page after all retries")
					return nil, err
				}

				// Check if it's a retryable error
				if tb.isRetryableError(err) {
					backoffDuration := time.Duration(attempt+1) * backoffBase
					tb.logger.Warn().
						Err(err).
						Int("offset", offset).
						Int("attempt", attempt+1).
						Dur("backoff", backoffDuration).
						Msg("Retryable error, backing off")
					time.Sleep(backoffDuration)
					continue
				} else {
					tb.logger.Error().
						Err(err).
						Int("offset", offset).
						Msg("Non-retryable error, aborting")
					return nil, err
				}
			}

			// Success - break out of retry loop
			break
		}

		var res TorrentsListResponse
		err = json.Unmarshal(resp, &res)
		if err != nil {
			tb.logger.Error().
				Err(err).
				Int("offset", offset).
				Msg("Failed to unmarshal torrent page response")
			return nil, err
		}

		if !res.Success || res.Data == nil {
			tb.logger.Error().
				Interface("error", res.Error).
				Int("offset", offset).
				Msg("Torbox API error in paginated request")
			return nil, fmt.Errorf("torbox API error: %v", res.Error)
		}

		pageData := *res.Data

		// If no torrents in this page, we've reached the end
		if len(pageData) == 0 {
			break
		}

		torrents := make([]*types.Torrent, 0, len(pageData))

		for _, data := range pageData {
			t := &types.Torrent{
				Id:               strconv.Itoa(data.Id),
				Name:             data.Name,
				Bytes:            data.Size,
				Folder:           data.Name,
				Progress:         data.Progress * 100,
				Status:           tb.getTorboxStatus(data.DownloadState, data.DownloadFinished),
				Speed:            data.DownloadSpeed,
				Seeders:          data.Seeds,
				Filename:         data.Name,
				OriginalFilename: data.Name,
				MountPath:        tb.MountPath,
				Debrid:           tb.name,
				Files:            make(map[string]types.File),
				Added:            data.CreatedAt.Format(time.RFC3339),
				InfoHash:         data.Hash,
			}

			// Process files
			for _, f := range data.Files {
				fileName := filepath.Base(f.Name)
				if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
					// Skip sample files
					continue
				}
				if !cfg.IsAllowedFile(fileName) {
					continue
				}
				if !cfg.IsSizeAllowed(f.Size) {
					continue
				}
				file := types.File{
					TorrentId: t.Id,
					Id:        strconv.Itoa(f.Id),
					Name:      fileName,
					Size:      f.Size,
					Path:      f.Name,
				}

				// For downloaded torrents, set a placeholder link to indicate file is available
				if data.DownloadFinished {
					file.Link = fmt.Sprintf("torbox://%s/%d", t.Id, f.Id)
				}

				t.Files[fileName] = file
			}

			// Set original filename based on first file or torrent name
			var cleanPath string
			if len(t.Files) > 0 {
				cleanPath = path.Clean(data.Files[0].Name)
			} else {
				cleanPath = path.Clean(data.Name)
			}
			t.OriginalFilename = strings.Split(cleanPath, "/")[0]

			torrents = append(torrents, t)
		}

		allTorrents = append(allTorrents, torrents...)

		// Log pagination progress
		tb.logger.Trace().
			Int("page_torrents", len(pageData)).
			Int("total_torrents", len(allTorrents)).
			Int("offset", offset).
			Int("page_size", pageSize).
			Msg("Successfully fetched torrent page")

		// If we got fewer torrents than the page size, we've reached the end
		if len(pageData) < pageSize {
			tb.logger.Info().
				Int("total_torrents", len(allTorrents)).
				Int("total_pages", (offset/pageSize)+1).
				Msg("Successfully retrieved all torrents with pagination")
			break
		}

		offset += pageSize
	}

	return allTorrents, nil
}

func (tb *Torbox) GetDownloadUncached() bool {
	return tb.DownloadUncached
}

func (tb *Torbox) GetDownloadLinks() (map[string]*types.DownloadLink, error) {
	return nil, nil
}

func (tb *Torbox) CheckLink(link string) error {
	return nil
}

func (tb *Torbox) GetMountPath() string {
	return tb.MountPath
}

func (tb *Torbox) DeleteDownloadLink(linkId string) error {
	url := fmt.Sprintf("%s/api/torrents/controltorrent/%s", tb.Host, linkId)
	payload := map[string]string{"torrent_id": linkId, "action": "Delete"}
	jsonPayload, _ := json.Marshal(payload)
	req, _ := http.NewRequest(http.MethodDelete, url, bytes.NewBuffer(jsonPayload))
	if _, err := tb.client.MakeRequest(req); err != nil {
		return err
	}
	tb.logger.Info().Msgf("Download link %s deleted from Torbox", linkId)
	return nil
}

func (tb *Torbox) GetAvailableSlots() (int, error) {
	const maxRetries = 3
	const backoffBase = 2 * time.Second

	// Get user profile to determine plan limits with retry logic
	url := fmt.Sprintf("%s/api/user/me?settings=false", tb.Host)

	var resp []byte
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err = tb.client.MakeRequest(req)
		if err != nil {
			if attempt == maxRetries-1 {
				tb.logger.Error().
					Err(err).
					Int("final_attempt", attempt+1).
					Msg("Failed to fetch user profile for slot calculation after all retries")
				return 0, err
			}

			if tb.isRetryableError(err) {
				backoffDuration := time.Duration(attempt+1) * backoffBase
				tb.logger.Warn().
					Err(err).
					Int("attempt", attempt+1).
					Dur("backoff", backoffDuration).
					Msg("Retryable error fetching user profile, backing off")
				time.Sleep(backoffDuration)
				continue
			} else {
				tb.logger.Error().
					Err(err).
					Msg("Non-retryable error fetching user profile")
				return 0, err
			}
		}
		break
	}
	if err != nil {
		tb.logger.Error().Err(err).Msg("Failed to fetch user profile for slot calculation")
		return 0, err
	}

	var userRes UserResponse
	err = json.Unmarshal(resp, &userRes)
	if err != nil {
		tb.logger.Error().Err(err).Msg("Failed to unmarshal user profile response")
		return 0, err
	}

	if !userRes.Success || userRes.Data == nil {
		tb.logger.Error().Interface("error", userRes.Error).Msg("Torbox API error fetching user profile")
		return 0, fmt.Errorf("torbox API error: %v", userRes.Error)
	}

	userData := *userRes.Data

	// Calculate max slots based on plan and additional slots
	var baseSlotsForPlan int
	switch userData.Plan {
	case 1: // Plan 1 (likely basic/essential)
		baseSlotsForPlan = 3
	case 2: // Plan 2 (likely standard)
		baseSlotsForPlan = 5
	case 3: // Plan 3 (likely pro)
		baseSlotsForPlan = 10
	default:
		// Default to a reasonable number for unknown plans
		baseSlotsForPlan = 3
		tb.logger.Warn().Int("plan", userData.Plan).Msg("Unknown Torbox plan, using default slot count")
	}

	maxSlots := baseSlotsForPlan + userData.AdditionalConcurrentSlots

	// Get all torrents to count active ones
	torrents, err := tb.GetTorrents()
	if err != nil {
		tb.logger.Error().Err(err).Msg("Failed to get torrents for slot calculation")
		return 0, err
	}

	// Count active torrents (downloading, seeding, etc.)
	activeTorrents := 0
	for _, torrent := range torrents {
		// Count torrents that are actively downloading or processing
		if torrent.Status == "downloading" || torrent.Progress < 100 {
			activeTorrents++
		}
	}

	// Calculate available slots (ensure we don't go negative)
	availableSlots := maxSlots - activeTorrents
	if availableSlots < 0 {
		availableSlots = 0
	}

	tb.logger.Debug().
		Int("plan", userData.Plan).
		Int("base_slots", baseSlotsForPlan).
		Int("additional_slots", userData.AdditionalConcurrentSlots).
		Int("max_slots", maxSlots).
		Int("active_torrents", activeTorrents).
		Int("available_slots", availableSlots).
		Msg("Calculated available slots for Torbox based on user plan")

	return availableSlots, nil
}

func (tb *Torbox) Accounts() *types.Accounts {
	return tb.accounts
}

// checkCircuitBreaker checks if the circuit breaker should prevent API calls
func (tb *Torbox) checkCircuitBreaker() bool {
	tb.mu.RLock()
	defer tb.mu.RUnlock()

	if !tb.circuitBreakerOpen {
		return false
	}

	// Check if timeout has passed
	if time.Since(tb.circuitBreakerTime) > tb.circuitBreakerTimeout {
		tb.mu.RUnlock()
		tb.mu.Lock()
		tb.circuitBreakerOpen = false
		tb.circuitBreakerCount = 0
		tb.mu.Unlock()
		tb.mu.RLock()

		tb.logger.Info().
			Dur("timeout", tb.circuitBreakerTimeout).
			Msg("Circuit breaker timeout expired, allowing API calls")
		return false
	}

	return true
}

// recordError records an error for circuit breaker logic
func (tb *Torbox) recordError(err error) {
	if !tb.isRetryableError(err) {
		return
	}

	tb.mu.Lock()
	defer tb.mu.Unlock()

	now := time.Now()

	// Reset count if window has passed
	if tb.circuitBreakerTime.IsZero() || now.Sub(tb.circuitBreakerTime) > tb.circuitBreakerWindow {
		tb.circuitBreakerCount = 1
		tb.circuitBreakerTime = now
		return
	}

	tb.circuitBreakerCount++

	// Open circuit breaker if too many errors
	if tb.circuitBreakerCount >= 5 {
		tb.circuitBreakerOpen = true
		tb.logger.Warn().
			Int("error_count", tb.circuitBreakerCount).
			Dur("window", tb.circuitBreakerWindow).
			Dur("timeout", tb.circuitBreakerTimeout).
			Msg("Circuit breaker opened due to repeated errors")
	}
}
