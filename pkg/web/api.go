package web

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"encoding/json"
	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/qbit"
	"github.com/sirrobot01/decypharr/pkg/service"
	"github.com/sirrobot01/decypharr/pkg/version"
)

func (ui *Handler) handleGetArrs(w http.ResponseWriter, r *http.Request) {
	svc := service.GetService()
	request.JSONResponse(w, svc.Arr.GetAll(), http.StatusOK)
}

func (ui *Handler) handleAddContent(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	svc := service.GetService()

	results := make([]*qbit.ImportRequest, 0)
	errs := make([]string, 0)

	arrName := r.FormValue("arr")
	notSymlink := r.FormValue("notSymlink") == "true"
	downloadUncached := r.FormValue("downloadUncached") == "true"
	if arrName == "" {
		arrName = "uncategorized"
	}

	_arr := svc.Arr.Get(arrName)
	if _arr == nil {
		_arr = arr.New(arrName, "", "", false, false, &downloadUncached)
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
			importReq := qbit.NewImportRequest(magnet, _arr, !notSymlink, downloadUncached)
			if err := importReq.Process(ui.qbit); err != nil {
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

			importReq := qbit.NewImportRequest(magnet, _arr, !notSymlink, downloadUncached)
			err = importReq.Process(ui.qbit)
			if err != nil {
				errs = append(errs, fmt.Sprintf("File %s: %v", fileHeader.Filename, err))
				continue
			}
			results = append(results, importReq)
		}
	}

	request.JSONResponse(w, struct {
		Results []*qbit.ImportRequest `json:"results"`
		Errors  []string              `json:"errors,omitempty"`
	}{
		Results: results,
		Errors:  errs,
	}, http.StatusOK)
}

func (ui *Handler) handleRepairMedia(w http.ResponseWriter, r *http.Request) {
	var req RepairRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	svc := service.GetService()

	var arrs []string

	if req.ArrName != "" {
		_arr := svc.Arr.Get(req.ArrName)
		if _arr == nil {
			http.Error(w, "No Arrs found to repair", http.StatusNotFound)
			return
		}
		arrs = append(arrs, req.ArrName)
	}

	if req.Async {
		go func() {
			if err := svc.Repair.AddJob(arrs, req.MediaIds, req.AutoProcess, false); err != nil {
				ui.logger.Error().Err(err).Msg("Failed to repair media")
			}
		}()
		request.JSONResponse(w, "Repair process started", http.StatusOK)
		return
	}

	if err := svc.Repair.AddJob([]string{req.ArrName}, req.MediaIds, req.AutoProcess, false); err != nil {
		http.Error(w, fmt.Sprintf("Failed to repair: %v", err), http.StatusInternalServerError)
		return
	}

	request.JSONResponse(w, "Repair completed", http.StatusOK)
}

func (ui *Handler) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	v := version.GetInfo()
	request.JSONResponse(w, v, http.StatusOK)
}

func (ui *Handler) handleGetTorrents(w http.ResponseWriter, r *http.Request) {
	request.JSONResponse(w, ui.qbit.Storage.GetAllSorted("", "", nil, "added_on", false), http.StatusOK)
}

func (ui *Handler) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	category := chi.URLParam(r, "category")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hash == "" {
		http.Error(w, "No hash provided", http.StatusBadRequest)
		return
	}
	ui.qbit.Storage.Delete(hash, category, removeFromDebrid)
	w.WriteHeader(http.StatusOK)
}

func (ui *Handler) handleDeleteTorrents(w http.ResponseWriter, r *http.Request) {
	hashesStr := r.URL.Query().Get("hashes")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hashesStr == "" {
		http.Error(w, "No hashes provided", http.StatusBadRequest)
		return
	}
	hashes := strings.Split(hashesStr, ",")
	ui.qbit.Storage.DeleteMultiple(hashes, removeFromDebrid)
	w.WriteHeader(http.StatusOK)
}

func (ui *Handler) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	arrCfgs := make([]config.Arr, 0)
	svc := service.GetService()
	for _, a := range svc.Arr.GetAll() {
		arrCfgs = append(arrCfgs, config.Arr{
			Host:             a.Host,
			Name:             a.Name,
			Token:            a.Token,
			Cleanup:          a.Cleanup,
			SkipRepair:       a.SkipRepair,
			DownloadUncached: a.DownloadUncached,
		})
	}
	cfg.Arrs = arrCfgs
	request.JSONResponse(w, cfg, http.StatusOK)
}

func (ui *Handler) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	// Decode the JSON body
	var updatedConfig config.Config
	if err := json.NewDecoder(r.Body).Decode(&updatedConfig); err != nil {
		ui.logger.Error().Err(err).Msg("Failed to decode config update request")
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get the current configuration
	currentConfig := config.Get()

	// Update fields that can be changed
	currentConfig.LogLevel = updatedConfig.LogLevel
	currentConfig.MinFileSize = updatedConfig.MinFileSize
	currentConfig.MaxFileSize = updatedConfig.MaxFileSize
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

	// Update Debrids
	if len(updatedConfig.Debrids) > 0 {
		currentConfig.Debrids = updatedConfig.Debrids
		// Clear legacy single debrid if using array
	}

	if len(updatedConfig.Arrs) > 0 {
		currentConfig.Arrs = updatedConfig.Arrs
	}

	// Update Arrs through the service
	svc := service.GetService()
	svc.Arr.Clear() // Clear existing arrs

	for _, a := range updatedConfig.Arrs {
		svc.Arr.AddOrUpdate(&arr.Arr{
			Name:             a.Name,
			Host:             a.Host,
			Token:            a.Token,
			Cleanup:          a.Cleanup,
			SkipRepair:       a.SkipRepair,
			DownloadUncached: a.DownloadUncached,
		})
	}
	currentConfig.Arrs = updatedConfig.Arrs
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

func (ui *Handler) handleGetRepairJobs(w http.ResponseWriter, r *http.Request) {
	svc := service.GetService()
	request.JSONResponse(w, svc.Repair.GetJobs(), http.StatusOK)
}

func (ui *Handler) handleProcessRepairJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No job ID provided", http.StatusBadRequest)
		return
	}
	svc := service.GetService()
	if err := svc.Repair.ProcessJob(id); err != nil {
		ui.logger.Error().Err(err).Msg("Failed to process repair job")
	}
	w.WriteHeader(http.StatusOK)
}

func (ui *Handler) handleDeleteRepairJob(w http.ResponseWriter, r *http.Request) {
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

	svc := service.GetService()
	svc.Repair.DeleteJobs(req.IDs)
	w.WriteHeader(http.StatusOK)
}
