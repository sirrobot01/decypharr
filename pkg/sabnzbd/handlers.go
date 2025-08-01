package sabnzbd

import (
	"context"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
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
	name := r.FormValue("name")
	if name == "" {
		s.handleListQueue(w, r)
		return
	}
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "delete":
		s.handleQueueDelete(w, r)
	case "pause":
		s.handleQueuePause(w, r)
	case "resume":
		s.handleQueueResume(w, r)
	}
}

// handleResume handles resume operations
func (s *SABnzbd) handleQueueResume(w http.ResponseWriter, r *http.Request) {
	response := StatusResponse{Status: true}
	request.JSONResponse(w, response, http.StatusOK)
}

// handleDelete handles delete operations
func (s *SABnzbd) handleQueueDelete(w http.ResponseWriter, r *http.Request) {
	nzoIDs := r.FormValue("value")
	if nzoIDs == "" {
		s.writeError(w, "No NZB IDs provided", http.StatusBadRequest)
		return
	}

	var successCount int
	var errors []string

	for _, nzoID := range strings.Split(nzoIDs, ",") {
		nzoID = strings.TrimSpace(nzoID)
		if nzoID == "" {
			continue // Skip empty IDs
		}

		s.logger.Info().Str("nzo_id", nzoID).Msg("Deleting NZB")

		// Use atomic delete operation
		if err := s.usenet.Store().AtomicDelete(nzoID); err != nil {
			s.logger.Error().
				Err(err).
				Str("nzo_id", nzoID).
				Msg("Failed to delete NZB")
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
		} else {
			// Partial success
			s.logger.Warn().
				Int("success_count", successCount).
				Int("error_count", len(errors)).
				Strs("errors", errors).
				Msg("Partial success in queue deletion")
		}
	}

	response := StatusResponse{
		Status: true,
		Error:  "", // Could add error details here if needed
	}
	request.JSONResponse(w, response, http.StatusOK)
}

// handlePause handles pause operations
func (s *SABnzbd) handleQueuePause(w http.ResponseWriter, r *http.Request) {
	response := StatusResponse{Status: true}
	request.JSONResponse(w, response, http.StatusOK)
}

// handleQueue returns the current download queue
func (s *SABnzbd) handleListQueue(w http.ResponseWriter, r *http.Request) {
	nzbs := s.usenet.Store().GetQueue()

	queue := Queue{
		Version: Version,
		Slots:   []QueueSlot{},
	}

	// Convert NZBs to queue slots
	for _, nzb := range nzbs {
		if nzb.ETA <= 0 {
			nzb.ETA = 0 // Ensure ETA is non-negative
		}
		var timeLeft string
		if nzb.ETA == 0 {
			timeLeft = "00:00:00" // If ETA is 0, set TimeLeft to "00:00:00"
		} else {
			// Convert ETA from seconds to "HH:MM:SS" format
			duration := time.Duration(nzb.ETA) * time.Second
			timeLeft = duration.String()
		}
		slot := QueueSlot{
			Status:     s.mapNZBStatus(nzb.Status),
			Mb:         nzb.TotalSize,
			Filename:   nzb.Name,
			Cat:        nzb.Category,
			MBLeft:     0,
			Percentage: nzb.Percentage,
			NzoId:      nzb.ID,
			Size:       nzb.TotalSize,
			TimeLeft:   timeLeft, // This is in "00:00:00" format
		}
		queue.Slots = append(queue.Slots, slot)
	}

	response := QueueResponse{
		Queue:   queue,
		Status:  true,
		Version: Version,
	}

	request.JSONResponse(w, response, http.StatusOK)
}

// handleHistory returns the download history
func (s *SABnzbd) handleHistory(w http.ResponseWriter, r *http.Request) {
	limitStr := r.FormValue("limit")
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
	history := s.getHistory(r.Context(), limit)

	response := HistoryResponse{
		History: history,
	}

	request.JSONResponse(w, response, http.StatusOK)
}

// handleConfig returns the configuration
func (s *SABnzbd) handleConfig(w http.ResponseWriter, r *http.Request) {

	response := ConfigResponse{
		Config: s.config,
	}

	request.JSONResponse(w, response, http.StatusOK)
}

// handleAddURL handles adding NZB by URL
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

	url := r.FormValue("name")
	action := r.FormValue("action")
	downloadDir := r.FormValue("download_dir")
	if action == "" {
		action = "symlink"
	}
	if downloadDir == "" {
		downloadDir = s.config.Misc.DownloadDir
	}

	if url == "" {
		s.writeError(w, "URL is required", http.StatusBadRequest)
		return
	}

	nzoID, err := s.addNZBURL(ctx, url, _arr, action, downloadDir)
	if err != nil {
		s.writeError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if nzoID == "" {
		s.writeError(w, "Failed to add NZB", http.StatusInternalServerError)
		return
	}

	response := AddNZBResponse{
		Status: true,
		NzoIds: []string{nzoID},
	}

	request.JSONResponse(w, response, http.StatusOK)
}

