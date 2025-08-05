package webdav

import (
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"golang.org/x/net/webdav"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/version"
)

const DeleteAllBadTorrentKey = "DELETE_ALL_BAD_TORRENTS"

type Handler struct {
	Name     string
	logger   zerolog.Logger
	cache    *store.Cache
	URLBase  string
	RootPath string
}

func NewHandler(name, urlBase string, cache *store.Cache, logger zerolog.Logger) *Handler {
	h := &Handler{
		Name:     name,
		cache:    cache,
		logger:   logger,
		URLBase:  urlBase,
		RootPath: path.Join(urlBase, "webdav", name),
	}
	return h
}

// Mkdir implements webdav.FileSystem
func (h *Handler) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission // Read-only filesystem
}

func (h *Handler) readinessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-h.cache.IsReady():
			// WebDAV is ready, proceed
			next.ServeHTTP(w, r)
		default:
			// WebDAV is still initializing
			w.Header().Set("Retry-After", "5")
			http.Error(w, "WebDAV service is initializing, please try again shortly", http.StatusServiceUnavailable)
		}
	})
}

// RemoveAll implements webdav.FileSystem
func (h *Handler) RemoveAll(ctx context.Context, name string) error {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(h.RootPath)

	if name == rootDir {
		return os.ErrPermission
	}

	// Skip if it's version.txt
	if name == path.Join(rootDir, "version.txt") {
		return os.ErrPermission
	}

	// Check if the name is a parent path
	if _, ok := h.isParentPath(name); ok {
		return os.ErrPermission
	}

	// Check if the name is a torrent folder
	rel := strings.TrimPrefix(name, rootDir+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(h.getParentItems(), parts[0]) {
		torrentName := parts[1]
		torrent := h.cache.GetTorrentByName(torrentName)
		if torrent == nil {
			return os.ErrNotExist
		}
		// Remove the torrent from the cache and debrid
		h.cache.OnRemove(torrent.Id)
		return nil
	}
	// If we reach here, it means the path is a file
	if len(parts) >= 2 {
		if utils.Contains(h.getParentItems(), parts[0]) {
			torrentName := parts[1]
			cached := h.cache.GetTorrentByName(torrentName)
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file, ok := cached.GetFile(filename); ok {
					if err := h.cache.RemoveFile(cached.Id, file.Name); err != nil {
						h.logger.Error().Err(err).Msgf("Failed to remove file %s from torrent %s", file.Name, torrentName)
						return err
					}
					// If the file was successfully removed, we can return nil
					return nil
				}
			}
		}
	}

	return nil
}

// Rename implements webdav.FileSystem
func (h *Handler) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission // Read-only filesystem
}

func (h *Handler) getTorrentsFolders(folder string) []os.FileInfo {
	return h.cache.GetListing(folder)
}

func (h *Handler) getParentItems() []string {
	parents := []string{"__all__", "torrents", "__bad__"}

	// Add custom folders
	parents = append(parents, h.cache.GetCustomFolders()...)

	// version.txt
	parents = append(parents, "version.txt")
	return parents
}

func (h *Handler) getParentFiles() []os.FileInfo {
	now := time.Now()
	rootFiles := make([]os.FileInfo, 0, len(h.getParentItems()))
	for _, item := range h.getParentItems() {
		f := &FileInfo{
			name:    item,
			size:    0,
			mode:    0755 | os.ModeDir,
			modTime: now,
			isDir:   true,
		}
		if item == "version.txt" {
			f.isDir = false
			f.size = int64(len(version.GetInfo().String()))
		}
		rootFiles = append(rootFiles, f)
	}
	return rootFiles
}

