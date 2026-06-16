package server

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"sort"
	"strconv"
	"strings"

	json "github.com/bytedance/sonic"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/version"
	"github.com/sourcegraph/conc/iter"
	"golang.org/x/crypto/bcrypt"
)

type mountCacheCleaner interface {
	CleanupCache() (map[string]interface{}, error)
}

type mountCachePurger interface {
	PurgeCache() (map[string]interface{}, error)
}

func (s *Server) handleGetArrs(w http.ResponseWriter, r *http.Request) {
	utils.JSONResponse(w, s.manager.Arr().GetAll(), http.StatusOK)
}

func (s *Server) handleAddContent(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	arrName := r.FormValue("arr")
	action := r.FormValue("action")
	debridName := r.FormValue("debrid")
	callbackUrl := r.FormValue("callbackUrl")
	downloadFolder := r.FormValue("downloadFolder")
	if downloadFolder == "" {
		downloadFolder = config.Get().DownloadFolder
	}
	skipMultiSeason := r.FormValue("skipMultiSeason") == "true"

	dlUncached := r.FormValue("downloadUncached") == "true"
	var downloadUncached *bool
	if dlUncached {
		downloadUncached = &dlUncached
	}
	rmTrackerUrls := r.FormValue("rmTrackerUrls") == "true"

	// Check config setting - if always remove tracker URLs is enabled, force it to true
	cfg := config.Get()
	if cfg.AlwaysRmTrackerUrls {
		rmTrackerUrls = true
	}

	_arr := s.manager.Arr().Get(arrName)
	if _arr == nil {
		// These are not found in the config. They are throwaway arrs.
		_arr = arr.New(arrName, "", "", false, false, downloadUncached, "", "")
	}

	// Unified task type for all content types
	type addTask struct {
		taskType   string // "torrent", "nzbURL", "nzbFile"
		magnet     *utils.Magnet
		nzbContent []byte
		name       string
		source     string // for error messages
	}

	var tasks []addTask

	// Collect torrent URLs
	if urls := r.FormValue("urls"); urls != "" {
		for _, u := range strings.Split(urls, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				magnet, err := utils.GetMagnetFromUrl(trimmed, rmTrackerUrls)
				if err != nil {
					tasks = append(tasks, addTask{
						taskType: "error",
						source:   fmt.Sprintf("Failed to parse URL %s: %v", trimmed, err),
					})
					continue
				}
				tasks = append(tasks, addTask{taskType: "torrent", magnet: magnet, source: fmt.Sprintf("URL %s", trimmed)})
			}
		}
	}

	// Collect torrent files
	if files := r.MultipartForm.File["files"]; len(files) > 0 {
		for _, fileHeader := range files {
			file, err := fileHeader.Open()
			if err != nil {
				tasks = append(tasks, addTask{
					taskType: "error",
					source:   fmt.Sprintf("Failed to open file %s: %v", fileHeader.Filename, err),
				})
				continue
			}

			magnet, err := utils.GetMagnetFromFile(file, fileHeader.Filename, rmTrackerUrls)
			if err != nil {
				tasks = append(tasks, addTask{
					taskType: "error",
					source:   fmt.Sprintf("Failed to parse torrent file %s: %v", fileHeader.Filename, err),
				})
				continue
			}
			tasks = append(tasks, addTask{taskType: "torrent", magnet: magnet, source: fmt.Sprintf("File %s", fileHeader.Filename), name: fileHeader.Filename})
		}
	}

	// Collect NZB URLs
	if nzbURLs := r.FormValue("nzbURLs"); nzbURLs != "" {
		for _, u := range strings.Split(nzbURLs, "\n") {
			if trimmed := strings.TrimSpace(u); trimmed != "" {
				filename, content, err := utils.DownloadFile(trimmed, utils.WithHeader("User-Agent", s.nzbUserAgent))
				if err != nil {
					tasks = append(tasks, addTask{
						taskType: "error",
						source:   fmt.Sprintf("Failed to fetch NZB from URL %s: %v", trimmed, err),
					})
					continue
				}
				tasks = append(tasks, addTask{taskType: "nzb", nzbContent: content, name: filename, source: fmt.Sprintf("NZB URL %s", trimmed)})
			}
		}
	}

	// Collect NZB files
	if nzbFiles := r.MultipartForm.File["nzbFiles"]; len(nzbFiles) > 0 {
		for _, fileHeader := range nzbFiles {
			content, err := getNZBContentFromFile(fileHeader)
			if err != nil {
				tasks = append(tasks, addTask{
					taskType: "error",
					source:   fmt.Sprintf("Failed to read NZB file %s: %v", fileHeader.Filename, err),
				})
				continue
			}
			tasks = append(tasks, addTask{taskType: "nzb", nzbContent: content, source: fmt.Sprintf("NZB File %s", fileHeader.Filename), name: fileHeader.Filename})
		}
	}

	// Parse all tasks in parallel using iter.Map
	mapper := iter.Mapper[addTask, *manager.ImportRequest]{
		MaxGoroutines: min(len(tasks), 10),
	}

	results := mapper.Map(tasks, func(task *addTask) *manager.ImportRequest {
		switch task.taskType {
		case "error":
			// Task already failed during collection phase
			return &manager.ImportRequest{
				Status: "error",
				Error:  fmt.Sprintf("Failed to import torrent %s: %v", task.name, task.magnet),
			}

		case "torrent":
			importReq := manager.NewTorrentRequest(debridName, downloadFolder, task.magnet, _arr, config.DownloadAction(action), downloadUncached, callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
			if err := s.manager.AddNewTorrent(ctx, importReq); err != nil {
				s.logger.Error().Err(err).Str("source", task.source).Msg("Failed to add torrent")
				importReq.Error = err.Error()
				importReq.Status = "error"
			}
			return importReq

		case "nzb":
			importReq := manager.NewNZBRequest(task.name, downloadFolder, task.nzbContent, _arr, config.DownloadAction(action), callbackUrl, manager.ImportTypeAPI, skipMultiSeason)
			nzoID, err := s.manager.AddNewNZB(ctx, importReq)
			if err != nil {
				s.logger.Error().Err(err).Str("source", task.source).Msg("Failed to add NZB")
				importReq.Error = err.Error()
				importReq.Status = "error"
			}
			importReq.Id = nzoID
			return importReq

		default:
			return nil
		}
	})

	// Filter out nil results
	filtered := make([]*manager.ImportRequest, 0, len(results))
	for _, r := range results {
		if r != nil {
			filtered = append(filtered, r)
		}
	}

	utils.JSONResponse(w, filtered, http.StatusOK)
}

