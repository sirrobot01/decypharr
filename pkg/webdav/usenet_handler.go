package webdav

import (
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"golang.org/x/net/webdav"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/version"
)

type UsenetHandler struct {
	name     string
	logger   zerolog.Logger
	usenet   usenet.Usenet
	URLBase  string
	RootPath string
}

func NewUsenetHandler(name, urlBase string, usenet usenet.Usenet, logger zerolog.Logger) Handler {
	h := &UsenetHandler{
		name:     name,
		usenet:   usenet,
		logger:   logger,
		URLBase:  urlBase,
		RootPath: path.Join(urlBase, "webdav", name),
	}
	return h
}

func (hu *UsenetHandler) Type() string {
	return "usenet"
}

func (hu *UsenetHandler) Name() string {
	return hu.name
}

func (hu *UsenetHandler) Start(ctx context.Context) error {
	return hu.usenet.Start(ctx)
}

func (hu *UsenetHandler) Readiness(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hu.usenet.IsReady():
			// WebDAV is ready, proceed
			next.ServeHTTP(w, r)
		default:
			// WebDAV is still initializing
			w.Header().Set("Retry-After", "5")
			http.Error(w, "WebDAV service is initializing, please try again shortly", http.StatusServiceUnavailable)
		}
	})
}

// Mkdir implements webdav.FileSystem
func (hu *UsenetHandler) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission // Read-only filesystem
}