// returns the os.FileInfo slice for “depth-1” children of cleanPath
func (h *Handler) getChildren(name string) []os.FileInfo {

	if name[0] != '/' {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	root := path.Clean(h.RootPath)

	// top‐level “parents” (e.g. __all__, torrents etc)
	if name == root {
		return h.getParentFiles()
	}
	// one level down (e.g. /root/parentFolder)
	if parent, ok := h.isParentPath(name); ok {
		return h.getTorrentsFolders(parent)
	}
	// torrent-folder level (e.g. /root/parentFolder/torrentName)
	rel := strings.TrimPrefix(name, root+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(h.getParentItems(), parts[0]) {
		torrentName := parts[1]
		if t := h.cache.GetTorrentByName(torrentName); t != nil {
			return h.getFileInfos(t)
		}
	}
	return nil
}

func (h *Handler) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(h.RootPath)
	metadataOnly := ctx.Value(metadataOnlyKey) != nil
	now := time.Now()

	// 1) special case version.txt
	if name == path.Join(rootDir, "version.txt") {
		versionInfo := version.GetInfo().String()
		return &File{
			cache:        h.cache,
			isDir:        false,
			content:      []byte(versionInfo),
			name:         "version.txt",
			size:         int64(len(versionInfo)),
			metadataOnly: metadataOnly,
			modTime:      now,
		}, nil
	}

	// 2) directory case: ask getChildren
	if children := h.getChildren(name); children != nil {
		displayName := filepath.Clean(path.Base(name))
		if name == rootDir {
			displayName = "/"
		}
		return &File{
			cache:        h.cache,
			isDir:        true,
			children:     children,
			name:         displayName,
			size:         0,
			metadataOnly: metadataOnly,
			modTime:      now,
		}, nil
	}

	// 3) file‐within‐torrent case
	// everything else must be a file under a torrent folder
	rel := strings.TrimPrefix(name, rootDir+"/")
	parts := strings.Split(rel, "/")
	if len(parts) >= 2 {
		if utils.Contains(h.getParentItems(), parts[0]) {
			torrentName := parts[1]
			cached := h.cache.GetTorrentByName(torrentName)
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file, ok := cached.GetFile(filename); ok && !file.Deleted {
					return &File{
						cache:        h.cache,
						torrentName:  torrentName,
						fileId:       file.Id,
						isDir:        false,
						name:         file.Name,
						size:         file.Size,
						link:         file.Link,
						metadataOnly: metadataOnly,
						isRar:        file.IsRar,
						modTime:      cached.AddedOn,
					}, nil
				}
			}
		}
	}

	h.logger.Info().Msgf("File not found: %s", name)
	return nil, os.ErrNotExist
}

// Stat implements webdav.FileSystem
func (h *Handler) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	f, err := h.OpenFile(ctx, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return f.Stat()
}

func (h *Handler) getFileInfos(torrent *store.CachedTorrent) []os.FileInfo {
	torrentFiles := torrent.GetFiles()
	files := make([]os.FileInfo, 0, len(torrentFiles))

	// Sort by file name since the order is lost when using the map
	sortedFiles := make([]*types.File, 0, len(torrentFiles))
	for _, file := range torrentFiles {
		sortedFiles = append(sortedFiles, &file)
	}
	slices.SortFunc(sortedFiles, func(a, b *types.File) int {
		return strings.Compare(a.Name, b.Name)
	})

	for _, file := range sortedFiles {
		files = append(files, &FileInfo{
			name:    file.Name,
			size:    file.Size,
			mode:    0644,
			modTime: torrent.AddedOn,
			isDir:   false,
		})
	}
	return files
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	switch r.Method {
	case "GET":
		h.handleGet(w, r)
		return
	case "HEAD":
		h.handleHead(w, r)
		return
	case "OPTIONS":
		h.handleOptions(w, r)
		return
	case "PROPFIND":
		h.handlePropfind(w, r)
		return
	case "DELETE":
		if err := h.handleDelete(w, r); err == nil {
			return
		}
		// fallthrough to default
	}
	handler := &webdav.Handler{
		FileSystem: h,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				h.logger.Trace().
					Err(err).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Msg("WebDAV error")
			}
		},
	}
	handler.ServeHTTP(w, r)
}

func getContentType(fileName string) string {
	contentType := "application/octet-stream"

	// Determine content type based on file extension
	switch {
	case strings.HasSuffix(fileName, ".mp4"):
		contentType = "video/mp4"
	case strings.HasSuffix(fileName, ".mkv"):
		contentType = "video/x-matroska"
	case strings.HasSuffix(fileName, ".avi"):
		contentType = "video/x-msvideo"
	case strings.HasSuffix(fileName, ".mov"):
		contentType = "video/quicktime"
	case strings.HasSuffix(fileName, ".m4v"):
		contentType = "video/x-m4v"
	case strings.HasSuffix(fileName, ".ts"):
		contentType = "video/mp2t"
	case strings.HasSuffix(fileName, ".srt"):
		contentType = "application/x-subrip"
	case strings.HasSuffix(fileName, ".vtt"):
		contentType = "text/vtt"
	}
	return contentType
}

func (h *Handler) isParentPath(urlPath string) (string, bool) {
	parents := h.getParentItems()
	lastComponent := path.Base(urlPath)
	for _, p := range parents {
		if p == lastComponent {
			return p, true
		}
	}
	return "", false
}