// handleAddFile handles NZB file uploads
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

	// Parse multipart form
	err := r.ParseMultipartForm(32 << 20) // 32 MB limit
	if err != nil {
		s.writeError(w, "Failed to parse multipart form", http.StatusBadRequest)
		return
	}

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
	action := r.FormValue("action")
	downloadDir := r.FormValue("download_dir")
	if action == "" {
		action = "symlink"
	}
	if downloadDir == "" {
		downloadDir = s.config.Misc.DownloadDir
	}

	// Process NZB file
	nzbID, err := s.addNZBFile(ctx, content, header.Filename, _arr, action, downloadDir)
	if err != nil {
		s.writeError(w, fmt.Sprintf("Failed to add NZB file: %s", err.Error()), http.StatusInternalServerError)
		return
	}
	if nzbID == "" {
		s.writeError(w, "Failed to add NZB file", http.StatusInternalServerError)
		return
	}

	response := AddNZBResponse{
		Status: true,
		NzoIds: []string{nzbID},
	}

	request.JSONResponse(w, response, http.StatusOK)
}

// handleVersion returns version information
func (s *SABnzbd) handleVersion(w http.ResponseWriter, r *http.Request) {
	response := VersionResponse{
		Version: Version,
	}
	request.JSONResponse(w, response, http.StatusOK)
}

// handleGetCategories returns available categories
func (s *SABnzbd) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	categories := s.getCategories()
	request.JSONResponse(w, categories, http.StatusOK)
}

// handleGetScripts returns available scripts
func (s *SABnzbd) handleGetScripts(w http.ResponseWriter, r *http.Request) {
	scripts := []string{"None"}
	request.JSONResponse(w, scripts, http.StatusOK)
}

// handleGetFiles returns files for a specific NZB
func (s *SABnzbd) handleGetFiles(w http.ResponseWriter, r *http.Request) {
	nzoID := r.FormValue("value")
	var files []string

	if nzoID != "" {
		nzb := s.usenet.Store().Get(nzoID)
		if nzb != nil {
			for _, file := range nzb.Files {
				files = append(files, file.Name)
			}
		}
	}

	request.JSONResponse(w, files, http.StatusOK)
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
	request.JSONResponse(w, response, http.StatusOK)
}

// Helper methods

func (s *SABnzbd) getHistory(ctx context.Context, limit int) History {
	cat := getCategory(ctx)
	items := s.usenet.Store().GetHistory(cat, limit)
	slots := make([]HistorySlot, 0, len(items))
	history := History{
		Version: Version,
		Paused:  false,
	}
	for _, item := range items {
		slot := HistorySlot{
			Status:      s.mapNZBStatus(item.Status),
			Name:        item.Name,
			NZBName:     item.Name,
			NzoId:       item.ID,
			Category:    item.Category,
			FailMessage: item.FailMessage,
			Bytes:       item.TotalSize,
			Storage:     item.Storage,
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
	request.JSONResponse(w, response, status)
}

func (s *SABnzbd) mapNZBStatus(status string) string {
	switch status {
	case "downloading":
		return StatusDownloading
	case "completed":
		return StatusCompleted
	case "paused":
		return StatusPaused
	case "error", "failed":
		return StatusFailed
	case "processing":
		return StatusProcessing
	case "verifying":
		return StatusVerifying
	case "repairing":
		return StatusRepairing
	case "extracting":
		return StatusExtracting
	case "moving":
		return StatusMoving
	case "running":
		return StatusRunning
	default:
		return StatusQueued
	}
}

func (s *SABnzbd) addNZBURL(ctx context.Context, url string, arr *arr.Arr, action, downloadDir string) (string, error) {
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
	return s.addNZBFile(ctx, content, filename, arr, action, downloadDir)
}

func (s *SABnzbd) addNZBFile(ctx context.Context, content []byte, filename string, arr *arr.Arr, action, downloadDir string) (string, error) {
	if s.usenet == nil {
		return "", fmt.Errorf("store not initialized")
	}
	req := &usenet.ProcessRequest{
		NZBContent:  content,
		Name:        filename,
		Arr:         arr,
		Action:      action,
		DownloadDir: downloadDir,
	}
	nzb, err := s.usenet.ProcessNZB(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to process NZB: %w", err)
	}
	return nzb.ID, nil
}
