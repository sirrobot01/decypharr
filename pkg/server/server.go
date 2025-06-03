package server

import (
	"context"
	"errors"
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"io"
	"net/http"
	"net/url"
	"os"
)

type Server struct {
	router *chi.Mux
	logger zerolog.Logger
}

func New(handlers map[string]http.Handler) *Server {
	l := logger.New("http")
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	cfg := config.Get()

	s := &Server{
		logger: l,
	}
	staticPath, _ := url.JoinPath(cfg.URLBase, "static")
	r.Handle(staticPath+"/*",
		http.StripPrefix(staticPath, http.FileServer(http.Dir("static"))),
	)

	r.Route(cfg.URLBase, func(r chi.Router) {
		for pattern, handler := range handlers {
			r.Mount(pattern, handler)
		}

		//logs
		r.Get("/logs", s.getLogs)

		//debugs
		r.Route("/debug", func(r chi.Router) {
			r.Get("/stats", s.handleStats)
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
	logFile := logger.GetLogPath()

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
	_, err = io.Copy(w, file)
	if err != nil {
		s.logger.Error().Err(err).Msg("Error streaming log file")
		http.Error(w, "Error streaming log file", http.StatusInternalServerError)
		return
	}
}
