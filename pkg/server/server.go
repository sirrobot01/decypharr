package server

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/gorilla/sessions"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/server/qbit"
	"github.com/sirrobot01/decypharr/pkg/server/sabnzbd"
	"github.com/sirrobot01/decypharr/pkg/server/webdav"
	"github.com/sirrobot01/decypharr/pkg/stats"
)

//go:embed templates/*
var content embed.FS

//go:embed assets/build/*
var assetsEmbed embed.FS

//go:embed assets/images/*
var imagesEmbed embed.FS

type AddRequest struct {
	Url        string   `json:"url"`
	Arr        string   `json:"arr"`
	File       string   `json:"file"`
	NotSymlink bool     `json:"notSymlink"`
	Content    string   `json:"content"`
	Seasons    []string `json:"seasons"`
	Episodes   []string `json:"episodes"`
}

type ArrResponse struct {
	Name string `json:"name"`
	Url  string `json:"url"`
}

type ContentResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Type  string `json:"type"`
	ArrID string `json:"arr"`
}

// shutdownGrace is how long Start waits for in-flight requests to finish
// before it forces the remaining connections closed. It has to be long enough
// for ordinary API requests to complete and short enough that a restart does
// not read as an outage to whoever triggered it.
const shutdownGrace = 5 * time.Second

// handlerGrace is how long Start waits, after cutting the connections, for
// the handlers on them to unwind before it lets the caller tear down what
// they were using.
//
// Generous on purpose. Handlers that touch their connection die on the next
// read or write, so the ones this is really about - a WebDAV read - are gone
// in milliseconds and never come near this. What is left is a handler off
// doing something that ignores its connection entirely: utils.DownloadFile,
// say, which takes neither a timeout nor the request context. Returning while
// one of those is still running lets the caller reset the manager out from
// under it, so prefer to wait. The cap is only here so such a handler cannot
// hold the restart open indefinitely - which is the bug this whole change is
// about - and reaching it is reported rather than passed over.
const handlerGrace = 30 * time.Second

type Server struct {
	router       *chi.Mux
	logger       zerolog.Logger
	manager      *manager.Manager
	stats        *stats.Collector
	cookie       *sessions.CookieStore
	templates    *template.Template
	nzbUserAgent string
	urlBase      string
	restartFunc  func()

	// instanceID identifies this Server, and so the run of the service it
	// belongs to. A restart builds a fresh Server, so a client that watched
	// the value change knows the new listener is up rather than guessing
	// from a timer. See handleGetVersion.
	instanceID string

	// inflight counts handlers that have been entered and not yet returned.
	// http.Server.Close does not wait for those, so Start uses this to wait
	// for them itself before the caller tears down what they are using.
	inflight sync.WaitGroup
}

// trackInflight records a handler for the duration of its run, so that a
// forced shutdown can still wait for handlers to unwind.
func (s *Server) trackInflight(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.inflight.Add(1)
		defer s.inflight.Done()
		next.ServeHTTP(w, r)
	})
}

func New(mgr *manager.Manager) *Server {
	l := logger.New("http")
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.StripSlashes)
	r.Use(middleware.RedirectSlashes)

	cfg := config.Get()

	templates := template.Must(template.ParseFS(
		content,
		"templates/layout.html",
		"templates/setup_layout.html",
		"templates/index.html",
		"templates/download.html",
		"templates/repair.html",
		"templates/reacquire.html",
		"templates/repair_tabs.html",
		"templates/stats.html",
		"templates/config.html",
		"templates/browse.html",
		"templates/login.html",
		"templates/register.html",
		"templates/setup.html",
	))
	cookieStore := sessions.NewCookieStore([]byte(cfg.SecretKey()))
	cookieStore.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400 * 7,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	statsCollector := stats.New(mgr)

	s := &Server{
		logger:     l,
		manager:    mgr,
		stats:      statsCollector,
		cookie:     cookieStore,
		templates:  templates,
		urlBase:    cfg.URLBase,
		instanceID: strconv.FormatInt(time.Now().UnixNano(), 36),
	}

	qb := qbit.New(mgr)
	sb := sabnzbd.New(mgr)
	wd := webdav.NewHandler(mgr)

	routes := make(map[string]http.Handler)
	routes["/api/v2"] = qb.Routes()

	if !wd.IsDisabled() {
		routes["/webdav"] = wd.Routes()
	}
	// Serves the URLs written into .strm files; independent of DisableWebDav.
	routes["/stream"] = wd.StreamRoutes()
	routes["/sabnzbd"] = sb.Routes()

	// Trim trailing slash so chi registers the URLBase root path itself
	routePath := cfg.URLBase
	if routePath != "/" {
		routePath = strings.TrimSuffix(routePath, "/")
	}
	r.Route(routePath, func(r chi.Router) {
		// Mount web routes
		r.Mount("/", s.WebRoutes())

		for path, handler := range routes {
			r.Mount(path, handler)
		}

		r.Group(func(r chi.Router) {
			r.Use(s.authMiddleware)

			// logs
			r.Get("/logs", s.getLogs) // deprecated, use /debug/logs

			r.Route("/debug", func(r chi.Router) {
				r.Get("/stats", s.stats.Handler())
				r.Post("/speedtest", s.handleSpeedTest)
				r.Get("/logs", s.getLogs)
				r.Get("/logs/rclone", s.getRcloneLogs)
				r.Get("/ingests", s.handleIngests)
				r.Get("/ingests/{debrid}", s.handleIngestsByDebrid)
			})

			// Webhooks
			r.Post("/webhooks/tautulli", s.handleTautulli)
		})
	})
	s.router = r
	return s
}

