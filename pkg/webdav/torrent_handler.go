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

type TorrentHandler struct {
	name     string
	logger   zerolog.Logger
	cache    *store.Cache
	URLBase  string
	RootPath string
}

func NewTorrentHandler(name, urlBase string, cache *store.Cache, logger zerolog.Logger) Handler {
	h := &TorrentHandler{
		name:     name,
		cache:    cache,
		logger:   logger,
		URLBase:  urlBase,
		RootPath: path.Join(urlBase, "webdav", name),
	}
	return h
}

func (ht *TorrentHandler) Start(ctx context.Context) error {
	return ht.cache.Start(ctx)
}

func (ht *TorrentHandler) Type() string {
	return "torrent"
}

func (ht *TorrentHandler) Readiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-ht.cache.IsReady():
			// WebDAV is ready, proceed
			next.ServeHTTP(w, r)
		default:
			// WebDAV is still initializing
			w.Header().Set("Retry-After", "5")
			http.Error(w, "WebDAV service is initializing, please try again shortly", http.StatusServiceUnavailable)
		}
	})
}

// Name returns the name of the handler
func (ht *TorrentHandler) Name() string {
	return ht.name
}

// Mkdir implements webdav.FileSystem
func (ht *TorrentHandler) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission // Read-only filesystem
}