func getNZBContentFromFile(fileHeader *multipart.FileHeader) ([]byte, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read NZB content
	nzbContent, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	return nzbContent, nil
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request) {
	v := version.GetInfo()
	utils.JSONResponse(w, v, http.StatusOK)
}

func (s *Server) handleRunMountCacheCleanup(w http.ResponseWriter, r *http.Request) {
	mountMgr := s.manager.MountManager()
	if mountMgr == nil || !mountMgr.IsReady() {
		http.Error(w, "Mount is not ready", http.StatusServiceUnavailable)
		return
	}

	cleaner, ok := mountMgr.(mountCacheCleaner)
	if !ok {
		http.Error(w, "Manual cache cleanup is only available for DFS mounts", http.StatusBadRequest)
		return
	}

	cleanupStats, err := cleaner.CleanupCache()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to run mount cache cleanup")
		http.Error(w, "Failed to run mount cache cleanup", http.StatusInternalServerError)
		return
	}

	if s.stats != nil {
		s.stats.Refresh()
	}

	utils.JSONResponse(w, map[string]interface{}{
		"status": "success",
		"cache":  cleanupStats,
	}, http.StatusOK)
}

func (s *Server) handlePurgeMountCache(w http.ResponseWriter, r *http.Request) {
	mountMgr := s.manager.MountManager()
	if mountMgr == nil || !mountMgr.IsReady() {
		http.Error(w, "Mount is not ready", http.StatusServiceUnavailable)
		return
	}

	purger, ok := mountMgr.(mountCachePurger)
	if !ok {
		http.Error(w, "Cache purge is only available for DFS mounts", http.StatusBadRequest)
		return
	}

	purgeStats, err := purger.PurgeCache()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to purge mount cache")
		http.Error(w, "Failed to purge mount cache", http.StatusInternalServerError)
		return
	}

	if s.stats != nil {
		s.stats.Refresh()
	}

	utils.JSONResponse(w, map[string]interface{}{
		"status": "success",
		"cache":  purgeStats,
	}, http.StatusOK)
}