func (s *Server) SetRestartFunc(restartFunc func()) {
	s.restartFunc = restartFunc
}

func (s *Server) Restart() {
	if s.restartFunc != nil {
		time.Sleep(200 * time.Millisecond)
		s.restartFunc()
	} else {
		s.logger.Warn().Msg("Restart function not set")
	}
}

func (s *Server) Start(ctx context.Context) error {
	cfg := config.Get()

	// Start background stats collector
	s.stats.Start(ctx)

	addr := fmt.Sprintf("%s:%s", cfg.BindAddress, cfg.Port)
	s.logger.Info().Msgf("Starting server on %s%s", addr, cfg.URLBase)
	srv := &http.Server{
		Addr:    addr,
		Handler: s.trackInflight(s.router),
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error().Err(err).Msgf("Error starting server")
		}
	}()

	<-ctx.Done()
	s.logger.Info().Msg("Shutting down gracefully...")

	// Bound the drain. Shutdown closes the listeners straight away but then
	// waits for every in-flight request to finish, and a WebDAV read is in
	// flight for as long as the player is streaming the file. On a config
	// save the caller blocks on this before it can bind the next listener,
	// so an unbounded drain leaves the UI answering nothing (a bad gateway
	// behind a reverse proxy) for the length of somebody's movie. The
	// restart tears down the debrid clients those reads depend on anyway,
	// so cut them once ordinary requests have had time to land.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.logger.Warn().Err(err).Dur("grace", shutdownGrace).
			Msg("Requests still in flight after the shutdown grace period; closing them")
		closeErr := srv.Close()
		// Close cuts the connections but returns without waiting for the
		// handlers behind them, and the caller goes on to tear down the
		// manager those handlers are still reading and writing. Most unwind
		// as soon as their connection goes (the next read or write fails),
		// so wait for them, bounded: a handler stuck somewhere other than
		// its connection must not hold the restart open again.
		if !s.waitForHandlers(handlerGrace) {
			s.logger.Warn().Dur("grace", handlerGrace).
				Msg("Handlers still running after their connections were closed; continuing")
		}
		return closeErr
	}
	return nil
}

// waitForHandlers blocks until every tracked handler has returned, or until
// grace expires. It reports whether they all returned.
func (s *Server) waitForHandlers(grace time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.inflight.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		return false
	}
}

func (s *Server) getLogs(w http.ResponseWriter, r *http.Request) {
	logFile := filepath.Join(logger.GetLogPath(), "decypharr.log")

	// Open and read the file
	file, err := os.Open(logFile)
	if err != nil {
		http.Error(w, "Error reading log file", http.StatusInternalServerError)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			s.logger.Error().Err(err).Msg("Error closing log file")
		}
	}(file)

	// Set headers
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=application.log")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	// Stream the file
	if _, err := io.Copy(w, file); err != nil {
		http.Error(w, "Error streaming log file", http.StatusInternalServerError)
		return
	}
}

func (s *Server) getRcloneLogs(w http.ResponseWriter, r *http.Request) {
	// Rclone logs resides in the same directory as the application logs
	logFile := filepath.Join(logger.GetLogPath(), "rclone.log")
	// Open and read the file
	file, err := os.Open(logFile)
	if err != nil {
		http.Error(w, "Error reading log file", http.StatusInternalServerError)
		return
	}
	defer func(file *os.File) {
		err := file.Close()
		if err != nil {
			return
		}
	}(file)

	// Set headers
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "inline; filename=application.log")
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	// Stream the file
	if _, err := io.Copy(w, file); err != nil {
		http.Error(w, fmt.Sprintf("error stremaing file %s", err), http.StatusInternalServerError)
		return
	}
}