// RemoveAll implements webdav.FileSystem
func (hu *UsenetHandler) RemoveAll(ctx context.Context, name string) error {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(hu.RootPath)

	if name == rootDir {
		return os.ErrPermission
	}

	// Skip if it's version.txt
	if name == path.Join(rootDir, "version.txt") {
		return os.ErrPermission
	}

	// Check if the name is a parent path
	if _, ok := hu.isParentPath(name); ok {
		return os.ErrPermission
	}

	// Check if the name is a torrent folder
	rel := strings.TrimPrefix(name, rootDir+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(hu.getParentItems(), parts[0]) {
		nzb := hu.usenet.Store().GetByName(parts[1])
		if nzb == nil {
			return os.ErrNotExist
		}
		// Remove the nzb from the store
		if err := hu.usenet.Store().Delete(nzb.ID); err != nil {
			hu.logger.Error().Err(err).Msgf("Failed to remove torrent %s", parts[1])
			return err
		}
		return nil
	}
	// If we reach here, it means the path is a file
	if len(parts) >= 2 {
		if utils.Contains(hu.getParentItems(), parts[0]) {
			cached := hu.usenet.Store().GetByName(parts[1])
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file := cached.GetFileByName(filename); file != nil {
					if err := hu.usenet.Store().RemoveFile(cached.ID, file.Name); err != nil {
						hu.logger.Error().Err(err).Msgf("Failed to remove file %s from torrent %s", file.Name, parts[1])
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
func (hu *UsenetHandler) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission // Read-only filesystem
}

func (hu *UsenetHandler) getTorrentsFolders(folder string) []os.FileInfo {
	return hu.usenet.Store().GetListing(folder)
}

func (hu *UsenetHandler) getParentItems() []string {
	parents := []string{"__all__", "__bad__"}

	// version.txt
	parents = append(parents, "version.txt")
	return parents
}

func (hu *UsenetHandler) getParentFiles() []os.FileInfo {
	now := time.Now()
	rootFiles := make([]os.FileInfo, 0, len(hu.getParentItems()))
	for _, item := range hu.getParentItems() {
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
func (hu *UsenetHandler) GetChildren(name string) []os.FileInfo {

	if name[0] != '/' {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	root := path.Clean(hu.RootPath)

	// top‐level “parents” (e.g. __all__, torrents etc)
	if name == root {
		return hu.getParentFiles()
	}
	if parent, ok := hu.isParentPath(name); ok {
		return hu.getTorrentsFolders(parent)
	}
	// torrent-folder level (e.g. /root/parentFolder/torrentName)
	rel := strings.TrimPrefix(name, root+"/")
	parts := strings.Split(rel, "/")
	if len(parts) == 2 && utils.Contains(hu.getParentItems(), parts[0]) {
		if u := hu.usenet.Store().GetByName(parts[1]); u != nil {
			return hu.getFileInfos(u)
		}
	}
	return nil
}

func (hu *UsenetHandler) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	if !strings.HasPrefix(name, "/") {
		name = "/" + name
	}
	name = utils.PathUnescape(path.Clean(name))
	rootDir := path.Clean(hu.RootPath)
	metadataOnly := ctx.Value(metadataOnlyKey) != nil
	now := time.Now()

	// 1) special case version.txt
	if name == path.Join(rootDir, "version.txt") {
		versionInfo := version.GetInfo().String()
		return &UsenetFile{
			usenet:       hu.usenet,
			isDir:        false,
			content:      []byte(versionInfo),
			name:         "version.txt",
			size:         int64(len(versionInfo)),
			metadataOnly: metadataOnly,
			modTime:      now,
		}, nil
	}

	// 2) directory case: ask GetChildren
	if children := hu.GetChildren(name); children != nil {
		displayName := filepath.Clean(path.Base(name))
		if name == rootDir {
			displayName = "/"
		}
		return &UsenetFile{
			usenet:       hu.usenet,
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
		if utils.Contains(hu.getParentItems(), parts[0]) {
			cached := hu.usenet.Store().GetByName(parts[1])
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Clean(path.Join(parts[2:]...))
				if file := cached.GetFileByName(filename); file != nil {
					return &UsenetFile{
						usenet:       hu.usenet,
						nzbID:        cached.ID,
						fileId:       file.Name,
						isDir:        false,
						name:         file.Name,
						size:         file.Size,
						metadataOnly: metadataOnly,
						modTime:      cached.AddedOn,
					}, nil
				}
			}
		}
	}
	return nil, os.ErrNotExist
}

// Stat implements webdav.FileSystem
func (hu *UsenetHandler) Stat(ctx context.Context, name string) (os.FileInfo, error) {
	f, err := hu.OpenFile(ctx, name, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	return f.Stat()
}

func (hu *UsenetHandler) getFileInfos(nzb *usenet.NZB) []os.FileInfo {
	nzbFiles := nzb.GetFiles()
	files := make([]os.FileInfo, 0, len(nzbFiles))

	sort.Slice(nzbFiles, func(i, j int) bool {
		return nzbFiles[i].Name < nzbFiles[j].Name
	})

	for _, file := range nzbFiles {
		files = append(files, &FileInfo{
			name:    file.Name,
			size:    file.Size,
			mode:    0644,
			modTime: nzb.AddedOn,
			isDir:   false,
		})
	}
	return files
}

func (hu *UsenetHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		hu.handleGet(w, r)
		return
	case "HEAD":
		hu.handleHead(w, r)
		return
	case "OPTIONS":
		hu.handleOptions(w, r)
		return
	case "PROPFIND":
		hu.handlePropfind(w, r)
		return
	case "DELETE":
		if err := hu.handleDelete(w, r); err == nil {
			return
		}
		// fallthrough to default
	}
	handler := &webdav.Handler{
		FileSystem: hu,
		LockSystem: webdav.NewMemLS(),
		Logger: func(r *http.Request, err error) {
			if err != nil {
				hu.logger.Trace().
					Err(err).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Msg("WebDAV error")
			}
		},
	}
	handler.ServeHTTP(w, r)
}

func (hu *UsenetHandler) isParentPath(urlPath string) (string, bool) {
	parents := hu.getParentItems()
	lastComponent := path.Base(urlPath)
	for _, p := range parents {
		if p == lastComponent {
			return p, true
		}
	}
	return "", false
}

func (hu *UsenetHandler) serveDirectory(w http.ResponseWriter, r *http.Request, file webdav.File) {
	var children []os.FileInfo
	if f, ok := file.(*UsenetFile); ok {
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
	_, canDelete := hu.isParentPath(cleanPath)

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
		URLBase:                hu.URLBase,
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

func (hu *UsenetHandler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	handlePropfind(hu, hu.logger, w, r)
}

func (hu *UsenetHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	fRaw, err := hu.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
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
		hu.serveDirectory(w, r, fRaw)
		return
	}

	// Set common headers
	etag := fmt.Sprintf("\"%x-%x\"", fi.ModTime().Unix(), fi.Size())
	ext := path.Ext(fi.Name())
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Content-Type", getContentType(ext))
	w.Header().Set("Connection", "keep-alive")

	// Handle File struct with direct streaming
	if file, ok := fRaw.(*UsenetFile); ok {
		if err := file.StreamResponse(w, r); err != nil {
			var streamErr *streamError
			if errors.As(err, &streamErr) {
				// Handle client disconnections silently (just debug log)
				if errors.Is(streamErr.Err, context.Canceled) || errors.Is(streamErr.Err, context.DeadlineExceeded) || streamErr.IsClientDisconnection {
					return // Don't log as error or try to write response
				}

				if streamErr.StatusCode > 0 && !hasHeadersWritten(w) {
					return
				} else {
					hu.logger.Error().
						Err(streamErr.Err).
						Str("path", r.URL.Path).
						Msg("Stream error")
				}
			} else {
				// Generic error
				if !hasHeadersWritten(w) {
					http.Error(w, "Stream error", http.StatusInternalServerError)
					return
				} else {
					hu.logger.Error().
						Err(err).
						Str("path", r.URL.Path).
						Msg("Stream error after headers written")
				}
			}
			return
		}
		return
	}

	// Fallback to ServeContent for other webdav.File implementations
	if rs, ok := fRaw.(io.ReadSeeker); ok {
		http.ServeContent(w, r, fi.Name(), fi.ModTime(), rs)
	} else {
		w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
		_, _ = io.Copy(w, fRaw)
	}
}

func (hu *UsenetHandler) handleHead(w http.ResponseWriter, r *http.Request) {
	f, err := hu.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		hu.logger.Error().Err(err).Str("path", r.URL.Path).Msg("Failed to open file")
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
		hu.logger.Error().Err(err).Msg("Failed to stat file")
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fi.Size()))
	w.Header().Set("Last-Modified", fi.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

func (hu *UsenetHandler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND")
	w.WriteHeader(http.StatusOK)
}

// handleDelete deletes a torrent by id, or all bad torrents if the id is DeleteAllBadTorrentKey
func (hu *UsenetHandler) handleDelete(w http.ResponseWriter, r *http.Request) error {
	cleanPath := path.Clean(r.URL.Path) // Remove any leading slashes

	_, torrentId := path.Split(cleanPath)
	if torrentId == "" {
		return os.ErrNotExist
	}

	if torrentId == DeleteAllBadTorrentKey {
		return hu.handleDeleteAll(w)
	}

	return hu.handleDeleteById(w, torrentId)
}

func (hu *UsenetHandler) handleDeleteById(w http.ResponseWriter, nzID string) error {
	cached := hu.usenet.Store().Get(nzID)
	if cached == nil {
		return os.ErrNotExist
	}

	err := hu.usenet.Store().Delete(nzID)
	if err != nil {
		hu.logger.Error().Err(err).Str("nzbID", nzID).Msg("Failed to delete NZB")
		http.Error(w, "Failed to delete NZB", http.StatusInternalServerError)
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (hu *UsenetHandler) handleDeleteAll(w http.ResponseWriter) error {

	w.WriteHeader(http.StatusNoContent)
	return nil
}
