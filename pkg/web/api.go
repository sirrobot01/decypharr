package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/wire"
	"golang.org/x/crypto/bcrypt"

	"encoding/json"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/version"
)

func (wb *Web) handleGetArrs(w http.ResponseWriter, r *http.Request) {
	arrStorage := wire.Get().Arr()
	request.JSONResponse(w, arrStorage.GetAll(), http.StatusOK)
}

func (wb *Web) handleAddContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_store := wire.Get()

	results := make([]*wire.ImportRequest, 0)
	errs := make([]string, 0)

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	debridName := r.FormValue("debrid")
	callbackUrl := r.FormValue("callbackUrl")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" {
		downloadFolder = config.Get().QBitTorrent.DownloadFolder
	}
	skipMultiSeason := r.FormValue("skipMultiSeason") == "true"

	downloadUncached := r.FormValue("downloadUncached") == "true"
	rmTrackerUrls := r.FormValue("rmTrackerUrls") == "true"

	// Check config setting - if always remove tracker URLs is enabled, force it to true
	cfg := config.Get()
	if cfg.QBitTorrent.AlwaysRmTrackerUrls {
		rmTrackerUrls = true
	}

	_arr := _store.Arr().Get(arrName)
	if _arr == nil {
		// These are not found in the config. They are throwaway arrs.
		_arr = arr.New(arrName, "", "", false, false, &downloadUncached, "", "")
	}

	// Handle URLs
	if urls := r.FormValue("urls"); urls != "" {
		var urlList []string
		for _, u := range strings.Split(urls, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				urlList = append(urlList, trimmed)
			}
		}

		for _, url := range urlList {
			magnet, err := utils.GetMagnetFromUrl(url, rmTrackerUrls)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to parse URL %s: %v", url, err))
				continue
			}

			importReq := wire.NewImportRequest(debridName, downloadFolder, magnet, _arr, action, downloadUncached, callbackUrl, wire.ImportTypeAPI, skipMultiSeason)
			if err := _store.AddTorrent(ctx, importReq); err != nil {
				wb.logger.Error().Err(err).Str("url", url).Msg("Failed to add torrent")
				errs = append(errs, fmt.Sprintf("URL %s: %v", url, err))
				continue
			}
			results = append(results, importReq)
		}
	}

	// Handle torrent/magnet files
	if files := r.MultipartForm.File["files"]; len(files) > 0 {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to open file %s: %v", fileHeader.Filename, err))
				continue
			}

			magnet, err := utils.GetMagnetFromFile(file, fileHeader.Filename, rmTrackerUrls)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to parse torrent file %s: %v", fileHeader.Filename, err))
				continue
			}

			importReq := wire.NewImportRequest(debridName, downloadFolder, magnet, _arr, action, downloadUncached, callbackUrl, wire.ImportTypeAPI, skipMultiSeason)
			err = _store.AddTorrent(ctx, importReq)
			if err != nil {
				wb.logger.Error().Err(err).Str("file", fileHeader.Filename).Msg("Failed to add torrent")
				errs = append(errs, fmt.Sprintf("File %s: %v", fileHeader.Filename, err))
				continue
			}
			results = append(results, importReq)
		}
	}

	request.JSONResponse(w, struct {
		Results []*wire.ImportRequest `json:"results"`
		Errors  []string              `json:"errors,omitempty"`
	}{
		Results: results,
		Errors:  errs,
	}, http.StatusOK)
}

