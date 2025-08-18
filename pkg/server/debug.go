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
	debridStats := make([]debridTypes.Stats, 0)
	for debridName, client := range clients {
		debridStat := debridTypes.Stats{}
		libraryStat := debridTypes.LibraryStats{}
		profile, err := client.GetProfile()
		if err != nil {
			s.logger.Error().Err(err).Str("debrid", debridName).Msg("Failed to get debrid profile")
			profile = &debridTypes.Profile{
				Name: debridName,
			}
		}
		profile.Name = debridName
		debridStat.Profile = profile
		cache, ok := caches[debridName]
		if ok {
			// Get torrent data
			libraryStat.Total = cache.TotalTorrents()
			libraryStat.Bad = len(cache.GetListing("__bad__"))
			libraryStat.ActiveLinks = cache.GetTotalActiveDownloadLinks()

		}
		debridStat.Library = libraryStat
		
		// Get detailed account information
		accounts := client.Accounts().All()
		accountDetails := make([]map[string]any, 0)
		for _, account := range accounts {
			// Mask token - show first 8 characters and last 4 characters
			maskedToken := ""
			if len(account.Token) > 12 {
				maskedToken = account.Token[:8] + "****" + account.Token[len(account.Token)-4:]
			} else if len(account.Token) > 8 {
				maskedToken = account.Token[:4] + "****" + account.Token[len(account.Token)-2:]
			} else {
				maskedToken = "****"
			}
			
			accountDetail := map[string]any{
				"order":        account.Order,
				"disabled":     account.Disabled,
				"token_masked": maskedToken,
				"username":     account.Username,
				"traffic_used": account.TrafficUsed,
				"links_count":  account.LinksCount(),
				"debrid":       account.Debrid,
			}
			accountDetails = append(accountDetails, accountDetail)
		}
		debridStat.Accounts = accountDetails
		debridStats = append(debridStats, debridStat)
	}
	stats["debrids"] = debridStats

	// Add rclone stats if available
	if rcManager := store.Get().RcloneManager(); rcManager != nil && rcManager.IsReady() {
		rcStats, err := rcManager.GetStats()
		if err != nil {
			s.logger.Error().Err(err).Msg("Failed to get rclone stats")
			stats["rclone"] = map[string]interface{}{
				"enabled":      true,
				"server_ready": false,
			}
		} else {
			stats["rclone"] = rcStats
		}
	} else {
		stats["rclone"] = map[string]interface{}{
			"enabled":      false,
			"server_ready": false,
		}
	}

	request.JSONResponse(w, stats, http.StatusOK)
}
