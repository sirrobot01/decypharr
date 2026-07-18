package sabnzbd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// handleAPI is the main handler for all SABnzbd API requests
func (s *SABnzbd) handleAPI(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	mode := getMode(ctx)

	switch mode {
	case ModeQueue:
		s.handleQueue(w, r)
	case ModeHistory:
		s.handleHistory(w, r)
	case ModeConfig:
		s.handleConfig(w, r)
	case ModeStatus, ModeFullStatus:
		s.handleStatus(w, r)
	case ModeGetConfig:
		s.handleConfig(w, r)
	case ModeAddURL:
		s.handleAddURL(w, r)
	case ModeAddFile:
		s.handleAddFile(w, r)
	case ModeVersion:
		s.handleVersion(w, r)
	case ModeGetCats:
		s.handleGetCategories(w, r)
	case ModeGetScripts:
		s.handleGetScripts(w, r)
	case ModeGetFiles:
		s.handleGetFiles(w, r)
	default:
		// Default to queue if no mode specified
		s.logger.Warn().Str("mode", mode).Msg("Unknown API mode, returning 404")
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

func (s *SABnzbd) handleQueue(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		s.handleListQueue(w, r)
		return
	}
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "delete":
		s.handleDelete(w, r)
	case "pause":
		s.handleQueuePause(w, r)
	case "resume":
		s.handleQueueResume(w, r)
	}
}

// handleResume handles resume operations
func (s *SABnzbd) handleQueueResume(w http.ResponseWriter, r *http.Request) {
	response := StatusResponse{Status: true}
	utils.JSONResponse(w, response, http.StatusOK)
}

// handleDelete handles delete operations
func (s *SABnzbd) handleDelete(w http.ResponseWriter, r *http.Request) {
	nzoIDs := r.URL.Query().Get("value")
	cat := getCategory(r.Context())
	if nzoIDs == "" {
		s.writeError(w, "No NZB IDs provided", http.StatusBadRequest)
		return
	}

	var successCount int
	var errors []string

	if nzoIDs == "failed" {
		// Delete all failed entries
		if err := s.manager.Queue().DeleteWhere(cat, config.ProtocolNZB, storage.EntryStateError, nil, nil); err != nil {
			s.logger.Error().
				Err(err).
				Msg("Failed to delete all failed NZBs")
			s.writeError(w, fmt.Sprintf("Failed to delete failed NZBs: %v", err), http.StatusInternalServerError)
			return
		}
		response := StatusResponse{
			Status: true,
		}
		utils.JSONResponse(w, response, http.StatusOK)
		return
	}

	for nzoID := range strings.SplitSeq(nzoIDs, ",") {
		nzoID = strings.TrimSpace(nzoID)
		if nzoID == "" {
			continue // Skip empty IDs
		}

		// Use atomic delete operation
		if err := s.manager.Queue().Delete(nzoID, nil); err != nil {
			errors = append(errors, fmt.Sprintf("Failed to delete %s: %v", nzoID, err))
		} else {
			successCount++
		}
	}

	// Return response with success/error information
	if len(errors) > 0 {
		if successCount == 0 {
			// All deletions failed
			s.writeError(w, fmt.Sprintf("All deletions failed: %s", strings.Join(errors, "; ")), http.StatusInternalServerError)
			return
		}

		// Partial success
		s.logger.Warn().
			Int("success_count", successCount).
			Int("error_count", len(errors)).
			Strs("errors", errors).
			Msg("Partial success in queue deletion")
	}

	response := StatusResponse{
		Status: true,
		Error:  "", // Could add error details here if needed
	}
	utils.JSONResponse(w, response, http.StatusOK)
}

// handlePause handles pause operations
func (s *SABnzbd) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	response := StatusResponse{Status: true}
	utils.JSONResponse(w, response, http.StatusOK)
}

