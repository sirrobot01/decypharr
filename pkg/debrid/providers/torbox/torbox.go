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
	"github.com/sirrobot01/decypharr/pkg/debrid/account"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/version"
	"go.uber.org/ratelimit"
)

type Torbox struct {
	name                  string
	Host                  string `json:"host"`
	APIKey                string
	accountsManager       *account.Manager
	autoExpiresLinksAfter time.Duration

	DownloadUncached bool
	client           *request.Client

	MountPath   string
	logger      zerolog.Logger
	checkCached bool
	addSamples  bool
}

func New(dc config.Debrid, ratelimits map[string]ratelimit.Limiter) (*Torbox, error) {

	headers := map[string]string{
		"Authorization": fmt.Sprintf("Bearer %s", dc.APIKey),
		"User-Agent":    fmt.Sprintf("Decypharr/%s (%s; %s)", version.GetInfo(), runtime.GOOS, runtime.GOARCH),
	}
	_log := logger.New(dc.Name)
	client := request.New(
		request.WithHeaders(headers),
		request.WithRateLimiter(ratelimits["main"]),
		request.WithLogger(_log),
		request.WithProxy(dc.Proxy),
	)
	autoExpiresLinksAfter, err := time.ParseDuration(dc.AutoExpireLinksAfter)
	if autoExpiresLinksAfter == 0 || err != nil {
		autoExpiresLinksAfter = 48 * time.Hour
	}

	return &Torbox{
		name:                  "torbox",
		Host:                  "https://api.torbox.app/v1",
		APIKey:                dc.APIKey,
		accountsManager:       account.NewManager(dc, ratelimits["download"], _log),
		DownloadUncached:      dc.DownloadUncached,
		autoExpiresLinksAfter: autoExpiresLinksAfter,
		client:                client,
		MountPath:             dc.Folder,
		logger:                _log,
		checkCached:           dc.CheckCached,
		addSamples:            dc.AddSamples,
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
	if !torrent.DownloadUncached {
		_ = writer.WriteField("add_only_if_cached", "true")
	}
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
	torrent.Added = time.Now().Format(time.RFC3339)

	return torrent, nil
}

func (tb *Torbox) getTorboxStatus(status string, finished bool) string {
	if finished {
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

	return determinedStatus
}

func (tb *Torbox) GetTorrent(torrentId string) (*types.Torrent, error) {
	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, torrentId)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
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

	totalFiles := 0
	skippedSamples := 0
	skippedFileType := 0
	skippedSize := 0
	validFiles := 0
	filesWithLinks := 0

	for _, f := range data.Files {
		totalFiles++
		fileName := filepath.Base(f.Name)

		if !tb.addSamples && utils.IsSampleFile(f.AbsolutePath) {
			skippedSamples++
			continue
		}
		if !cfg.IsAllowedFile(fileName) {
			skippedFileType++
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
			skippedSize++
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
	}

	// Log summary only if there are issues or for debugging
	tb.logger.Debug().
		Str("torrent_id", t.Id).
		Str("torrent_name", t.Name).
		Bool("download_finished", data.DownloadFinished).
		Str("status", t.Status).
		Int("total_files", totalFiles).
		Int("valid_files", validFiles).
		Int("final_file_count", len(t.Files)).
		Msg("Torrent file processing completed")
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
	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, t.Id)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return err
	}
	var res InfoResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return err
	}
	data := res.Data
	name := data.Name

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
			continue
		}

		if !cfg.IsAllowedFile(fileName) {
			continue
		}

		if !cfg.IsSizeAllowed(f.Size) {
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
	linkCh := make(chan types.DownloadLink)
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
			if link.DownloadLink != "" {
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

	// Check for errors
	for err := range errCh {
		if err != nil {
			return err // Return the first error encountered
		}
	}

	t.Files = files
	return nil
}

func (tb *Torbox) GetDownloadLink(t *types.Torrent, file *types.File) (types.DownloadLink, error) {
	// 1. Call TorBox requestdl endpoint to validate access to this file
	url := fmt.Sprintf("%s/api/torrents/requestdl/", tb.Host)
	query := gourl.Values{}
	query.Add("torrent_id", t.Id)
	query.Add("token", tb.APIKey)
	query.Add("file_id", file.Id)
	url += "?" + query.Encode()

	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Failed to make request to Torbox API")
		return types.DownloadLink{}, err
	}

	var data DownloadLinksResponse
	if err = json.Unmarshal(resp, &data); err != nil {
		tb.logger.Error().
			Err(err).
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Failed to unmarshal Torbox API response")
		return types.DownloadLink{}, err
	}

	if data.Data == nil {
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Bool("success", data.Success).
			Interface("error", data.Error).
			Str("detail", data.Detail).
			Msg("Torbox API returned no data")
		return types.DownloadLink{}, fmt.Errorf("error getting download links")
	}

	// CDN link. Not being used anymore.
	cdnLink := *data.Data
	if cdnLink == "" {
		tb.logger.Error().
			Str("torrent_id", t.Id).
			Str("file_id", file.Id).
			Msg("Torbox API returned empty download link")
		return types.DownloadLink{}, fmt.Errorf("error getting download links")
	}

	// 2. Build the permalink as TorBox recommends:
	//    {{api_base}}/{{api_version}}/api/torrents/requestdl?token={{api_key}}&torrent_id={{torrent_id}}&file_id={{torrent_file_id}}&redirect=true
	permalinkQuery := gourl.Values{}
	permalinkQuery.Add("token", tb.APIKey)
	permalinkQuery.Add("torrent_id", t.Id)
	permalinkQuery.Add("file_id", file.Id)
	permalinkQuery.Add("redirect", "true")

	permalink := fmt.Sprintf("%s/api/torrents/requestdl?%s", tb.Host, permalinkQuery.Encode())

	// Debug log so we can see the generated permalink while testing
	tb.logger.Debug().
		Str("torrent_id", t.Id).
		Str("file_id", file.Id).
		Str("permalink", permalink).
		Msg("Generated Torbox download permalink")

	now := time.Now()
	dl := types.DownloadLink{
		Token:        tb.APIKey,
		Link:         file.Link,
		DownloadLink: permalink, // store permalink, not cdnLink
		Id:           file.Id,
		Generated:    now,
		ExpiresAt:    now.Add(tb.autoExpiresLinksAfter),
	}

	tb.accountsManager.StoreDownloadLink(dl)

	return dl, nil
}