func (s *Server) handleGetTorrents(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters for server-side filtering, sorting, and pagination
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 20
	}

	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	category := strings.TrimSpace(r.URL.Query().Get("category"))
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	sortBy := strings.TrimSpace(r.URL.Query().Get("sort_by"))
	sortOrder := strings.TrimSpace(r.URL.Query().Get("sort_order"))

	if sortBy == "" {
		sortBy = "added_on"
	}
	if sortOrder == "" {
		sortOrder = "desc"
	}

	// GetReader all torrents
	allTorrents := s.manager.Queue().ListFilter("", config.ProtocolAll, "", nil, "added_on", false)
	for _, t := range allTorrents {
		t.Sanitize()
	}

	// Apply filters
	filteredTorrents := make([]*storage.Entry, 0)
	for _, t := range allTorrents {
		// Search filter - search in name and hash
		if search != "" {
			searchIn := strings.ToLower(t.Name + " " + t.InfoHash)
			if !strings.Contains(searchIn, search) {
				continue
			}
		}

		// Category filter
		if category != "" && t.Category != category {
			continue
		}

		// State filter
		if state != "" && t.State != storage.TorrentState(state) {
			continue
		}

		filteredTorrents = append(filteredTorrents, t)
	}

	// Apply sorting
	sortQueuedTorrents(filteredTorrents, sortBy, sortOrder)

	// Calculate pagination
	total := len(filteredTorrents)
	totalPages := (total + limit - 1) / limit
	offset := (page - 1) * limit

	// Apply pagination
	var paginatedTorrents []*storage.Entry
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedTorrents = filteredTorrents[offset:end]
	} else {
		paginatedTorrents = []*storage.Entry{}
	}

	// GetReader unique categories
	categorySet := make(map[string]bool)
	for _, t := range allTorrents {
		if t.Category != "" {
			categorySet[t.Category] = true
		}
	}

	categories := make([]string, 0, len(categorySet))
	for c := range categorySet {
		categories = append(categories, c)
	}

	utils.JSONResponse(w, map[string]interface{}{
		"torrents":    paginatedTorrents,
		"total":       total,
		"page":        page,
		"limit":       limit,
		"total_pages": totalPages,
		"has_prev":    page > 1,
		"has_next":    page < totalPages,
		"categories":  categories,
	}, http.StatusOK)
}

// sortQueuedTorrents sorts torrents based on the given field and order
func sortQueuedTorrents(torrents []*storage.Entry, sortBy, sortOrder string) {
	if len(torrents) == 0 {
		return
	}

	less := func(i, j int) bool {
		var result bool
		switch sortBy {
		case "name":
			result = strings.ToLower(torrents[i].Name) < strings.ToLower(torrents[j].Name)
		case "size":
			result = torrents[i].Size < torrents[j].Size
		case "added_on":
			result = torrents[i].AddedOn.Before(torrents[j].AddedOn)
		case "progress":
			result = torrents[i].Progress < torrents[j].Progress
		case "category":
			result = strings.ToLower(torrents[i].Category) < strings.ToLower(torrents[j].Category)
		case "state":
			result = torrents[i].State < torrents[j].State
		default:
			result = torrents[i].AddedOn.Before(torrents[j].AddedOn)
		}

		if sortOrder == "desc" {
			return !result
		}
		return result
	}

	sort.Slice(torrents, less)
}

