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
	"reflect"
	"strings"
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
		HttpOnly: false,
	}

	statsCollector := stats.New(mgr)

	s := &Server{
		logger:       l,
		manager:      mgr,
		stats:        statsCollector,
		cookie:       cookieStore,
		templates:    templates,
		nzbUserAgent: cfg.NZBUserAgent,
		urlBase:      cfg.URLBase,
	}

	qb := qbit.New(mgr)
	sb := sabnzbd.New(mgr)
	wd := webdav.NewHandler(mgr)

	routes := make(map[string]http.Handler)
	routes["/api/v2"] = qb.Routes()

	if !wd.IsDisabled() {
		routes["/webdav"] = wd.Routes()
	}
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

			//logs
			r.Get("/logs", s.getLogs) // deprecated, use /debug/logs

			r.Route("/debug", func(r chi.Router) {
				r.Get("/stats", s.stats.Handler())
				r.Post("/speedtest", s.handleSpeedTest)
				r.Get("/logs", s.getLogs)
				r.Get("/logs/rclone", s.getRcloneLogs)
				r.Get("/ingests", s.handleIngests)
				r.Get("/ingests/{debrid}", s.handleIngestsByDebrid)
			})
		})

		//webhooks
		r.Post("/webhooks/tautulli", s.handleTautulli)
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

func (s *Server) ApplyRuntime(cfg *config.Config) error {
	if cfg == nil {
		return nil
	}
	current := config.Get()
	notificationsChanged := !reflect.DeepEqual(current.Notifications, cfg.Notifications)
	repairChanged := !reflect.DeepEqual(current.Repair, cfg.Repair)

	current.ApplyRuntime(cfg)
	s.nzbUserAgent = current.NZBUserAgent

	if notificationsChanged && s.manager != nil && s.manager.Notifications != nil {
		s.manager.Notifications.Reload(&current.Notifications)
	}
	if repairChanged && s.manager != nil {
		if svc := s.manager.Repair(); svc != nil {
			if err := svc.ApplyConfig(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Server) Start(ctx context.Context) error {
	cfg := config.Get()

	// Start background stats collector
	s.stats.Start(ctx)

	addr := fmt.Sprintf("%s:%s", cfg.BindAddress, cfg.Port)
	s.logger.Info().Msgf("Starting server on %s%s", addr, cfg.URLBase)
	srv := &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error().Err(err).Msgf("Error starting server")
		}
	}()

	<-ctx.Done()
	s.logger.Info().Msg("Shutting down gracefully...")
	return srv.Shutdown(context.Background())
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