func (tb *Torbox) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

func (tb *Torbox) GetTorrents() ([]*types.Torrent, error) {
	offset := 0
	allTorrents := make([]*types.Torrent, 0)

	for {
		torrents, err := tb.getTorrents(offset)
		if err != nil {
			break
		}
		if len(torrents) == 0 {
			break
		}
		allTorrents = append(allTorrents, torrents...)
		offset += len(torrents)
	}
	return allTorrents, nil
}

func (tb *Torbox) getTorrents(offset int) ([]*types.Torrent, error) {
	url := fmt.Sprintf("%s/api/torrents/mylist?offset=%d", tb.Host, offset)
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	resp, err := tb.client.MakeRequest(req)
	if err != nil {
		return nil, err
	}

	var res TorrentsListResponse
	err = json.Unmarshal(resp, &res)
	if err != nil {
		return nil, err
	}

	if !res.Success || res.Data == nil {
		return nil, fmt.Errorf("torbox API error: %v", res.Error)
	}

	torrents := make([]*types.Torrent, 0, len(*res.Data))
	cfg := config.Get()

	for _, data := range *res.Data {
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

	return torrents, nil
}

func (tb *Torbox) GetDownloadUncached() bool {
	return tb.DownloadUncached
}

func (tb *Torbox) RefreshDownloadLinks() error {
	return nil
}

func (tb *Torbox) CheckLink(link string) error {
	return nil
}

func (tb *Torbox) GetMountPath() string {
	return tb.MountPath
}

func (tb *Torbox) GetAvailableSlots() (int, error) {
	//TODO: Implement the logic to check available slots for Torbox
	return 0, fmt.Errorf("not implemented")
}

func (tb *Torbox) GetProfile() (*types.Profile, error) {
	return nil, nil
}

func (tb *Torbox) AccountManager() *account.Manager {
	return tb.accountsManager
}

func (tb *Torbox) SyncAccounts() error {
	return nil
}

func (tb *Torbox) DeleteDownloadLink(account *account.Account, downloadLink types.DownloadLink) error {
	account.DeleteDownloadLink(downloadLink.Link)
	return nil
}
