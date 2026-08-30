package webdav

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/strm"
)

// StreamRoutes returns the identity-addressed stream router mounted at
// /stream. It serves the URLs written into .strm files and is independent of
// the WebDAV surface: GET/HEAD only, no WebDAV middleware.
func (h *Handler) StreamRoutes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.readinessMiddleware)
	r.Get("/{infohash}/{fileID}/{name}", h.handleStream)
	r.Head("/{infohash}/{fileID}/{name}", h.handleStream)
	return r
}

func (h *Handler) handleStream(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	infohash := chi.URLParam(r, "infohash")
	fileID := chi.URLParam(r, "fileID")

	// While WebDAV auth is on the route stays authenticated via the URL
	// signature; Basic auth is accepted as a curl/debug fallback. Signatures
	// are always written, so enabling auth later breaks nothing.
	if cfg.UseAuth && cfg.EnableWebdavAuth &&
		!strm.Verify(cfg.Strm.Secret, infohash, fileID, r.URL.Query().Get("s")) {
		if user, pass, ok := r.BasicAuth(); !ok || !config.VerifyAuth(user, pass) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	entry, err := h.manager.GetEntry(infohash)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	file, err := entry.GetFileByID(fileID)
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}

	// Identity + size: stable across restarts and renames.
	etag := fmt.Sprintf("\"%s-%s-%d\"", infohash, fileID, file.Size)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Content-Type", utils.GetContentType(file.Name))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Last-Modified", file.AddedOn.UTC().Format(http.TimeFormat))

	// ffprobe opens a file several times before playback; HEAD answers from
	// storage alone and must never trigger upstream work.
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Length", strconv.FormatInt(file.Size, 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Redirect mode hands the player the provider link so bytes bypass us
	// entirely. Usenet entries have no upstream URL and always proxy; a link
	// failure falls back to proxying this request.
	if cfg.Strm.DeliveryMode == config.StrmDeliveryRedirect && entry.IsTorrent() {
		if dl, err := h.manager.GetDownloadLink(r.Context(), entry, file.Name); err == nil && dl.DownloadLink != "" {
			http.Redirect(w, r, dl.DownloadLink, http.StatusFound)
			return
		} else if err != nil {
			h.logger.Rate(infohash+"/"+fileID).Warn().Err(err).
				Msgf("stream redirect link failed, proxying: %s", file.Name)
		}
	}

	if err := h.StreamResponse(entry, file.Name, file.Size, w, r); err != nil {
		h.writeStreamError(fmt.Sprintf("%s/%s", infohash, file.Name), err, w)
	}
}