func (s *Server) handleDeleteTorrent(w http.ResponseWriter, r *http.Request) {
	hash := chi.URLParam(r, "hash")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hash == "" {
		http.Error(w, "No hash provided", http.StatusBadRequest)
		return
	}
	var cleanup func(torrent *storage.Entry) error

	if removeFromDebrid {
		cleanup = func(t *storage.Entry) error {
			exists, _ := s.manager.EntryExists(t.InfoHash)
			if exists {
				// Remove the entry from manager fully, which will handle removing from debrid and deleting the entry
				return s.manager.DeleteEntry(t.InfoHash, true)
			}
			go s.manager.RemoveTorrentPlacements(t)
			return nil
		}
	}

	if err := s.manager.Queue().Delete(hash, cleanup); err != nil {
		s.logger.Error().Err(err).Str("hash", hash).Msg("Failed to delete entry from queue")
		http.Error(w, "Failed to delete entry from queue", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteTorrents(w http.ResponseWriter, r *http.Request) {
	hashesStr := r.URL.Query().Get("hashes")
	removeFromDebrid := r.URL.Query().Get("removeFromDebrid") == "true"
	if hashesStr == "" {
		http.Error(w, "No hashes provided", http.StatusBadRequest)
		return
	}
	hashes := strings.Split(hashesStr, ",")
	var cleanup func(torrent *storage.Entry) error
	if removeFromDebrid {
		cleanup = func(t *storage.Entry) error {
			exists, _ := s.manager.EntryExists(t.InfoHash)
			if exists {
				// Remove the entry from manager fully, which will handle removing from debrid and deleting the entry
				return s.manager.DeleteEntry(t.InfoHash, true)
			}
			go s.manager.RemoveTorrentPlacements(t)
			return nil
		}
	}
	if err := s.manager.Queue().DeleteWhere("", config.ProtocolAll, "", hashes, cleanup); err != nil {
		s.logger.Error().Err(err).Msg("Failed to delete torrents")
		http.Error(w, "Failed to delete torrents", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	arrStorage := s.manager.Arr()
	cfg := config.Get()
	cfg.Arrs = arrStorage.SyncToConfig()

	// Create response with API token info
	type ConfigResponse struct {
		*config.Config
		APIToken     string `json:"api_token,omitempty"`
		AuthUsername string `json:"auth_username,omitempty"`
	}

	response := &ConfigResponse{Config: cfg}

	// AddOrUpdate API token and auth information
	auth := cfg.GetAuth()
	if auth != nil {
		if auth.APIToken != "" {
			response.APIToken = auth.APIToken
		}
		response.AuthUsername = auth.Username
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

func (s *Server) handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	// Decode the incoming config update
	var newConfig config.Config
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&newConfig); err != nil {
		s.logger.Error().Err(err).Msg("Failed to decode config update request")
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Basic validation
	if newConfig.BindAddress == "" {
		newConfig.BindAddress = "0.0.0.0"
	}
	if newConfig.Port == "" {
		newConfig.Port = "8282"
	}

	// Preserve fields that shouldn't be overwritten by frontend
	currentConfig := config.Get()
	newConfig.Auth = currentConfig.GetAuth()
	// The frontend config form doesn't include use_auth or enable_webdav_auth,
	// so they would be zero-valued (false) in the decoded payload. Preserve
	// them from the live config so auth isn't silently disabled on every save.
	newConfig.UseAuth = currentConfig.UseAuth
	newConfig.EnableWebdavAuth = currentConfig.EnableWebdavAuth

	// Filter out empty or incomplete arrs
	validArrs := make([]config.Arr, 0, len(newConfig.Arrs))
	for _, a := range newConfig.Arrs {
		if a.Name != "" && a.Host != "" && a.Token != "" {
			validArrs = append(validArrs, a)
		}
	}
	newConfig.Arrs = validArrs

	// Sync arr storage with the new configuration
	s.manager.Arr().SyncFromConfig(newConfig.Arrs)

	// Save the updated config. This also applies defaults to newConfig, so the
	// restart comparison below sees a fully-normalized config on both sides.
	if err := newConfig.Save(); err != nil {
		s.logger.Error().Err(err).Msg("Failed to save config")
		http.Error(w, "Error saving config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Only restart when a field that needs it actually changed (HTTP bind,
	// debrid/usenet clients, or the mount). For everything else, apply the new
	// config live so users aren't disrupted by a full restart on every save.
	restarted := config.Get().RequiresRestart(&newConfig)
	if restarted {
		go s.Restart()
	} else {
		config.Get().ApplyRuntime(&newConfig)
		// Reschedule/reapply the repair sweep if its settings changed.
		if svc := s.manager.Repair(); svc != nil {
			if err := svc.ApplyConfig(); err != nil {
				s.logger.Warn().Err(err).Msg("Failed to apply repair config after live update")
			}
		}
	}

	utils.JSONResponse(w, map[string]any{"status": "success", "restarted": restarted}, http.StatusOK)
}

func (s *Server) handleGetRepairConfig(w http.ResponseWriter, r *http.Request) {
	utils.JSONResponse(w, config.Get().Repair, http.StatusOK)
}

func (s *Server) handleUpdateRepairConfig(w http.ResponseWriter, r *http.Request) {
	var req config.RepairConfig
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.Enabled {
		if strings.TrimSpace(req.Schedule) == "" {
			http.Error(w, "Schedule is required when repair is enabled", http.StatusBadRequest)
			return
		}
		if _, err := utils.ConvertToJobDef(req.Schedule); err != nil {
			http.Error(w, fmt.Sprintf("Invalid schedule: %v", err), http.StatusBadRequest)
			return
		}
		if req.RecheckInterval != "" {
			if _, err := utils.ParseDuration(req.RecheckInterval); err != nil {
				http.Error(w, fmt.Sprintf("Invalid recheck_interval: %v", err), http.StatusBadRequest)
				return
			}
		}
		if req.Source != "" && req.Source != config.RepairSourceArr && req.Source != config.RepairSourceManaged {
			http.Error(w, "Invalid source (must be 'arr' or 'managed')", http.StatusBadRequest)
			return
		}
	}
	if req.NNTPConnectionPercent < 0 || req.NNTPConnectionPercent > 100 {
		http.Error(w, "Invalid nntp_connection_percent (must be between 0 and 100)", http.StatusBadRequest)
		return
	}

	cfg := config.Get()
	cfg.Repair = req
	if err := cfg.Save(); err != nil {
		s.logger.Error().Err(err).Msg("Failed to save repair config")
		http.Error(w, "Failed to save config: "+err.Error(), http.StatusInternalServerError)
		return
	}

	if svc := s.manager.Repair(); svc != nil {
		if err := svc.ApplyConfig(); err != nil {
			s.logger.Warn().Err(err).Msg("Failed to apply repair config")
			http.Error(w, "Saved, but failed to apply: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	utils.JSONResponse(w, cfg.Repair, http.StatusOK)
}

func (s *Server) handleRepairStatus(w http.ResponseWriter, r *http.Request) {
	svc := s.manager.Repair()
	if svc == nil {
		utils.JSONResponse(w, manager.RepairStatus{}, http.StatusOK)
		return
	}
	utils.JSONResponse(w, svc.Status(), http.StatusOK)
}

func (s *Server) handleRunRepair(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IgnoreLastChecked bool   `json:"ignore_last_checked,omitempty"`
		Force             bool   `json:"force,omitempty"`
		AutoRepair        *bool  `json:"auto_repair,omitempty"`
		UnrestrictLink    bool   `json:"unrestrict_link,omitempty"`
		Protocol          string `json:"protocol,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	ignoreLastChecked := req.IgnoreLastChecked || req.Force
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("ignore_last_checked"))) {
	case "1", "true", "yes", "on":
		ignoreLastChecked = true
	}
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("force"))) {
	case "1", "true", "yes", "on":
		ignoreLastChecked = true
	}
	autoRepair := req.AutoRepair
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("auto_repair"))) {
	case "1", "true", "yes", "on":
		v := true
		autoRepair = &v
	case "0", "false", "no", "off":
		v := false
		autoRepair = &v
	}
	unrestrictLink := req.UnrestrictLink
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("unrestrict_link"))) {
	case "1", "true", "yes", "on":
		unrestrictLink = true
	case "0", "false", "no", "off":
		unrestrictLink = false
	}
	protocolScope := strings.ToLower(strings.TrimSpace(req.Protocol))
	if queryProtocol := strings.TrimSpace(r.URL.Query().Get("protocol")); queryProtocol != "" {
		protocolScope = strings.ToLower(queryProtocol)
	}
	switch protocolScope {
	case "", "all", "both", "torrent", "nzb":
		if protocolScope == "both" {
			protocolScope = "all"
		}
	default:
		http.Error(w, "Invalid protocol; expected all, torrent, or nzb", http.StatusBadRequest)
		return
	}

	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	id, err := svc.RunNow(manager.RepairRunOptions{
		IgnoreLastChecked: ignoreLastChecked,
		AutoRepair:        autoRepair,
		UnrestrictLink:    unrestrictLink,
		ProtocolScope:     protocolScope,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	utils.JSONResponse(w, map[string]string{"run_id": id}, http.StatusOK)
}

func (s *Server) handleStopRepair(w http.ResponseWriter, r *http.Request) {
	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	if err := svc.StopRun(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleListRepairRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.manager.Storage().ListRepairRuns()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	utils.JSONResponse(w, runs, http.StatusOK)
}

func (s *Server) handleGetRepairRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "No run ID provided", http.StatusBadRequest)
		return
	}
	run, err := s.manager.Storage().GetRepairRun(id)
	if err != nil {
		http.Error(w, "Run not found", http.StatusNotFound)
		return
	}
	utils.JSONResponse(w, run, http.StatusOK)
}

func (s *Server) handleClearRepairRuns(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Storage().ClearRepairRuns(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleListEntryHealth(w http.ResponseWriter, r *http.Request) {
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	out := make([]*storage.EntryHealth, 0)
	_ = s.manager.Storage().ForEachEntryHealth(func(state *storage.EntryHealth) error {
		if statusFilter != "" && string(state.Status) != statusFilter {
			return nil
		}
		out = append(out, state)
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].EntryName < out[j].EntryName
	})
	utils.JSONResponse(w, out, http.StatusOK)
}

func (s *Server) handleGetEntryHealth(w http.ResponseWriter, r *http.Request) {
	name := utils.PathUnescape(chi.URLParam(r, "name"))
	if name == "" {
		http.Error(w, "No entry name provided", http.StatusBadRequest)
		return
	}
	state, err := s.manager.Storage().GetEntryHealth(name)
	if err != nil {
		http.Error(w, "Entry health not found", http.StatusNotFound)
		return
	}
	utils.JSONResponse(w, state, http.StatusOK)
}

func (s *Server) handleRecheckMedia(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arr     string `json:"arr"`
		MediaID string `json:"media_id"`
		Fix     bool   `json:"fix"`
	}
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.MediaID) == "" {
		http.Error(w, "media_id is required", http.StatusBadRequest)
		return
	}
	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	run, err := svc.RecheckMedia(s.manager.Context(), strings.TrimSpace(req.Arr), strings.TrimSpace(req.MediaID), req.Fix)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		}
		// Returning the run record (when present) gives the caller the
		// failure detail captured in storage as well as the message.
		if run != nil {
			utils.JSONResponse(w, map[string]interface{}{
				"error": err.Error(),
				"run":   run,
			}, status)
			return
		}
		http.Error(w, err.Error(), status)
		return
	}
	utils.JSONResponse(w, run, http.StatusOK)
}

func (s *Server) handleRecheckEntry(w http.ResponseWriter, r *http.Request) {
	name := utils.PathUnescape(chi.URLParam(r, "name"))
	if name == "" {
		http.Error(w, "No entry name provided", http.StatusBadRequest)
		return
	}
	fix := r.URL.Query().Get("fix") == "true"
	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	state, err := svc.RecheckEntry(s.manager.Context(), name, fix)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	utils.JSONResponse(w, state, http.StatusOK)
}

// handleFixBroken kicks off the Arr delete + re-search pass on currently
// broken entries. Body: {"names": ["...", ...]}. Empty/missing names ⇒ fix
// every broken entry in storage.
func (s *Server) handleFixBroken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names,omitempty"`
	}
	// Body is optional; ignore decode errors for empty / missing bodies.
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	run, err := svc.FixBroken(s.manager.Context(), req.Names)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	utils.JSONResponse(w, run, http.StatusOK)
}

// handleClearBroken clears currently broken files without asking the Arr to
// re-search for replacements. Body: {"names": ["...", ...]}. Empty/missing
// names ⇒ clear every broken entry in storage.
func (s *Server) handleClearBroken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Names []string `json:"names,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	run, err := svc.ClearBroken(s.manager.Context(), req.Names)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	utils.JSONResponse(w, run, http.StatusOK)
}

func (s *Server) handleClearRepairState(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Statuses []string `json:"statuses"`
	}
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	statuses := make([]storage.HealthStatus, 0, len(req.Statuses))
	for _, raw := range req.Statuses {
		status, ok := parseRepairHealthStatus(raw)
		if !ok {
			http.Error(w, "Invalid repair health status: "+raw, http.StatusBadRequest)
			return
		}
		statuses = append(statuses, status)
	}
	if len(statuses) == 0 {
		http.Error(w, "At least one status is required", http.StatusBadRequest)
		return
	}

	svc := s.manager.Repair()
	if svc == nil {
		http.Error(w, "Repair service not available", http.StatusServiceUnavailable)
		return
	}
	result, err := svc.ClearStates(statuses)
	if err != nil {
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already running") {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	utils.JSONResponse(w, result, http.StatusOK)
}

func parseRepairHealthStatus(raw string) (storage.HealthStatus, bool) {
	switch storage.HealthStatus(strings.ToLower(strings.TrimSpace(raw))) {
	case storage.HealthHealthy:
		return storage.HealthHealthy, true
	case storage.HealthBroken:
		return storage.HealthBroken, true
	case storage.HealthRepairing:
		return storage.HealthRepairing, true
	case storage.HealthStale:
		return storage.HealthStale, true
	case storage.HealthUnknown:
		return storage.HealthUnknown, true
	case storage.HealthUnsupported:
		return storage.HealthUnsupported, true
	default:
		return "", false
	}
}

func (s *Server) handleRefreshAPIToken(w http.ResponseWriter, _ *http.Request) {
	token, err := s.refreshAPIToken()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to refresh API token")
		http.Error(w, "Failed to refresh token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	utils.JSONResponse(w, map[string]interface{}{
		"token":   token,
		"message": "API token refreshed successfully",
	}, http.StatusOK)
}

func (s *Server) handleUpdateAuth(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username        string `json:"username"`
		Password        string `json:"password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
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
			s.logger.Error().Err(err).Msg("Failed to save auth config")
			http.Error(w, "Failed to save authentication settings", http.StatusInternalServerError)
			return
		}
		if err := cfg.Save(); err != nil {
			s.logger.Error().Err(err).Msg("Failed to save config")
			http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
			return
		}

		utils.JSONResponse(w, map[string]string{
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
		s.logger.Error().Err(err).Msg("Failed to hash password")
		http.Error(w, "Failed to process password", http.StatusInternalServerError)
		return
	}

	// Update auth settings
	auth.Username = req.Username
	auth.Password = string(hashedPassword)
	cfg.UseAuth = true

	// Save auth config
	if err := cfg.SaveAuth(auth); err != nil {
		s.logger.Error().Err(err).Msg("Failed to save auth config")
		http.Error(w, "Failed to save authentication settings", http.StatusInternalServerError)
		return
	}

	// Save main config
	if err := cfg.Save(); err != nil {
		s.logger.Error().Err(err).Msg("Failed to save config")
		http.Error(w, "Failed to save configuration", http.StatusInternalServerError)
		return
	}

	utils.JSONResponse(w, map[string]string{
		"message": "Authentication settings updated successfully",
	}, http.StatusOK)
}