// handleQueue returns the current download queue
func (s *SABnzbd) handleListQueue(w http.ResponseWriter, r *http.Request) {
	category := getCategory(r.Context())
	nzoIDsVal := r.URL.Query().Get("nzo_ids")
	var nzoIDs []string
	if nzoIDsVal != "" {
		nzoIDs = strings.Split(nzoIDsVal, ",")
	}

	entries := s.manager.Queue().ListFilter(category, config.ProtocolNZB, storage.EntryStateDownloading, nzoIDs, "added_on", false)

	queue := Queue{
		Version: Version,
		Slots:   []QueueSlot{},
	}

	const MB = 1024 * 1024

	// Convert NZBs to queue slots
	for index, e := range entries {
		nzb := convertToSABnzbdNZB(e)

		// Calculate size values as strings (SABnzbd format)
		sizeMB := float64(e.Size) / float64(MB)
		mbLeft := sizeMB * (1 - e.Progress)
		sizeStr := formatSize(e.Size)
		sizeLeftBytes := int64(float64(e.Size) * (1 - e.Progress))
		sizeLeftStr := formatSize(sizeLeftBytes)

		// get labels from entry
		labels := e.Tags
		if labels == nil {
			labels = []string{}
		}

		slot := QueueSlot{
			Status:       nzb.Status,
			Index:        index,
			Password:     "", // We don't expose passwords
			AvgAge:       "0d",
			TimeAdded:    e.CreatedAt.Unix(),
			Script:       "None",
			DirectUnpack: "", // null in SABnzbd when not active
			Mb:           fmt.Sprintf("%.2f", sizeMB),
			MBLeft:       fmt.Sprintf("%.2f", mbLeft),
			MBMissing:    "0.0",
			Size:         sizeStr,
			SizeLeft:     sizeLeftStr,
			Filename:     nzb.Name,
			Labels:       labels,
			Priority:     nzb.Priority,
			Cat:          nzb.Category,
			TimeLeft:     nzb.TimeLeft,
			Percentage:   fmt.Sprintf("%.0f", nzb.Percentage),
			NzoId:        nzb.NzoId,
			Unpackopts:   "3", // Default: +Repair/Unpack/Delete
		}
		queue.Slots = append(queue.Slots, slot)
	}

	response := QueueResponse{
		Queue:   queue,
		Status:  true,
		Version: Version,
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

func (s *SABnzbd) handleHistory(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		s.handleHistoryList(w, r)
		return
	}
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "delete":
		s.handleDelete(w, r)
	default:
		s.writeError(w, "Unknown history action", http.StatusBadRequest)
	}
}