func (wb *Web) handleRepairMedia(w http.ResponseWriter, r *http.Request) {
	var req RepairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	_store := wire.Get()

	var arrs []string

	if req.ArrName != "" {
		_arr := _store.Arr().Get(req.ArrName)
		if _arr == nil {
			http.Error(w, "No Arrs found to repair", http.StatusNotFound)
			return
		}
		arrs = append(arrs, req.ArrName)
	}

	if req.Async {
		go func() {
			if err := _store.Repair().AddJob(arrs, req.MediaIds, req.AutoProcess, false); err != nil {
				wb.logger.Error().Err(err).Msg("Failed to repair media")
			}
		}()
		request.JSONResponse(w, "Repair process started", http.StatusOK)
		return
	}

	if err := _store.Repair().AddJob([]string{req.ArrName}, req.MediaIds, req.AutoProcess, false); err != nil {
		http.Error(w, fmt.Sprintf("Failed to repair: %v", err), http.StatusInternalServerError)
		return
	}

	request.JSONResponse(w, "Repair completed", http.StatusOK)
}

func (wb *Web) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	v := version.GetInfo()
	request.JSONResponse(w, v, http.StatusOK)
}

func (wb *Web) handleGetTorrents(w http.ResponseWriter, r *http.Request) {
	request.JSONResponse(w, wb.torrents.GetAllSorted("", "", nil, "added_on", false), http.StatusOK)
}

func (wb *Web) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	category := chi.URLParam(r, "category")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hash == "" {
		http.Error(w, "No hash provided", http.StatusBadRequest)
		return
	}
	wb.torrents.Delete(hash, category, removeFromDebrid)
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleDeleteTorrents(w http.ResponseWriter, r *http.Request) {
	hashesStr := r.URL.Query().Get("hashes")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hashesStr == "" {
		http.Error(w, "No hashes provided", http.StatusBadRequest)
		return
	}
	hashes := strings.Split(hashesStr, ",")
	wb.torrents.DeleteMultiple(hashes, removeFromDebrid)
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	arrStorage := wire.Get().Arr()
	cfg := config.Get()
	cfg.Arrs = arrStorage.SyncToConfig()

	// Create response with API token info
	type ConfigResponse struct {
		*config.Config
		APIToken     string `json:"api_token,omitempty"`
		AuthUsername string `json:"auth_username,omitempty"`
	}

	response := &ConfigResponse{Config: cfg}

	// Add API token and auth information
	auth := cfg.GetAuth()
	if auth != nil {
		if auth.APIToken != "" {
			response.APIToken = auth.APIToken
		}
		response.AuthUsername = auth.Username
	}

	request.JSONResponse(w, response, http.StatusOK)
}

func (wb *Web) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	// Decode the JSON body
	var updatedConfig config.Config
	if err := json.NewDecoder(r.Body).Decode(&updatedConfig); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to decode config update request")
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get the current configuration
	currentConfig := config.Get()

	// Update fields that can be changed
	currentConfig.LogLevel = updatedConfig.LogLevel
	currentConfig.MinFileSize = updatedConfig.MinFileSize
	currentConfig.MaxFileSize = updatedConfig.MaxFileSize
	currentConfig.RemoveStalledAfter = updatedConfig.RemoveStalledAfter
	currentConfig.AllowedExt = updatedConfig.AllowedExt
	currentConfig.DiscordWebhook = updatedConfig.DiscordWebhook
	currentConfig.CallbackURL = updatedConfig.CallbackURL

	// Should this be added?
	currentConfig.URLBase = updatedConfig.URLBase
	currentConfig.BindAddress = updatedConfig.BindAddress
	currentConfig.Port = updatedConfig.Port

	// Update QBitTorrent config
	currentConfig.QBitTorrent = updatedConfig.QBitTorrent

	// Update Repair config
	currentConfig.Repair = updatedConfig.Repair
	currentConfig.Rclone = updatedConfig.Rclone

	// Update Debrids
	currentConfig.Debrids = updatedConfig.Debrids

	// Update Arrs through the service
	storage := wire.Get()
	arrStorage := storage.Arr()

	newConfigArrs := make([]config.Arr, 0)
	for _, a := range updatedConfig.Arrs {
		if a.Name == "" || a.Host == "" || a.Token == "" {
			// Skip empty or auto-generated arrs
			continue
		}
		newConfigArrs = append(newConfigArrs, a)
	}
	currentConfig.Arrs = newConfigArrs

	// Sync arrStorage with the new arrs
	arrStorage.SyncFromConfig(currentConfig.Arrs)

	if err := currentConfig.Save(); err != nil {
		http.Error(w, "Error saving config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if restartFunc != nil {
		go func() {
			// Small delay to ensure the response is sent
			time.Sleep(200 * time.Millisecond)
			restartFunc()
		}()
	}

	// Return success
	request.JSONResponse(w, map[string]string{"status": "success"}, http.StatusOK)
}

