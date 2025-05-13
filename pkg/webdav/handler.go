package webdav

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/debrid"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/version"
	"golang.org/x/net/webdav"
)

type Handler struct {
	Name     string
	logger   zerolog.Logger
	cache    *debrid.Cache
	RootPath string
}

func NewHandler(name string, cache *debrid.Cache, logger zerolog.Logger) *Handler {
	h := &Handler{
		Name:     name,
		cache:    cache,
		logger:   logger,
		RootPath: fmt.Sprintf("/%s", name),
	}
	return h
}

// Mkdir implements webdav.FileSystem
func (h *Handler) Mkdir(ctx context.Context, name string, perm os.FileMode) error {
	return os.ErrPermission // Read-only filesystem
}

// RemoveAll implements webdav.FileSystem
func (h *Handler) RemoveAll(ctx context.Context, name string) error {
	if name[0] != '/' {
		name = "/" + name
	}
	name = filepath.Clean(name)

	rootDir := filepath.Clean(h.getRootPath())

	if name == rootDir {
		return os.ErrPermission
	}

	torrentName, _ := getName(rootDir, name)
	cachedTorrent := h.cache.GetTorrentByName(torrentName)
	if cachedTorrent == nil {
		h.logger.Debug().Msgf("Torrent not found: %s", torrentName)
		return nil // It's possible that the torrent was removed
	}

	h.cache.OnRemove(cachedTorrent.Id)
	return nil
}

// Rename implements webdav.FileSystem
func (h *Handler) Rename(ctx context.Context, oldName, newName string) error {
	return os.ErrPermission // Read-only filesystem
}

func (h *Handler) getRootPath() string {
	return fmt.Sprintf(filepath.Join(string(os.PathSeparator), "webdav", "%s"), h.Name)
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
	name = utils.PathUnescape(name)
	name = filepath.Clean(name)
	if !strings.HasPrefix(name, string(os.PathSeparator)) {
		name = string(os.PathSeparator) + name
	}
	root := filepath.Clean(h.getRootPath())
	if name == root {
		return h.getParentFiles()
	}
	// one level down (e.g. /root/parentFolder)
	if parent, ok := h.isParentPath(name); ok {
		return h.getTorrentsFolders(parent)
	}
	// torrent-folder level (e.g. /root/parentFolder/torrentName)
	rel := strings.TrimPrefix(name, root+string(os.PathSeparator))
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) == 2 && utils.Contains(h.getParentItems(), parts[0]) {
		torrentName := parts[1]
		if t := h.cache.GetTorrentByName(torrentName); t != nil {
			return h.getFileInfos(t.Torrent)
		}
	}
	return nil
}

func (h *Handler) OpenFile(ctx context.Context, name string, flag int, perm os.FileMode) (webdav.File, error) {
	name = utils.PathUnescape(name)
	name = filepath.Clean(name)
	if !strings.HasPrefix(name, string(os.PathSeparator)) {
		name = string(os.PathSeparator) + name
	}
	rootDir := filepath.Clean(h.getRootPath())
	metadataOnly := ctx.Value("metadataOnly") != nil
	now := time.Now()

	// 1) special case version.txt
	if name == filepath.Join(rootDir, "version.txt") {
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
		displayName := filepath.Base(name)
		if name == rootDir {
			displayName = string(os.PathSeparator)
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
	rel := strings.TrimPrefix(name, rootDir+string(os.PathSeparator))
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) >= 2 {
		if utils.Contains(h.getParentItems(), parts[0]) {
			torrentName := parts[1]
			cached := h.cache.GetTorrentByName(torrentName)
			if cached != nil && len(parts) >= 3 {
				filename := filepath.Join(parts[2:]...)
				if file, ok := cached.Files[filename]; ok {
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
	files := make([]os.FileInfo, 0, len(torrent.Files))
	now := time.Now()

	// Sort by file name since the order is lost when using the map
	sortedFiles := make([]*types.File, 0, len(torrent.Files))
	for _, file := range torrent.Files {
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

	default:
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
		return

	}
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

	// Prepare template data
	data := struct {
		Path       string
		ParentPath string
		ShowParent bool
		Children   []os.FileInfo
	}{
		Path:       cleanPath,
		ParentPath: parentPath,
		ShowParent: showParent,
		Children:   children,
	}

	// Parse and execute template
	funcMap := template.FuncMap{
		"add": func(a, b int) int {
			return a + b
		},
		"urlpath": func(p string) string {
			segments := strings.Split(p, "/")
			for i, segment := range segments {
				segments[i] = url.PathEscape(segment)
			}
			return strings.Join(segments, "/")
		},
		"formatSize": func(bytes int64) string {
			const (
				KB = 1024
				MB = 1024 * KB
				GB = 1024 * MB
				TB = 1024 * GB
			)

			var size float64
			var unit string

			switch {
			case bytes >= TB:
				size = float64(bytes) / TB
				unit = "TB"
			case bytes >= GB:
				size = float64(bytes) / GB
				unit = "GB"
			case bytes >= MB:
				size = float64(bytes) / MB
				unit = "MB"
			case bytes >= KB:
				size = float64(bytes) / KB
				unit = "KB"
			default:
				size = float64(bytes)
				unit = "bytes"
			}

			// Format to 2 decimal places for larger units, no decimals for bytes
			if unit == "bytes" {
				return fmt.Sprintf("%.0f %s", size, unit)
			}
			return fmt.Sprintf("%.2f %s", size, unit)
		},
		"hasSuffix": strings.HasSuffix,
	}
	tmpl, err := template.New("directory").Funcs(funcMap).Parse(directoryTemplate)
	if err != nil {
		h.logger.Error().Err(err).Msg("Failed to parse directory template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		h.logger.Error().Err(err).Msg("Failed to execute directory template")
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
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
	defer fRaw.Close()

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
	}

	rs, ok := fRaw.(io.ReadSeeker)
	if !ok {
		// If not, read the entire file into memory as a fallback.
		buf, err := io.ReadAll(fRaw)
		if err != nil {
			h.logger.Error().Err(err).Msg("Failed to read file content")
			http.Error(w, "Server Error", http.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(buf)
	}
	fileName := fi.Name()
	contentType := getContentType(fileName)
	w.Header().Set("Content-Type", contentType)
	// http.ServeContent automatically handles Range requests.
	http.ServeContent(w, r, fileName, fi.ModTime(), rs)
}

func (h *Handler) handleHead(w http.ResponseWriter, r *http.Request) {
	f, err := h.OpenFile(r.Context(), r.URL.Path, os.O_RDONLY, 0)
	if err != nil {
		h.logger.Error().Err(err).Str("path", r.URL.Path).Msg("Failed to open file")
		http.NotFound(w, r)
		return
	}
	defer f.Close()

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