// RemoveAll implements webdav.FileSystem
func (ht *TorrentHandler) RemoveAll(ctx context.Context, name string) error {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(ht.RootPath)

	if name == rootDir {
		return os.ErrPermission
	}

	// Skip if it's version.txt
	if name == path.Join(rootDir, "version.txt") {
		return os.ErrPermission
	}

	// Check if the name is a parent path
	if _, ok := ht.isParentPath(name); ok {
		return os.ErrPermission
	}

	// Check if the name is a torrent folder
	rel := strings.TrimPrefix(name, rootDir+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(ht.getParentItems(), parts[0]) {
		torrentName := parts[1]
		torrent := ht.cache.GetTorrentByName(torrentName)
		if torrent == nil {
			return os.ErrNotExist
		}
		// Remove the torrent from the cache and debrid
		ht.cache.OnRemove(torrent.Id)
		return nil
	}
	// If we reach here, it means the path is a file
	if len(parts) >= 2 {
		if utils.Contains(ht.getParentItems(), parts[0]) {
			torrentName := parts[1]
			cached := ht.cache.GetTorrentByName(torrentName)
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file, ok := cached.GetFile(filename); ok {
					if err := ht.cache.RemoveFile(cached.Id, file.Name); err != nil {
						ht.logger.Error().Err(err).Msgf("Failed to remove file %s from torrent %s", file.Name, torrentName)
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
func (ht *TorrentHandler) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission // Read-only filesystem
}

func (ht *TorrentHandler) getTorrentsFolders(folder string) []os.FileInfo {
	return ht.cache.GetListing(folder)
}

func (ht *TorrentHandler) getParentItems() []string {
	parents := []string{"__all__", "torrents", "__bad__"}

	// Add custom folders
	parents = append(parents, ht.cache.GetCustomFolders()...)

	// version.txt
	parents = append(parents, "version.txt")
	return parents
}

func (ht *TorrentHandler) getParentFiles() []os.FileInfo {
	now := time.Now()
	rootFiles := make([]os.FileInfo, 0, len(ht.getParentItems()))
	for _, item := range ht.getParentItems() {
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

// GetChildren returns the os.FileInfo slice for “depth-1” children of cleanPath
func (ht *TorrentHandler) GetChildren(name string) []os.FileInfo {

	if name[0] != '/' {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	root := path.Clean(ht.RootPath)

	// top‐level “parents” (e.g. __all__, torrents etc)
	if name == root {
		return ht.getParentFiles()
	}
	// one level down (e.g. /root/parentFolder)
	if parent, ok := ht.isParentPath(name); ok {
		return ht.getTorrentsFolders(parent)
	}
	// torrent-folder level (e.g. /root/parentFolder/torrentName)
	rel := strings.TrimPrefix(name, root+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(ht.getParentItems(), parts[0]) {
		torrentName := parts[1]
		if t := ht.cache.GetTorrentByName(torrentName); t != nil {
			return ht.getFileInfos(t)
		}
	}
	return nil
}

func (ht *TorrentHandler) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(ht.RootPath)
	metadataOnly := ctx.Value(metadataOnlyKey) != nil
	now := time.Now()

	// 1) special case version.txt
	if name == path.Join(rootDir, "version.txt") {
		versionInfo := version.GetInfo().String()
		return &TorrentFile{
			cache:        ht.cache,
			isDir:        false,
			content:      []byte(versionInfo),
			name:         "version.txt",
			size:         int64(len(versionInfo)),
			metadataOnly: metadataOnly,
			modTime:      now,
		}, nil
	}

	// 2) directory case: ask Children
	if children := ht.GetChildren(name); children != nil {
		displayName := filepath.Clean(path.Base(name))
		if name == rootDir {
			displayName = "/"
		}
		return &TorrentFile{
			cache:        ht.cache,
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
		if utils.Contains(ht.getParentItems(), parts[0]) {
			torrentName := parts[1]
			cached := ht.cache.GetTorrentByName(torrentName)
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file, ok := cached.GetFile(filename); ok && !file.Deleted {
					return &TorrentFile{
						cache:        ht.cache,
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
	return nil, os.ErrNotExist
}

// Stat implements webdav.FileSystem
func (ht *TorrentHandler) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	f, err := ht.OpenFile(ctx, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return f.Stat()
}

func (ht *TorrentHandler) getFileInfos(torrent *store.CachedTorrent) []os.FileInfo {
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

func (ht *TorrentHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	switch r.Method {
	case "GET":
		ht.handleGet(w, r)
		return
	case "HEAD":
		ht.handleHead(w, r)
		return
	case "OPTIONS":
		ht.handleOptions(w, r)
		return
	case "PROPFIND":
		ht.handlePropfind(w, r)
		return
	case "DELETE":
		if err := ht.handleDelete(w, r); err == nil {
			return
		}
		// fallthrough to default
	}
	handler := &webdav.Handler{
		FileSystem: ht,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				ht.logger.Trace().
					Err(err).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Msg("WebDAV error")
			}
		},
	}
	handler.ServeHTTP(w, r)
}

func (ht *TorrentHandler) isParentPath(urlPath string) (string, bool) {
	parents := ht.getParentItems()
	lastComponent := path.Base(urlPath)
	for _, p := range parents {
		if p == lastComponent {
			return p, true
		}
	}
	return "", false
}

func (ht *TorrentHandler) serveDirectory(w http.ResponseWriter, r *http.Request, file webdav.File) {
	var children []os.FileInfo
	if f, ok := file.(*TorrentFile); ok {
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
	_, canDelete := ht.isParentPath(cleanPath)

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
		URLBase:                ht.URLBase,
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

func (ht *TorrentHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	fRaw, err := ht.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
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
		ht.serveDirectory(w, r, fRaw)
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
	if file, ok := fRaw.(*TorrentFile); ok {
		// Handle nginx proxy (X-Accel-Redirect)
		if file.content == nil && !file.isRar && ht.cache.StreamWithRclone() {
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
				} else {
					ht.logger.Error().
						Err(streamErr.Err).
						Str("path", r.URL.Path).
						Msg("Stream error")
				}
			} else {
				// Generic error
				if !hasHeadersWritten(w) {
					http.Error(w, "Stream error", http.StatusInternalServerError)
				} else {
					ht.logger.Error().
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

func (ht *TorrentHandler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	handlePropfind(ht, ht.logger, w, r)
}

func (ht *TorrentHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	f, err := ht.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		ht.logger.Error().Err(err).Str("path", r.URL.Path).Msg("Failed to open file")
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
		ht.logger.Error().Err(err).Msg("Failed to stat file")
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", getContentType(fi.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

func (ht *TorrentHandler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND")
	w.Header().Set("DAV", "1, 2")
	w.WriteHeader(http.StatusOK)
}

// handleDelete deletes a torrent by id, or all bad torrents if the id is DeleteAllBadTorrentKey
func (ht *TorrentHandler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	cleanPath := path.Clean(r.URL.Path) // Remove any leading slashes

	_, torrentId := path.Split(cleanPath)
	if torrentId == "" {
		return os.ErrNotExist
	}

	if torrentId == DeleteAllBadTorrentKey {
		return ht.handleDeleteAll(w)
	}

	return ht.handleDeleteById(w, torrentId)
}

func (ht *TorrentHandler) handleDeleteById(w http.ResponseWriter, tId string) error {
	cachedTorrent := ht.cache.GetTorrent(tId)
	if cachedTorrent == nil {
		return os.ErrNotExist
	}

	ht.cache.OnRemove(cachedTorrent.Id)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (ht *TorrentHandler) handleDeleteAll(w http.ResponseWriter) error {
	badTorrents := ht.cache.GetListing("__bad__")
	if len(badTorrents) == 0 {
		http.Error(w, "No bad torrents to delete", http.StatusNotFound)
		return nil
	}

	for _, fi := range badTorrents {
		tName := strings.TrimSpace(strings.SplitN(fi.Name(), "||", 2)[0])
		t := ht.cache.GetTorrentByName(tName)
		if t != nil {
			ht.cache.OnRemove(t.Id)
		}
	}

	w.WriteHeader(http.StatusNoContent)
	return nil
}