// handleHistoryList returns the download history
func (s *SABnzbd) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	if limitStr == "" {
		limitStr = "0"
	}
	limit, err := strconv.Atoi(limitStr)
	if err != nil {
		s.logger.Error().Err(err).Msg("Invalid limit parameter for history")
		s.writeError(w, "Invalid limit parameter", http.StatusBadRequest)
		return
	}
	if limit < 0 {
		limit = 0
	}
	nzoIDsValue := r.URL.Query().Get("nzo_ids")
	var nzoIDs []string
	if nzoIDsValue != "" {
		for id := range strings.SplitSeq(nzoIDsValue, ",") {
			nzoIDs = append(nzoIDs, id)
		}
	}
	history := s.getHistory(r.Context(), limit, nzoIDs)

	response := HistoryResponse{
		History: history,
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

// handleConfig returns the configuration
func (s *SABnzbd) handleConfig(w http.ResponseWriter, r *http.Request) {

	response := ConfigResponse{
		Config: s.config,
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

// handleAddURL handles adding NZB by URL (supports multiple URLs separated by newlines)
func (s *SABnzbd) handleAddURL(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_arr := getArrFromContext(ctx)
	cat := getCategory(ctx)

	if _arr == nil {
		// If Arr is not in context, create a new one with default values
		_arr = arr.New(cat, "", "", false, false, nil, "", "")
	}

	if r.Method != http.MethodPost {
		s.logger.Warn().Str("method", r.Method).Msg("Invalid method")
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	urls := r.URL.Query().Get("name")

	cfg := config.Get()
	action := cfg.DefaultDownloadAction
	if r.URL.Query().Get("action") != "" {
		action = config.DownloadAction(r.URL.Query().Get("action"))
	}

	if urls == "" {
		s.writeError(w, "URL is required", http.StatusBadRequest)
		return
	}

	// Split URLs by newline to support multiple URLs
	urlList := strings.Split(urls, "\n")
	var nzoIDs []string
	var errors []string

	for _, url := range urlList {
		url = strings.TrimSpace(url)
		if url == "" {
			continue
		}

		nzoID, err := s.addNZBURL(ctx, url, _arr, action)
		if err != nil {
			s.logger.Error().Err(err).Str("url", url).Msg("Failed to add NZB from URL")
			errors = append(errors, fmt.Sprintf("Failed to add %s: %v", url, err))
			continue
		}
		if nzoID != "" {
			nzoIDs = append(nzoIDs, nzoID)
		}
	}

	if len(nzoIDs) == 0 {
		errMsg := "Failed to add any NZBs"
		if len(errors) > 0 {
			errMsg = strings.Join(errors, "; ")
		}
		s.writeError(w, errMsg, http.StatusInternalServerError)
		return
	}

	response := AddNZBResponse{
		Status: true,
		NzoIds: nzoIDs,
	}

	// Include partial errors if some URLs failed
	if len(errors) > 0 {
		response.Error = fmt.Sprintf("Partial success: %s", strings.Join(errors, "; "))
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

// handleAddFile handles NZB file uploads (supports multiple files)
func (s *SABnzbd) handleAddFile(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_arr := getArrFromContext(ctx)
	cat := getCategory(ctx)

	if _arr == nil {
		// If Arr is not in context, create a new one with default values
		_arr = arr.New(cat, "", "", false, false, nil, "", "")
	}

	if r.Method != http.MethodPost {
		s.writeError(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form with larger limit for multiple files
	err := r.ParseMultipartForm(100 << 20) // 100 MB limit for multiple files
	if err != nil {
		s.writeError(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}

	cfg := config.Get()
	action := cfg.DefaultDownloadAction
	if r.URL.Query().Get("action") != "" {
		action = config.DownloadAction(r.URL.Query().Get("action"))
	}

	var nzoIDs []string
	var errors []string

	// Try to get multiple files from "name" field
	if r.MultipartForm != nil && r.MultipartForm.File != nil {
		// Parse all files from "name" field
		files := r.MultipartForm.File["name"]
		if len(files) == 0 {
			s.writeError(w, "No files uploaded", http.StatusBadRequest)
			return
		}

		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				errors = append(errors, fmt.Sprintf("Failed to open %s: %v", fileHeader.Filename, err))
				continue
			}

			// Read file content
			content, err := io.ReadAll(file)
			file.Close()
			if err != nil {
				errors = append(errors, fmt.Sprintf("Failed to read %s: %v", fileHeader.Filename, err))
				continue
			}

			// Parse NZB file
			nzbID, err := s.addNZBFile(ctx, content, fileHeader.Filename, _arr, action)
			if err != nil {
				s.logger.Error().Err(err).Str("filename", fileHeader.Filename).Msg("Failed to add NZB file")
				errors = append(errors, fmt.Sprintf("Failed to add %s: %v", fileHeader.Filename, err))
				continue
			}
			if nzbID != "" {
				nzoIDs = append(nzoIDs, nzbID)
			}
		}
	} else {
		// Fallback to single file handling
		file, header, err := r.FormFile("name")
		if err != nil {
			s.writeError(w, "No file uploaded", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Read file content
		content, err := io.ReadAll(file)
		if err != nil {
			s.writeError(w, "Failed to read file", http.StatusInternalServerError)
			return
		}

		// Parse NZB file
		nzbID, err := s.addNZBFile(ctx, content, header.Filename, _arr, action)
		if err != nil {
			s.writeError(w, fmt.Sprintf("Failed to add NZB file: %s", err.Error()), http.StatusInternalServerError)
			return
		}
		if nzbID != "" {
			nzoIDs = append(nzoIDs, nzbID)
		}
	}

	if len(nzoIDs) == 0 {
		errMsg := "Failed to add any NZB files"
		if len(errors) > 0 {
			errMsg = strings.Join(errors, "; ")
		}
		s.writeError(w, errMsg, http.StatusInternalServerError)
		return
	}

	response := AddNZBResponse{
		Status: true,
		NzoIds: nzoIDs,
	}

	// Include partial errors if some files failed
	if len(errors) > 0 {
		response.Error = fmt.Sprintf("Partial success: %s", strings.Join(errors, "; "))
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

// handleVersion returns version information
func (s *SABnzbd) handleVersion(w http.ResponseWriter, r *http.Request) {
	response := VersionResponse{
		Version: Version,
	}
	utils.JSONResponse(w, response, http.StatusOK)
}

// handleGetCategories returns available categories
func (s *SABnzbd) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	categories := s.getCategories()
	utils.JSONResponse(w, categories, http.StatusOK)
}

// handleGetScripts returns available scripts
func (s *SABnzbd) handleGetScripts(w http.ResponseWriter, r *http.Request) {
	scripts := []string{"None"}
	utils.JSONResponse(w, scripts, http.StatusOK)
}

// handleGetFiles returns files for a specific NZB
func (s *SABnzbd) handleGetFiles(w http.ResponseWriter, r *http.Request) {
	nzoID := r.URL.Query().Get("value")
	if nzoID == "" {
		s.writeError(w, "NZB ID is required", http.StatusBadRequest)
		return
	}

	entry, err := s.manager.GetEntry(nzoID)
	if err != nil {
		s.writeError(w, "NZB not found", http.StatusNotFound)
		return
	}

	files := getNZBFiles(entry)

	// SABnzbd returns files wrapped in a "files" object
	response := map[string]any{
		"files": files,
	}
	utils.JSONResponse(w, response, http.StatusOK)
}

func (s *SABnzbd) handleStatus(w http.ResponseWriter, r *http.Request) {
	type status struct {
		CompletedDir string `json:"completed_dir"`
	}
	response := struct {
		Status status `json:"status"`
	}{
		Status: status{
			CompletedDir: s.config.Misc.DownloadDir,
		},
	}
	utils.JSONResponse(w, response, http.StatusOK)
}

// Helper methods

func (s *SABnzbd) getHistory(ctx context.Context, limit int, nzoIDs []string) History {
	cat := getCategory(ctx)
	completed := s.manager.Queue().ListFilter(cat, config.ProtocolNZB, storage.EntryStatePausedUP, nzoIDs, "added_on", false)
	failed := s.manager.Queue().ListFilter(cat, config.ProtocolNZB, storage.EntryStateError, nzoIDs, "added_on", false)
	slots := make([]HistorySlot, 0, len(completed)+len(failed))
	history := History{
		Version: Version,
		Paused:  false,
	}
	for _, item := range completed {
		slot := HistorySlot{
			Status:      mapStorageStateToSABStatus(item.State),
			Name:        item.Name,
			NZBName:     item.Name,
			NzoId:       item.InfoHash,
			Category:    item.Category,
			FailMessage: item.LastError,
			Bytes:       item.Size,
			Storage:     item.DownloadPath(),
		}
		slots = append(slots, slot)
	}

	for _, item := range failed {
		slot := HistorySlot{
			Status:      mapStorageStateToSABStatus(item.State),
			Name:        item.Name,
			NZBName:     item.Name,
			NzoId:       item.InfoHash,
			Category:    item.Category,
			FailMessage: item.LastError,
			Bytes:       item.Size,
			Storage:     item.DownloadPath(),
		}
		slots = append(slots, slot)
	}
	history.Slots = slots
	return history
}

func (s *SABnzbd) writeError(w http.ResponseWriter, message string, status int) {
	response := StatusResponse{
		Status: false,
		Error:  message,
	}
	utils.JSONResponse(w, response, status)
}

func (s *SABnzbd) addNZBURL(ctx context.Context, url string, arr *arr.Arr, action config.DownloadAction) (string, error) {
	if url == "" {
		return "", fmt.Errorf("URL is required")
	}
	// Download NZB content
	filename, content, err := utils.DownloadFile(url)
	if err != nil {
		s.logger.Error().Err(err).Str("url", url).Msg("Failed to download NZB from URL")
		return "", fmt.Errorf("failed to download NZB from URL: %w", err)
	}

	if len(content) == 0 {
		s.logger.Warn().Str("url", url).Msg("Downloaded content is empty")
		return "", fmt.Errorf("downloaded content is empty")
	}
	return s.addNZBFile(ctx, content, filename, arr, action)
}

func (s *SABnzbd) addNZBFile(ctx context.Context, content []byte, filename string, arr *arr.Arr, action config.DownloadAction) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("NZB content is empty")
	}

	cfg := config.Get()

	importReq := manager.NewNZBRequest(filename, s.downloadFolder, content, arr, action, cfg.Notifications.CallbackURL, manager.ImportTypeSABnzbd, cfg.SkipMultiSeason)
	id, err := s.manager.AddNewNZB(ctx, importReq)
	if err != nil {
		return "", err
	}
	return id, nil
}

// formatSize formats bytes to human-readable string (SABnzbd format)
func formatSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
		TB = GB * 1024
	)

	switch {
	case bytes >= TB:
		return fmt.Sprintf("%.2f T", float64(bytes)/float64(TB))
	case bytes >= GB:
		return fmt.Sprintf("%.2f G", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f M", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f K", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
