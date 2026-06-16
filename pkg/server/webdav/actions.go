package webdav

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"path"
	"path/filepath"

	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

func (h *Handler) handlePropfind(current *manager.FileInfo, children []manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	cleanPath := path.Clean(r.URL.Path)
	sb := convertToXML(cleanPath, current, children)
	// Set headers
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")

	// Set status code and write response
	w.WriteHeader(http.StatusMultiStatus) // 207 MultiStatus
	_, _ = w.Write(sb.Bytes())
}

func (h *Handler) handleGet(current *manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	if current.IsDir() {
		http.Error(w, "Bad Request: Cannot GET a directory", http.StatusBadRequest)
		return
	}
	h.handleDownload(current, w, r)
}

func (h *Handler) handleDelete(current *manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	if err := h.manager.RemoveEntry(current); err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent) // 204 No Content
}

func (h *Handler) handleHead(entry *manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", utils.GetContentType(entry.Name()))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", entry.Size()))
	w.Header().Set("Last-Modified", entry.ModTime().UTC().Format(http.TimeFormat))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleCopy(current *manager.FileInfo, w http.ResponseWriter, r *http.Request, delete bool) {
	destHeader := r.Header.Get("Destination")
	if destHeader == "" {
		http.Error(w, "Bad Request: Missing Destination header", http.StatusBadRequest)
		return
	}
	destPath := path.Clean(destHeader)
	err := h.manager.CopyEntry(current, destPath, delete)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated) // 201 Created
}

func (h *Handler) handleOptions(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, MKCOL, COPY, MOVE, PROPFIND")
	w.Header().Set("DAV", "1, 2")
	w.WriteHeader(http.StatusOK)
}

func (h *Handler) handleDownload(info *manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	etag := fmt.Sprintf("\"%x-%x\"", info.ModTime().Unix(), info.Size())
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))

	ext := filepath.Ext(info.Name())
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	if !info.IsRemote() {
		// Write .Content disposition for local files
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename*=UTF-8''%s", utils.PathUnescape(info.Name())))
		_, _ = w.Write(info.Content())
		return
	}

	entry, err := h.manager.GetEntryByName(info.Parent(), info.Name())
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if entry == nil {
		http.Error(w, "File Not Found", http.StatusNotFound)
		return
	}

	if err := h.StreamResponse(entry, info, w, r); err != nil {
		// Use the file path as key for rate limiting - same file error logged once per 30s
		logKey := fmt.Sprintf("%s/%s", info.Parent(), info.Name())

		var streamErr *customerror.Error
		if errors.As(err, &streamErr) {
			if !streamErr.HeadersWritten {
				http.Error(w, streamErr.Error(), streamErr.StatusCode())
			}
			if !streamErr.IsSilent() {
				h.logger.Rate(logKey).Error().Err(err).Msgf("Error streaming file: %s", logKey)
			}
			return
		}

		// Generic error - only write if we haven't started the response
		if !customerror.IsSilentError(err) {
			h.logger.Rate(logKey).Error().Err(err).Msgf("Error streaming file: %s", logKey)
		}
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

}