func (h *Handler) serveDirectory(w http.ResponseWriter, r *http.Request, file webdav.File) {
	var children []os.FileInfo
	if f, ok := file.(*File); ok {
		children = f.children
	} else {
		var err error
		children, err = file.Readdir(-1)
		if err != nil {
			http.Error(w, "Failed to list directory", http.StatusInternalServerError)
			return
		}
	}

	// Clean and prepare the path
	cleanPath := path.Clean(r.URL.Path)
	parentPath := path.Dir(cleanPath)
	showParent := cleanPath != "/" && parentPath != "." && parentPath != cleanPath
	isBadPath := strings.HasSuffix(cleanPath, "__bad__")
	_, canDelete := h.isParentPath(cleanPath)

	// Prepare template data
	data := struct {
		Path                   string
		ParentPath             string
		ShowParent             bool
		Children               []os.FileInfo
		URLBase                string
		IsBadPath              bool
		CanDelete              bool
		DeleteAllBadTorrentKey string
	}{
		Path:                   cleanPath,
		ParentPath:             parentPath,
		ShowParent:             showParent,
		Children:               children,
		URLBase:                h.URLBase,
		IsBadPath:              isBadPath,
		CanDelete:              canDelete,
		DeleteAllBadTorrentKey: DeleteAllBadTorrentKey,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tplDirectory.ExecuteTemplate(w, "directory.html", data); err != nil {
		return
	}
}

// Handlers

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	fRaw, err := h.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer fRaw.Close()

	fi, err := fRaw.Stat()
	if err != nil {
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}

	if fi.IsDir() {
		h.serveDirectory(w, r, fRaw)
		return
	}

	// Set common headers
	etag := fmt.Sprintf("\"%x-%x\"", fi.ModTime().Unix(), fi.Size())
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))

	ext := filepath.Ext(fi.Name())
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	// Handle File struct with direct streaming
	if file, ok := fRaw.(*File); ok {
		// Handle nginx proxy (X-Accel-Redirect)
		if file.content == nil && !file.isRar && h.cache.StreamWithRclone() {
			link, err := file.getDownloadLink()
			if err != nil || link == "" {
				http.Error(w, "Could not fetch download link", http.StatusPreconditionFailed)
				return
			}

			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fi.Name()))
			w.Header().Set("X-Accel-Redirect", link)
			w.Header().Set("X-Accel-Buffering", "no")
			http.Redirect(w, r, link, http.StatusFound)
			return
		}

		if err := file.StreamResponse(w, r); err != nil {
			var streamErr *streamError
			if errors.As(err, &streamErr) {
				// Handle client disconnections silently (just debug log)
				if errors.Is(streamErr.Err, context.Canceled) || errors.Is(streamErr.Err, context.DeadlineExceeded) || streamErr.IsClientDisconnection {
					return // Don't log as error or try to write response
				}

				if streamErr.StatusCode > 0 && !hasHeadersWritten(w) {
					http.Error(w, streamErr.Error(), streamErr.StatusCode)
					return
				} else {
					h.logger.Error().
						Err(streamErr.Err).
						Str("path", r.URL.Path).
						Msg("Stream error")
				}
			} else {
				// Generic error
				if !hasHeadersWritten(w) {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				} else {
					h.logger.Error().
						Err(err).
						Str("path", r.URL.Path).
						Msg("Stream error after headers written")
				}
			}
		}
		return
	}

	// Fallback to ServeContent for other webdav.File implementations
	if rs, ok := fRaw.(io.ReadSeeker); ok {
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
	} else {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, fRaw)
	}
}

func (h *Handler) handleHead(w http.ResponseWriter, r *http.Request) {
	f, err := h.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		h.logger.Error().Err(err).Str("path", r.URL.Path).Msg("Failed to open file")
		http.NotFound(w, r)
		return
	}
	defer func(f webdav.File) {
		err := f.Close()
		if err != nil {
			return
		}
	}(f)

	fi, err := f.Stat()
	if err != nil {
		h.logger.Error().Err(err).Msg("Failed to stat file")
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", getContentType(fi.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND")
	w.Header().Set("DAV", "1, 2")
	w.WriteHeader(http.StatusOK)
}

// handleDelete deletes a torrent by id, or all bad torrents if the id is DeleteAllBadTorrentKey
func (h *Handler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	cleanPath := path.Clean(r.URL.Path) // Remove any leading slashes

	_, torrentId := path.Split(cleanPath)
	if torrentId == "" {
		return os.ErrNotExist
	}

	if torrentId == DeleteAllBadTorrentKey {
		return h.handleDeleteAll(w)
	}

	return h.handleDeleteById(w, torrentId)
}

func (h *Handler) handleDeleteById(w http.ResponseWriter, tId string) error {
	cachedTorrent := h.cache.GetTorrent(tId)
	if cachedTorrent == nil {
		return os.ErrNotExist
	}

	h.cache.OnRemove(cachedTorrent.Id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) handleDeleteAll(w http.ResponseWriter) error {
	badTorrents := h.cache.GetListing("__bad__")
	if len(badTorrents) == 0 {
		http.Error(w, "No bad torrents to delete", http.StatusNotFound)
		return nil
	}

	for _, fi := range badTorrents {
		tName := strings.TrimSpace(strings.SplitN(fi.Name(), "||", 2)[0])
		t := h.cache.GetTorrentByName(tName)
		if t != nil {
			h.cache.OnRemove(t.Id)
		}
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}
