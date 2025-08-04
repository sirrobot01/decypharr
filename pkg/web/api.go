package web

import (
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/store"
	"net/http"
	"strings"
	"time"

	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/version"
)

func (wb *Web) handleGetArrs(w http.ResponseWriter, r *http.Request) {
	_store := store.Get()
	request.JSONResponse(w, _store.Arr().GetAll(), http.StatusOK)
}

func (wb *Web) handleAddContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_store := store.Get()

	results := make([]*store.ImportRequest, 0)
	errs := make([]string, 0)

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	debridName := r.FormValue("debrid")
	callbackUrl := r.FormValue("callbackUrl")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" {
		downloadFolder = config.Get().QBitTorrent.DownloadFolder
	}

	downloadUncached := r.FormValue("downloadUncached") == "true"

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
			magnet, err := utils.GetMagnetFromUrl(url)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to parse URL %s: %v", url, err))
				continue
			}

			importReq := store.NewImportRequest(debridName, downloadFolder, magnet, _arr, action, downloadUncached, callbackUrl, store.ImportTypeAPI)
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

			magnet, err := utils.GetMagnetFromFile(file, fileHeader.Filename)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to parse torrent file %s: %v", fileHeader.Filename, err))
				continue
			}

			importReq := store.NewImportRequest(debridName, downloadFolder, magnet, _arr, action, downloadUncached, callbackUrl, store.ImportTypeAPI)
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
		Results []*store.ImportRequest `json:"results"`
		Errors  []string               `json:"errors,omitempty"`
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

	_store := store.Get()

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
	// Merge config arrs, with arr Storage
	unique := map[string]config.Arr{}
	cfg := config.Get()
	arrStorage := store.Get().Arr()

	// Add existing Arrs from storage
	for _, a := range arrStorage.GetAll() {
		if _, ok := unique[a.Name]; !ok {
			// Only add if not already in the unique map
			unique[a.Name] = config.Arr{
				Name:             a.Name,
				Host:             a.Host,
				Token:            a.Token,
				Cleanup:          a.Cleanup,
				SkipRepair:       a.SkipRepair,
				DownloadUncached: a.DownloadUncached,
				SelectedDebrid:   a.SelectedDebrid,
				Source:           a.Source,
			}
		}
	}

	for _, a := range cfg.Arrs {
		if a.Host == "" || a.Token == "" {
			continue // Skip empty arrs
		}
		unique[a.Name] = a
	}
	cfg.Arrs = make([]config.Arr, 0, len(unique))
	for _, a := range unique {
		cfg.Arrs = append(cfg.Arrs, a)
	}
	request.JSONResponse(w, cfg, http.StatusOK)
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
	if len(updatedConfig.Debrids) > 0 {
		currentConfig.Debrids = updatedConfig.Debrids
		// Clear legacy single debrid if using array
	}

	// Update Arrs through the service
	storage := store.Get()
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

	// Add config arr into the config
	for _, a := range currentConfig.Arrs {
		if a.Host == "" || a.Token == "" {
			continue // Skip empty arrs
		}
		existingArr := arrStorage.Get(a.Name)
		if existingArr != nil {
			// Update existing Arr
			existingArr.Host = a.Host
			existingArr.Token = a.Token
			existingArr.Cleanup = a.Cleanup
			existingArr.SkipRepair = a.SkipRepair
			existingArr.DownloadUncached = a.DownloadUncached
			existingArr.SelectedDebrid = a.SelectedDebrid
			existingArr.Source = a.Source
			arrStorage.AddOrUpdate(existingArr)
		} else {
			// Create new Arr if it doesn't exist
			newArr := arr.New(a.Name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source)
			arrStorage.AddOrUpdate(newArr)
		}
	}

	if err := currentConfig.Save(); err != nil {
		http.Error(w, "Error saving config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if restartFunc != nil {
		go func() {
			// Small delay to ensure the response is sent
			time.Sleep(500 * time.Millisecond)
			restartFunc()
		}()
	}

	// Return success
	request.JSONResponse(w, map[string]string{"status": "success"}, http.StatusOK)
}

func (wb *Web) handleGetRepairJobs(w http.ResponseWriter, r *http.Request) {
	_store := store.Get()
	request.JSONResponse(w, _store.Repair().GetJobs(), http.StatusOK)
}

func (wb *Web) handleProcessRepairJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No job ID provided", http.StatusBadRequest)
		return
	}
	_store := store.Get()
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

	_store := store.Get()
	_store.Repair().DeleteJobs(req.IDs)
	w.WriteHeader(http.StatusOK)
}

func (wb *Web) handleStopRepairJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No job ID provided", http.StatusBadRequest)
		return
	}
	_store := store.Get()
	if err := _store.Repair().StopJob(id); err != nil {
		wb.logger.Error().Err(err).Msg("Failed to stop repair job")
		http.Error(w, "Failed to stop job: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