func (wb *Web) handleGetRepairJobs(w http.ResponseWriter, r *http.Request) {
	_store := wire.Get()
	request.JSONResponse(w, _store.Repair().GetJobs(), http.StatusOK)
}

func (wb *Web) handleProcessRepairJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No job ID provided", http.StatusBadRequest)
		return
	}
	_store := wire.Get()
	if err := _store.Repair().ProcessJob(id); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to process repair job")
	}
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleDeleteRepairJob(w http.ResponseWriter, r *http.Request) {
	// Read ids from body
	var req struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(req.IDs) == 0 {
		http.Error(w, "No job IDs provided", http.StatusBadRequest)
		return
	}

	_store := wire.Get()
	_store.Repair().DeleteJobs(req.IDs)
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleStopRepairJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No job ID provided", http.StatusBadRequest)
		return
	}
	_store := wire.Get()
	if err := _store.Repair().StopJob(id); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to stop repair job")
		http.Error(w, "Failed to stop job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleRefreshAPIToken(w http.ResponseWriter, _ *http.Request) {
	token, err := wb.refreshAPIToken()
	if err != nil {
		wb.logger.Error().Err(err).Msg("Failed to refresh API token")
		http.Error(w, "Failed to refresh token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	request.JSONResponse(w, map[string]interface{}{
		"token":   token,
		"message": "API token refreshed successfully",
	}, http.StatusOK)
}

func (wb *Web) handleUpdateAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	cfg := config.Get()
	auth := cfg.GetAuth()
	if auth == nil {
		auth = &config.Auth{}
	}

	// Check if trying to disable authentication (both empty)
	if req.Username == "" && req.Password == "" {
		// Disable authentication
		cfg.UseAuth = false
		auth.Username = ""
		auth.Password = ""
		if err := cfg.SaveAuth(auth); err != nil {
			wb.logger.Error().Err(err).Msg("Failed to save auth config")
			http.Error(w, "Failed to save authentication settings", http.StatusInternalServerError)
			return
		}
		if err := cfg.Save(); err != nil {
			wb.logger.Error().Err(err).Msg("Failed to save config")
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		request.JSONResponse(w, map[string]string{
			"message": "Authentication disabled successfully",
		}, http.StatusOK)
		return
	}

	// Validate required fields
	if req.Username == "" {
		http.Error(w, "Username is required", http.StatusBadRequest)
		return
	}
	if req.Password == "" {
		http.Error(w, "Password is required", http.StatusBadRequest)
		return
	}
	if req.Password != req.ConfirmPassword {
		http.Error(w, "Passwords do not match", http.StatusBadRequest)
		return
	}

	// Hash the password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		wb.logger.Error().Err(err).Msg("Failed to hash password")
		http.Error(w, "Failed to process password", http.StatusInternalServerError)
		return
	}

	// Update auth settings
	auth.Username = req.Username
	auth.Password = string(hashedPassword)
	cfg.UseAuth = true

	// Save auth config
	if err := cfg.SaveAuth(auth); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to save auth config")
		http.Error(w, "Failed to save authentication settings", http.StatusInternalServerError)
		return
	}

	// Save main config
	if err := cfg.Save(); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to save config")
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	request.JSONResponse(w, map[string]string{
		"message": "Authentication settings updated successfully",
	}, http.StatusOK)
}
