package web

import (
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/store"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
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
	cfg := config.Get()

	results := make([]*store.ImportRequest, 0)
	errs := make([]string, 0)

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	debridName := r.FormValue("debrid")
	callbackUrl := r.FormValue("callbackUrl")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" && cfg.QBitTorrent != nil {
		downloadFolder = cfg.QBitTorrent.DownloadFolder
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
	currentConfig.URLBase = updatedConfig.URLBase
	currentConfig.BindAddress = updatedConfig.BindAddress
	currentConfig.Port = updatedConfig.Port

	// Update QBitTorrent config
	currentConfig.QBitTorrent = updatedConfig.QBitTorrent

	// Update Repair config
	currentConfig.Repair = updatedConfig.Repair

	// Update Debrids
	if len(updatedConfig.Debrids) > 0 {
		currentConfig.Debrids = updatedConfig.Debrids
	}

	currentConfig.Usenet = updatedConfig.Usenet
	currentConfig.SABnzbd = updatedConfig.SABnzbd

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

// NZB API Handlers

func (wb *Web) handleGetNZBs(w http.ResponseWriter, r *http.Request) {
	// Get query parameters for filtering
	status := r.URL.Query().Get("status")
	category := r.URL.Query().Get("category")
	nzbs := wb.usenet.Store().GetQueue()

	// Apply filters if provided
	filteredNZBs := make([]*usenet.NZB, 0)
	for _, nzb := range nzbs {
		if status != "" && nzb.Status != status {
			continue
		}
		if category != "" && nzb.Category != category {
			continue
		}
		filteredNZBs = append(filteredNZBs, nzb)
	}

	response := map[string]interface{}{
		"nzbs":  filteredNZBs,
		"count": len(filteredNZBs),
	}

	request.JSONResponse(w, response, http.StatusOK)
}

func (wb *Web) handleDeleteNZB(w http.ResponseWriter, r *http.Request) {
	nzbID := chi.URLParam(r, "id")
	if nzbID == "" {
		http.Error(w, "No NZB ID provided", http.StatusBadRequest)
		return
	}
	wb.usenet.Store().RemoveFromQueue(nzbID)

	wb.logger.Info().Str("nzb_id", nzbID).Msg("NZB delete requested")
	request.JSONResponse(w, map[string]string{"status": "success"}, http.StatusOK)
}

func (wb *Web) handleAddNZBContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cfg := config.Get()
	_store := store.Get()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	results := make([]interface{}, 0)
	errs := make([]string, 0)

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" {
		downloadFolder = cfg.SABnzbd.DownloadFolder
	}

	_arr := _store.Arr().Get(arrName)
	if _arr == nil {
		// These are not found in the config. They are throwaway arrs.
		_arr = arr.New(arrName, "", "", false, false, nil, "", "")
	}
	_nzbURLS := r.FormValue("nzbUrls")
	urlList := make([]string, 0)
	if _nzbURLS != "" {
		for _, u := range strings.Split(_nzbURLS, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				urlList = append(urlList, trimmed)
			}
		}
	}
	files := r.MultipartForm.File["nzbFiles"]
	totalItems := len(files) + len(urlList)
	if totalItems == 0 {
		request.JSONResponse(w, map[string]any{
			"results": nil,
			"errors":  "No NZB URLs or files provided",
		}, http.StatusBadRequest)
		return
	}

	var wg sync.WaitGroup
	for _, url := range urlList {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return // Exit if context is done
			default:
			}
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				errs = append(errs, fmt.Sprintf("Invalid URL format: %s", url))
				return
			}
			// Download the NZB file from the URL
			filename, content, err := utils.DownloadFile(url)
			if err != nil {
				wb.logger.Error().Err(err).Str("url", url).Msg("Failed to download NZB from URL")
				errs = append(errs, fmt.Sprintf("Failed to download NZB from URL %s: %v", url, err))
				return // Continue processing other URLs
			}
			req := &usenet.ProcessRequest{
				NZBContent:  content,
				Name:        filename,
				Arr:         _arr,
				Action:      action,
				DownloadDir: downloadFolder,
			}
			nzb, err := wb.usenet.ProcessNZB(ctx, req)
			if err != nil {
				errs = append(errs, fmt.Sprintf("Failed to process NZB from URL %s: %v", url, err))
				return
			}
			wb.logger.Info().Str("nzb_id", nzb.ID).Str("url", url).Msg("NZB added from URL")

			result := map[string]interface{}{
				"id":       nzb.ID,
				"name":     "NZB from URL",
				"url":      url,
				"category": arrName,
			}
			results = append(results, result)
		}(url)
	}

	// Handle NZB files
	for _, fileHeader := range files {
		wg.Add(1)
		go func(fileHeader *multipart.FileHeader) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			default:
			}
			file, err := fileHeader.Open()
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to open NZB file %s: %v", fileHeader.Filename, err))
				return
			}
			defer file.Close()

			content, err := io.ReadAll(file)
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to read NZB file %s: %v", fileHeader.Filename, err))
				return
			}
			req := &usenet.ProcessRequest{
				NZBContent:  content,
				Name:        fileHeader.Filename,
				Arr:         _arr,
				Action:      action,
				DownloadDir: downloadFolder,
			}
			nzb, err := wb.usenet.ProcessNZB(ctx, req)
			if err != nil {
				errs = append(errs, fmt.Sprintf("failed to process NZB file %s: %v", fileHeader.Filename, err))
				return
			}
			wb.logger.Info().Str("nzb_id", nzb.ID).Str("file", fileHeader.Filename).Msg("NZB added from file")
			// Simulate successful addition
			result := map[string]interface{}{
				"id":       nzb.ID,
				"name":     fileHeader.Filename,
				"filename": fileHeader.Filename,
				"category": arrName,
			}
			results = append(results, result)
		}(fileHeader)
	}

	// Wait for all goroutines to finish
	wg.Wait()

	// Validation
	if len(results) == 0 && len(errs) == 0 {
		request.JSONResponse(w, map[string]any{
			"results": nil,
			"errors":  "No NZB URLs or files processed successfully",
		}, http.StatusBadRequest)
		return
	}

	request.JSONResponse(w, struct {
		Results []interface{} `json:"results"`
		Errors  []string      `json:"errors,omitempty"`
	}{
		Results: results,
		Errors:  errs,
	}, http.StatusOK)
}
