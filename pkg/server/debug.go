package server

import (
	"fmt"
	"github.com/go-chi/chi/v5"
	"github.com/sirrobot01/decypharr/internal/request"
	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/store"
	"net/http"
	"runtime"
)

func (s *Server) handleIngests(w http.ResponseWriter, r *http.Request) {
	ingests := make([]debridTypes.IngestData, 0)
	_store := store.Get()
	debrids := _store.Debrid()
	if debrids == nil {
		http.Error(w, "Debrid service is not enabled", http.StatusInternalServerError)
		return
	}
	for _, cache := range debrids.Caches() {
		if cache == nil {
			s.logger.Error().Msg("Debrid cache is nil, skipping")
			continue
		}
		data, err := cache.GetIngests()
		if err != nil {
			s.logger.Error().Err(err).Msg("Failed to get ingests from debrid cache")
			http.Error(w, "Failed to get ingests: "+err.Error(), http.StatusInternalServerError)
			return
		}
		ingests = append(ingests, data...)
	}

	request.JSONResponse(w, ingests, 200)
}

func (s *Server) handleIngestsByDebrid(w http.ResponseWriter, r *http.Request) {
	debridName := chi.URLParam(r, "debrid")
	if debridName == "" {
		http.Error(w, "Debrid name is required", http.StatusBadRequest)
		return
	}

	_store := store.Get()
	debrids := _store.Debrid()

	if debrids == nil {
		http.Error(w, "Debrid service is not enabled", http.StatusInternalServerError)
		return
	}

	caches := debrids.Caches()

	cache, exists := caches[debridName]
	if !exists {
		http.Error(w, "Debrid cache not found: "+debridName, http.StatusNotFound)
		return
	}

	data, err := cache.GetIngests()
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to get ingests from debrid cache")
		http.Error(w, "Failed to get ingests: "+err.Error(), http.StatusInternalServerError)
		return
	}

	request.JSONResponse(w, data, 200)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	stats := map[string]any{
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

	debrids := store.Get().Debrid()
	if debrids == nil {
		request.JSONResponse(w, stats, http.StatusOK)
		return
	}
	clients := debrids.Clients()
	caches := debrids.Caches()
	profiles := make([]*debridTypes.Profile, 0)
	for debridName, client := range clients {
		profile, err := client.GetProfile()
		profile.Name = debridName
		if err != nil {
			s.logger.Error().Err(err).Msg("Failed to get debrid profile")
			continue
		}
		cache, ok := caches[debridName]
		if ok {
			// Get torrent data
			profile.LibrarySize = len(cache.GetTorrents())
			profile.BadTorrents = len(cache.GetListing("__bad__"))
			profile.ActiveLinks = cache.GetTotalActiveDownloadLinks()

		}
		profiles = append(profiles, profile)
	}
	stats["debrids"] = profiles
	request.JSONResponse(w, stats, http.StatusOK)
}
