package server

import (
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	json "github.com/bytedance/sonic"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// BrowseEntry represents a file or folder in the browse view
type BrowseEntry struct {
	Infohash     string `json:"infohash,omitempty"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	Size         int64  `json:"size"`
	ModTime      string `json:"mod_time"`
	IsDir        bool   `json:"is_dir"`
	InfoHash     string `json:"info_hash,omitempty"`  // For torrent folders
	CanDelete    bool   `json:"can_delete,omitempty"` // Whether this can be deleted
	ActiveDebrid string `json:"active_debrid"`
}

// BrowseResponse is the response for browse requests
type BrowseResponse struct {
	Entries    []BrowseEntry `json:"entries"`
	Total      int           `json:"total"`
	Page       int           `json:"page"`
	Limit      int           `json:"limit"`
	TotalPages int           `json:"total_pages"`
	CurrentDir string        `json:"current_dir"`
	ParentDir  string        `json:"parent_dir,omitempty"`
}

func getBrowseSortParams(r *http.Request) (string, string) {
	sortBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort_by")))
	sortOrder := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("sort_order")))

	switch sortBy {
	case "name", "size", "mod_time", "active_debrid":
	default:
		sortBy = "name"
	}

	if sortOrder != "desc" {
		sortOrder = "asc"
	}

	return sortBy, sortOrder
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

func sortBrowseEntries(entries []BrowseEntry, sortBy, sortOrder string) {
	sort.SliceStable(entries, func(i, j int) bool {
		// Keep folders above files regardless of sort direction.
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir && !entries[j].IsDir
		}

		cmp := 0
		switch sortBy {
		case "size":
			cmp = compareInt64(entries[i].Size, entries[j].Size)
		case "mod_time":
			cmp = strings.Compare(entries[i].ModTime, entries[j].ModTime)
		case "active_debrid":
			cmp = strings.Compare(strings.ToLower(entries[i].ActiveDebrid), strings.ToLower(entries[j].ActiveDebrid))
		default:
			cmp = strings.Compare(strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name))
		}

		if cmp == 0 {
			cmp = strings.Compare(strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name))
		}
		if cmp == 0 {
			cmp = strings.Compare(entries[i].Path, entries[j].Path)
		}

		if sortOrder == "desc" {
			cmp = -cmp
		}
		return cmp < 0
	})
}

// handleBrowseMount returns subdirectories under a mount (__all__, __bad__, etc.)
func (s *Server) handleBrowseMount(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	sortBy, sortOrder := getBrowseSortParams(r)

	children := s.manager.GetEntries()

	// Convert to browse entries
	entries := make([]BrowseEntry, 0, len(children))
	for _, child := range children {
		entries = append(entries, BrowseEntry{
			Name:         child.Name(),
			Path:         "/" + child.Name(),
			Size:         child.Size(),
			ModTime:      child.ModTime().Format("2006-01-02 15:04:05"),
			IsDir:        child.IsDir(),
			ActiveDebrid: child.ActiveDebrid(),
		})
	}
	sortBrowseEntries(entries, sortBy, sortOrder)

	// Apply pagination
	total := len(entries)
	totalPages := (total + limit - 1) / limit
	offset := (page - 1) * limit

	var paginatedEntries []BrowseEntry
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedEntries = entries[offset:end]
	} else {
		paginatedEntries = []BrowseEntry{}
	}

	utils.JSONResponse(w, BrowseResponse{
		Entries:    paginatedEntries,
		Total:      total,
		Page:       page,
		Limit:      limit,
		TotalPages: totalPages,
		CurrentDir: "/",
	}, http.StatusOK)
}

// handleBrowseGroup returns torrents in a group (__all__, __bad__, custom folder)
func (s *Server) handleBrowseGroup(w http.ResponseWriter, r *http.Request) {
	group := utils.PathUnescape(chi.URLParam(r, "group"))

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	sortBy, sortOrder := getBrowseSortParams(r)

	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))

	currentInfo, children := s.manager.GetEntryChildren(group)
	if currentInfo == nil {
		http.Error(w, "Group not found", http.StatusNotFound)
		return
	}

	// Convert to browse entries
	entries := make([]BrowseEntry, 0, len(children))
	for _, child := range children {
		// Apply search filter
		if search != "" && !strings.Contains(strings.ToLower(child.Name()), search) {
			continue
		}

		// GetReader torrent info hash for deletion support
		canDelete := false
		if child.IsDir() {
			canDelete = true
		}

		entries = append(entries, BrowseEntry{
			Name:         child.Name(),
			Path:         "/" + group + "/" + child.Name(),
			Size:         child.Size(),
			ModTime:      child.ModTime().Format("2006-01-02 15:04:05"),
			IsDir:        child.IsDir(),
			InfoHash:     child.InfoHash(),
			CanDelete:    canDelete,
			ActiveDebrid: child.ActiveDebrid(),
		})
	}
	sortBrowseEntries(entries, sortBy, sortOrder)

	// Apply pagination
	total := len(entries)
	totalPages := (total + limit - 1) / limit
	offset := (page - 1) * limit

	var paginatedEntries []BrowseEntry
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedEntries = entries[offset:end]
	} else {
		paginatedEntries = []BrowseEntry{}
	}

	utils.JSONResponse(w, BrowseResponse{
		Entries:    paginatedEntries,
		Total:      total,
		Page:       page,
		Limit:      limit,
		TotalPages: totalPages,
		CurrentDir: "/" + group,
		ParentDir:  "/",
	}, http.StatusOK)
}

// handleBrowseTorrentFiles returns files in a torrent folder
func (s *Server) handleBrowseTorrentFiles(w http.ResponseWriter, r *http.Request) {
	group := utils.PathUnescape(chi.URLParam(r, "group"))
	torrent := utils.PathUnescape(chi.URLParam(r, "torrent"))

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	sortBy, sortOrder := getBrowseSortParams(r)

	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))

	currentInfo, children := s.manager.GetTorrentChildren(torrent)
	if currentInfo == nil {
		http.Error(w, "Torrent not found", http.StatusNotFound)
		return
	}

	// Convert to browse entries
	entries := make([]BrowseEntry, 0, len(children))
	for _, child := range children {
		// Apply search filter
		if search != "" && !strings.Contains(strings.ToLower(child.Name()), search) {
			continue
		}

		pathParts := []string{"/", group}
		pathParts = append(pathParts, torrent, child.Name())

		entries = append(entries, BrowseEntry{
			Name:         child.Name(),
			Path:         filepath.Join(pathParts...),
			Size:         child.Size(),
			ModTime:      child.ModTime().Format("2006-01-02 15:04:05"),
			IsDir:        child.IsDir(),
			InfoHash:     child.InfoHash(),
			ActiveDebrid: child.ActiveDebrid(),
		})
	}
	sortBrowseEntries(entries, sortBy, sortOrder)

	// Apply pagination
	total := len(entries)
	totalPages := (total + limit - 1) / limit
	offset := (page - 1) * limit

	var paginatedEntries []BrowseEntry
	if offset < total {
		end := offset + limit
		if end > total {
			end = total
		}
		paginatedEntries = entries[offset:end]
	} else {
		paginatedEntries = []BrowseEntry{}
	}

	parentPath := "/" + group

	currentPath := parentPath + "/" + torrent

	response := BrowseResponse{
		Entries:    paginatedEntries,
		Total:      total,
		Page:       page,
		Limit:      limit,
		TotalPages: totalPages,
		CurrentDir: currentPath,
		ParentDir:  parentPath,
	}

	utils.JSONResponse(w, response, http.StatusOK)
}

// handleDeleteBrowseTorrent deletes a torrent by info hash
func (s *Server) handleDeleteBrowseTorrent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		http.Error(w, "Torrent ID is required", http.StatusBadRequest)
		return
	}

	if err := s.manager.DeleteEntry(id, true); err != nil {
		s.logger.Error().Err(err).Str("id", id).Msg("Failed to delete entry")
		http.Error(w, "Failed to delete entry", http.StatusInternalServerError)
		return
	}

	utils.JSONResponse(w, map[string]interface{}{
		"success": true,
		"message": "Item deleted successfully",
	}, http.StatusOK)
}

// handleBatchDeleteBrowseTorrents deletes multiple torrents
func (s *Server) handleBatchDeleteBrowseTorrents(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []string `json:"ids"`
	}

	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if len(req.IDs) == 0 {
		http.Error(w, "No torrent IDs provided", http.StatusBadRequest)
		return
	}

	if err := s.manager.DeleteTorrents(req.IDs, true); err != nil {
		s.logger.Error().Err(err).Msg("Failed to delete torrents")
		http.Error(w, "Failed to delete torrents", http.StatusInternalServerError)
		return
	}

	utils.JSONResponse(w, map[string]interface{}{
		"success": true,
		"message": "Torrents deleted successfully",
		"count":   len(req.IDs),
	}, http.StatusOK)
}

// handleDownloadFile proxies file download for both torrents and NZBs
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	torrentName := utils.PathUnescape(chi.URLParam(r, "torrent"))
	fileName := utils.PathUnescape(chi.URLParam(r, "file"))

	entry, err := s.manager.GetEntryByName(torrentName, fileName)
	if err != nil || entry == nil {
		http.Error(w, "Torrent not found", http.StatusNotFound)
		return
	}

	file, err := entry.GetFile(fileName)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	etag := fmt.Sprintf("\"%x-%x\"", entry.AddedOn.Unix(), file.Size)
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", entry.AddedOn.UTC().Format(http.TimeFormat))

	w.Header().Set("Content-Type", utils.GetContentType(file.Name))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", file.Name))

	switch entry.Protocol {
	case config.ProtocolTorrent:
		s.handleTorrentDownload(w, r, entry, file)
		return
	case config.ProtocolNZB:
		s.handleUsenetDownload(w, r, torrentName, file)
		return
	default:
		s.logger.Error().Msgf("Unsupported protocol: %s for %s/%s", entry.Protocol, entry.Name, fileName)
		http.Error(w, "Unsupported protocol", http.StatusPreconditionFailed)
		return
	}
}

func (s *Server) handleTorrentDownload(w http.ResponseWriter, r *http.Request, entry *storage.Entry, file *storage.File) {
	// For torrents, get debrid download link and redirect
	link, err := s.manager.GetDownloadLink(r.Context(), entry, file.Name)
	if err != nil || link.Empty() {
		s.logger.Error().Err(err).Str("torrent", entry.Name).Str("file", file.Name).Msg("Failed to get download link")
		http.Error(w, "Could not fetch download link", http.StatusPreconditionFailed)
		return
	}

	w.Header().Set("X-Accel-Redirect", link.DownloadLink)
	w.Header().Set("X-Accel-Buffering", "no")
	http.Redirect(w, r, link.DownloadLink, http.StatusFound)
}

func (s *Server) handleUsenetDownload(w http.ResponseWriter, r *http.Request, entryName string, file *storage.File) {
	w.Header().Set("Content-Type", utils.GetContentType(file.Name))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", file.Name))
	w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))

	err := s.manager.Usenet().Download(r.Context(), file.InfoHash, file.Name, w, nil)
	if err != nil && !customerror.IsSilentError(err) {
		s.logger.Error().Err(err).Msg("Download failed")
		// Can't send HTTP error after headers are sent
	}
}
