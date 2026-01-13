package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
)

type Server struct {
	router *chi.Mux
	logger zerolog.Logger
}

func New(handlers map[string]http.Handler) *Server {
	l := logger.New("http")
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Use(middleware.StripSlashes)
	r.Use(middleware.RedirectSlashes)

	cfg := config.Get()

	s := &Server{
		logger: l,
	}

	// URLBase is normalized to have trailing slash, but StripSlashes middleware
	// removes it from requests, so we need to match without trailing slash
	urlBase := cfg.URLBase
	if urlBase != "/" {
		urlBase = strings.TrimSuffix(urlBase, "/")
	}
	r.Route(urlBase, func(r chi.Router) {
		for pattern, handler := range handlers {
			r.Mount(pattern, handler)
		}

		//logs
		r.Get("/logs", s.getLogs) // deprecated, use /debug/logs

		r.Route("/debug", func(r chi.Router) {
			r.Get("/stats", s.handleStats)
			r.Get("/logs", s.getLogs)
			r.Get("/logs/rclone", s.getRcloneLogs)
			r.Get("/ingests", s.handleIngests)
			r.Get("/ingests/{debrid}", s.handleIngestsByDebrid)
		})

		//webhooks
		r.Post("/webhooks/tautulli", s.handleTautulli)

	})
	s.router = r
	return s
}

func (s *Server) Start(ctx context.Context) error {
	cfg := config.Get()

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
		s.logger.Error().Err(err).Msg("Error streaming log file")
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
			s.logger.Error().Err(err).Msg("Error closing log file")
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
		s.logger.Error().Err(err).Msg("Error streaming log file")
		http.Error(w, "Error streaming log file", http.StatusInternalServerError)
		return
	}
}
