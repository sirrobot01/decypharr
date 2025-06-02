package webdav

import (
	"context"
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
			return h.getFileInfos(t.Torrent)
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

func (h *Handler) getFileInfos(torrent *types.Torrent) []os.FileInfo {
	torrentFiles := torrent.GetFiles()
	files := make([]os.FileInfo, 0, len(torrentFiles))
	now := time.Now()

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
			modTime: now,
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
		if err := h.handleIDDelete(w, r); err == nil {
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
		Path       string
		ParentPath string
		ShowParent bool
		Children   []os.FileInfo
		URLBase    string
		IsBadPath  bool
		CanDelete  bool
	}{
		Path:       cleanPath,
		ParentPath: parentPath,
		ShowParent: showParent,
		Children:   children,
		URLBase:    h.URLBase,
		IsBadPath:  isBadPath,
		CanDelete:  canDelete,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tplDirectory.ExecuteTemplate(w, "directory.html", data); err != nil {
		return
	}
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	fRaw, err := h.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		h.logger.Error().Err(err).
			Str("path", r.URL.Path).
			Msg("Failed to open file")
		http.NotFound(w, r)
		return
	}
	defer func(fRaw webdav.File) {
		err := fRaw.Close()
		if err != nil {
			h.logger.Error().Err(err).Msg("Failed to close file")
			return
		}
	}(fRaw)

	fi, err := fRaw.Stat()
	if err != nil {
		h.logger.Error().Err(err).Msg("Failed to stat file")
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}

	// If the target is a directory, use your directory listing logic.
	if fi.IsDir() {
		h.serveDirectory(w, r, fRaw)
		return
	}

	// Checks if the file is a torrent file
	// .content is nil if the file is a torrent file
	// .content means file is preloaded, e.g version.txt
	if file, ok := fRaw.(*File); ok && file.content == nil {
		link, err := file.getDownloadLink()
		if err != nil {
			h.logger.Debug().
				Err(err).
				Str("link", file.link).
				Str("path", r.URL.Path).
				Msg("Could not fetch download link")
			http.Error(w, "Could not fetch download link", http.StatusPreconditionFailed)
			return
		}
		if link == "" {
			http.NotFound(w, r)
			return
		}
		file.downloadLink = link
		if h.cache.StreamWithRclone() {
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", fi.Name()))
			w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
			w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("X-Accel-Redirect", file.downloadLink)
			w.Header().Set("X-Accel-Buffering", "no")
			http.Redirect(w, r, file.downloadLink, http.StatusFound)
			return
		}
	}

	// ETags
	etag := fmt.Sprintf("\"%x-%x\"", fi.ModTime().Unix(), fi.Size())
	w.Header().Set("ETag", etag)

	// 7. Content-Type by extension
	ext := filepath.Ext(fi.Name())
	contentType := mime.TypeByExtension(ext)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)

	rs, ok := fRaw.(io.ReadSeeker)
	if !ok {
		if r.Header.Get("Range") != "" {
			http.Error(w, "Range not supported", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
		w.Header().Set("Accept-Ranges", "bytes")
		ctx := r.Context()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.Copy(w, fRaw)
		}()
		select {
		case <-ctx.Done():
			h.logger.Debug().Msg("Client cancelled download")
			return
		case <-done:
		}
		return
	}
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
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

// handleDelete deletes a torrent from using id
func (h *Handler) handleIDDelete(w http.ResponseWriter, r *http.Request) error {
	cleanPath := path.Clean(r.URL.Path) // Remove any leading slashes

	_, torrentId := path.Split(cleanPath)
	if torrentId == "" {
		return os.ErrNotExist
	}

	cachedTorrent := h.cache.GetTorrent(torrentId)
	if cachedTorrent == nil {
		return os.ErrNotExist
	}

	h.cache.OnRemove(cachedTorrent.Id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}
