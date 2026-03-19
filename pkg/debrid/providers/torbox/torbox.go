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
	
	var lastErr error
	for _, acc := range tb.accountsManager.All() {
		payload := &bytes.Buffer{}
		writer := multipart.NewWriter(payload)
		_ = writer.WriteField("magnet", torrent.Magnet.Link)
		if !torrent.DownloadUncached {
			_ = writer.WriteField("add_only_if_cached", "true")
		}
		err := writer.Close()
		if err != nil {
			lastErr = err
			continue
		}
		
		req, _ := http.NewRequest(http.MethodPost, url, payload)
		req.Header.Set("Content-Type", writer.FormDataContentType())
		resp, err := acc.Client().MakeRequest(req)
		
		if err != nil {
			if strings.Contains(err.Error(), "already queued") {
				// The torrent is already in the queue. Fetch all torrents to find its ID and resume it.
				torrents, fetchErr := tb.GetTorrents()
				if fetchErr == nil {
					for _, t := range torrents {
						if strings.EqualFold(t.InfoHash, torrent.InfoHash) {
							resumeUrl := fmt.Sprintf("%s/api/torrents/controltorrent", tb.Host)
							resumePayload := map[string]interface{}{"torrent_id": t.Id, "operation": "resume", "action": "resume"}
							jsonPayload, _ := json.Marshal(resumePayload)
							reqResume, _ := http.NewRequest(http.MethodPost, resumeUrl, bytes.NewBuffer(jsonPayload))
							reqResume.Header.Set("Content-Type", "application/json")
							_, _ = acc.Client().MakeRequest(reqResume)
							
							torrent.Id = t.Id
							torrent.Added = time.Now().Format(time.RFC3339)
							torrent.MountPath = tb.MountPath
							torrent.Debrid = tb.name
							return torrent, nil
						}
					}
				}
			}
			lastErr = err
			continue
		}
		
		var data AddMagnetResponse
		err = json.Unmarshal(resp, &data)
		if err != nil {
			lastErr = err
			continue
		}
		if data.Data == nil {
			lastErr = fmt.Errorf("error adding torrent. No data returned")
			continue
		}
		dt := *data.Data
		var torrentIdInt int
		if dt.TorrentId != 0 {
			torrentIdInt = dt.TorrentId
		} else {
			torrentIdInt = dt.Id
		}
		torrentId := strconv.Itoa(torrentIdInt)

		// Force resume it, as Torbox often leaves them 'queued'
		resumeUrl := fmt.Sprintf("%s/api/torrents/controltorrent", tb.Host)
		resumePayload := map[string]interface{}{"torrent_id": torrentIdInt, "operation": "resume", "action": "resume"}
		jsonPayload, _ := json.Marshal(resumePayload)
		reqResume, _ := http.NewRequest(http.MethodPost, resumeUrl, bytes.NewBuffer(jsonPayload))
		reqResume.Header.Set("Content-Type", "application/json")
		_, _ = acc.Client().MakeRequest(reqResume)

		torrent.Id = torrentId
		torrent.MountPath = tb.MountPath
		torrent.Debrid = tb.name
		torrent.Added = time.Now().Format(time.RFC3339)
	
		return torrent, nil
	}
	
	if lastErr != nil {
		tb.logger.Error().Err(lastErr).Msgf("Error adding torrent across all Torbox accounts")
	}

	return nil, lastErr
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
	
	var lastErr error
	for _, acc := range tb.accountsManager.All() {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := acc.Client().MakeRequest(req)
		if err != nil {
			lastErr = err
			continue
		}
		var rawResp struct {
			Success bool            `json:"success"`
			Error   any             `json:"error"`
			Detail  string          `json:"detail"`
			Data    json.RawMessage `json:"data"`
		}
		err = json.Unmarshal(resp, &rawResp)
		if err != nil {
			lastErr = err
			continue
		}
		
		if !rawResp.Success || rawResp.Data == nil || string(rawResp.Data) == "null" {
			lastErr = fmt.Errorf("error getting torrent. detail: %s", rawResp.Detail)
			continue
		}

		var data torboxInfo
		if err := json.Unmarshal(rawResp.Data, &data); err != nil {
			// fallback: try array
			var dataArray []torboxInfo
			if errArray := json.Unmarshal(rawResp.Data, &dataArray); errArray == nil && len(dataArray) > 0 {
				data = dataArray[0]
			} else {
				lastErr = fmt.Errorf("could not unmarshal torbox data: err=%v, errArray=%v", err, errArray)
				continue
			}
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
	if lastErr != nil {
		tb.logger.Error().Err(lastErr).Msgf("Error grabbing torrent info across all Torbox accounts")
	}
	return nil, lastErr
}

func (tb *Torbox) UpdateTorrent(t *types.Torrent) error {
	url := fmt.Sprintf("%s/api/torrents/mylist/?id=%s", tb.Host, t.Id)
	
	var lastErr error
	for _, acc := range tb.accountsManager.All() {
		req, _ := http.NewRequest(http.MethodGet, url, nil)
		resp, err := acc.Client().MakeRequest(req)
		if err != nil {
			lastErr = err
			continue
		}
		var rawResp struct {
			Success bool            `json:"success"`
			Error   any             `json:"error"`
			Detail  string          `json:"detail"`
			Data    json.RawMessage `json:"data"`
		}
		err = json.Unmarshal(resp, &rawResp)
		if err != nil {
			lastErr = err
			continue
		}
		
		if !rawResp.Success || rawResp.Data == nil || string(rawResp.Data) == "null" {
			lastErr = fmt.Errorf("error updating torrent. detail: %s", rawResp.Detail)
			continue
		}

		var data torboxInfo
		if err := json.Unmarshal(rawResp.Data, &data); err != nil {
			// fallback: try array
			var dataArray []torboxInfo
			if errArray := json.Unmarshal(rawResp.Data, &dataArray); errArray == nil && len(dataArray) > 0 {
				data = dataArray[0]
			} else {
				lastErr = fmt.Errorf("could not unmarshal torbox data: err=%v, errArray=%v", err, errArray)
				continue
			}
		}
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
	
	if lastErr != nil {
		tb.logger.Error().Err(lastErr).Msgf("Error unmarshalling torrent info across all Torbox accounts")
	}
	return lastErr
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
	url := fmt.Sprintf("%s/api/torrents/requestdl/", tb.Host)

	var lastErr error
	accounts := tb.accountsManager.Active()
	for _, acc := range accounts {
		query := gourl.Values{}
		query.Add("torrent_id", t.Id)
		query.Add("token", acc.Token)
		query.Add("file_id", file.Id)
		fullUrl := url + "?" + query.Encode()
	
		req, _ := http.NewRequest(http.MethodGet, fullUrl, nil)
		resp, err := acc.Client().MakeRequest(req)
		if err != nil {
			tb.logger.Error().
				Err(err).
				Str("torrent_id", t.Id).
				Str("file_id", file.Id).
				Msg("Failed to make request to Torbox API")
			lastErr = err
			continue
		}
	
		var data DownloadLinksResponse
		if err = json.Unmarshal(resp, &data); err != nil {
			tb.logger.Error().
				Err(err).
				Str("torrent_id", t.Id).
				Str("file_id", file.Id).
				Msg("Failed to unmarshal Torbox API response")
			lastErr = err
			continue
		}
	
		if data.Data == nil {
			lastErr = fmt.Errorf("error getting download links")
			continue
		}
	
		link := *data.Data
		if link == "" {
			lastErr = fmt.Errorf("error getting download links")
			continue
		}
	
		now := time.Now()
		dl := types.DownloadLink{
			Token:        acc.Token,
			Link:         file.Link,
			DownloadLink: link,
			Id:           file.Id,
			Generated:    now,
			ExpiresAt:    now.Add(tb.autoExpiresLinksAfter),
		}
	
		acc.StoreDownloadLink(dl)
	
		return dl, nil
	}
	if lastErr != nil {
		return types.DownloadLink{}, lastErr
	}
	return types.DownloadLink{}, fmt.Errorf("no active accounts available to unlock torbox link")
}

func (tb *Torbox) GetDownloadingStatus() []string {
	return []string{"downloading"}
}

func (tb *Torbox) GetTorrents() ([]*types.Torrent, error) {
	allTorrents := make([]*types.Torrent, 0)
	var lastErr error

	for _, acc := range tb.accountsManager.All() {
		offset := 0
		for {
			url := fmt.Sprintf("%s/api/torrents/mylist?offset=%d", tb.Host, offset)
			req, _ := http.NewRequest(http.MethodGet, url, nil)
			resp, err := acc.Client().MakeRequest(req)
			if err != nil {
				lastErr = err
				break
			}
		
			var res TorrentsListResponse
			err = json.Unmarshal(resp, &res)
			if err != nil {
				lastErr = err
				break
			}
		
			if !res.Success || res.Data == nil {
				lastErr = fmt.Errorf("torbox API error: %v", res.Error)
				break
			}
		
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
					file := types.File{
						TorrentId: t.Id,
						Id:        strconv.Itoa(f.Id),
						Name:      fileName,
						Size:      f.Size,
						Path:      f.Name,
					}
		
					if data.DownloadFinished {
						file.Link = fmt.Sprintf("torbox://%s/%d", t.Id, f.Id)
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
		
				allTorrents = append(allTorrents, t)
			}
			
			total := len(*res.Data)
			if total == 0 {
				break
			}
			offset += total
		}
	}
	
	if len(allTorrents) == 0 && lastErr != nil {
		return nil, lastErr
	}

	return allTorrents, nil
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
