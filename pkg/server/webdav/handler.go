package webdav

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

func init() {
	chi.RegisterMethod("PROPFIND")
	chi.RegisterMethod("PROPPATCH")
	chi.RegisterMethod("MKCOL")
	chi.RegisterMethod("COPY")
	chi.RegisterMethod("MOVE")
	chi.RegisterMethod("LOCK")
	chi.RegisterMethod("UNLOCK")
}

const (
	PROPFIND = "PROPFIND"
)

type Handler struct {
	logger  *logger.RateLimitedLogger
	manager *manager.Manager
}

func NewHandler(mgr *manager.Manager) *Handler {
	log := logger.NewRateLimitedLogger(logger.WithLogger(logger.New("webdav")))
	h := &Handler{
		logger:  log,
		manager: mgr,
	}
	return h
}

func (h *Handler) readinessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-h.manager.IsReady():
			// WebDAV is ready, proceed
			next.ServeHTTP(w, r)
		default:
			// WebDAV is still initializing
			w.Header().Set("Retry-After", "5")
			http.Error(w, "WebDAV service is initializing, please try again shortly", http.StatusServiceUnavailable)
		}
	})
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(h.readinessMiddleware)
	r.Use(h.commonMiddleware)
	r.Use(middleware.AllowContentEncoding("gzip"))
	// Always install the auth middleware; whether it actually enforces auth is
	// decided live per-request from config, so toggling UseAuth/EnableWebdavAuth
	// takes effect without rebuilding the router (no restart).
	r.Use(h.authMiddleware)

	r.HandleFunc("/", h.handleRoot)
	r.HandleFunc("/{group}", h.handleGroup)
	r.HandleFunc("/{group}/{torrent}", h.handleTorrentFolder)
	r.HandleFunc("/{group}/{torrent}/{file}", h.handleTorrentFile)
	r.HandleFunc("/stream/{group}/{torrent}/{file}", h.handleTorrentFile)
	return r
}

func (h *Handler) IsDisabled() bool {
	cfg := config.Get()
	return cfg.DisableWebDav
}

func (h *Handler) handler(current *manager.FileInfo, children []manager.FileInfo, w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "HEAD":
		h.handleHead(current, w, r)
	case "GET":
		if current == nil {
			http.Error(w, "Not Found", http.StatusNotFound)
			return
		}
		h.handleGet(current, w, r)
	case "DELETE":
		h.handleDelete(current, w, r)
	case PROPFIND:
		h.handlePropfind(current, children, w, r)
	case "COPY":
		h.handleCopy(current, w, r, false)
	case "OPTIONS":
		h.handleOptions(w, r)
	case "MOVE":
		h.handleCopy(current, w, r, true)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	current := h.manager.RootInfo()
	children := h.manager.GetEntries()
	h.handler(current, children, w, r)
}

func (h *Handler) handleGroup(w http.ResponseWriter, r *http.Request) {
	group := utils.PathUnescape(chi.URLParam(r, "group"))
	currentInfo, rawEntries := h.manager.GetEntryChildren(group)
	h.handler(currentInfo, rawEntries, w, r)

}

func (h *Handler) handleTorrentFolder(w http.ResponseWriter, r *http.Request) {
	torrent := utils.PathUnescape(chi.URLParam(r, "torrent"))

	currentInfo, children := h.manager.GetTorrentChildren(torrent)
	h.handler(currentInfo, children, w, r)
}

func (h *Handler) handleTorrentFile(w http.ResponseWriter, r *http.Request) {
	torrent := utils.PathUnescape(chi.URLParam(r, "torrent"))
	file := utils.PathUnescape(chi.URLParam(r, "file"))
	currentInfo, err := h.manager.GetTorrentFile(torrent, file)
	if err != nil || currentInfo == nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	h.handler(currentInfo, nil, w, r)
}

func (h *Handler) commonMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("DAV", "1, 2")
		w.Header().Set("Allow", "OPTIONS, PROPFIND GET, HEAD, POST, PUT, DELETE, MKCOL, PROPPATCH, COPY, MOVE, LOCK, UNLOCK")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "OPTIONS, GET, PROPFIND, HEAD, POST, PUT, DELETE, MKCOL, PROPPATCH, COPY, MOVE, LOCK, UNLOCK")
		w.Header().Set("Access-Control-Allow-Headers", "Depth, Content-Type, Authorization")

		next.ServeHTTP(w, r)
	})
}

func (h *Handler) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Read the auth toggles live so changes apply without a restart.
		cfg := config.Get()
		if !cfg.UseAuth || !cfg.EnableWebdavAuth {
			next.ServeHTTP(w, r)
			return
		}

		if h.isInternalBearer(r) {
			next.ServeHTTP(w, r)
			return
		}

		username, password, ok := r.BasicAuth()
		if !ok || !config.VerifyAuth(username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// isInternalBearer accepts the manager's random, in-memory, per-process token
// as an alternative to the WebDAV username/password. It exists for the repair
// sweep's ffprobe check: the configured WebDAV password is only ever stored
// as a bcrypt hash, so there's no plaintext credential available to hand to
// an external ffprobe process. The token is regenerated on every restart and
// never persisted, so this bypass has a lifetime and blast radius bounded to
// the current process.
func (h *Handler) isInternalBearer(r *http.Request) bool {
	token := h.manager.InternalToken()
	if token == "" {
		return false
	}
	after, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(after), []byte(token)) == 1
}
