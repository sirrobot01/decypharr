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
	"github.com/sirrobot01/decypharr/internal/request"
	"io"
	"net/http"
	"net/url"
	"os"
	"runtime"
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

		//stats
		r.Get("/stats", s.getStats)

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
			s.logger.Info().Msgf("Error starting server: %v", err)
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

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	stats := map[string]interface{}{
		// Memory stats
		"heap_alloc_mb":  fmt.Sprintf("%.2fMB", float64(memStats.HeapAlloc)/1024/1024),
		"total_alloc_mb": fmt.Sprintf("%.2fMB", float64(memStats.TotalAlloc)/1024/1024),
		"memory_used":    fmt.Sprintf("%.2fMB", float64(memStats.Sys)/1024/1024),

		// GC stats
		"gc_cycles": memStats.NumGC,
		// Goroutine stats
		"goroutines": runtime.NumGoroutine(),

		// System info
		"num_cpu": runtime.NumCPU(),

		// OS info
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"go_version": runtime.Version(),
	}
	request.JSONResponse(w, stats, http.StatusOK)
}
