package server

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *Server) WebRoutes() http.Handler {
	r := chi.NewRouter()

	// Apply setup redirect middleware globally
	r.Use(s.setupRedirectMiddleware)

	// Static assets - always public
	staticFS, _ := fs.Sub(assetsEmbed, "assets/build")
	imagesFS, _ := fs.Sub(imagesEmbed, "assets/images")
	r.Handle("/assets/*", http.StripPrefix(s.urlBase+"assets/", http.FileServer(http.FS(staticFS))))
	r.Handle("/images/*", http.StripPrefix(s.urlBase+"images/", http.FileServer(http.FS(imagesFS))))

	// Public routes - no auth needed
	r.Get("/version", s.handleGetVersion)
	r.Get("/login", s.LoginHandler)
	r.Post("/login", s.LoginHandler)
	r.Get("/register", s.RegisterHandler)
	r.Post("/register", s.RegisterHandler)
	r.Post("/skip-auth", s.skipAuthHandler)

	// Setup wizard - public, no auth required
	r.Get("/setup", s.SetupHandler)
	r.Post("/api/setup/complete", s.setupCompleteHandler)

	// Protected routes - require auth
	r.Group(func(r chi.Router) {
		r.Use(s.authMiddleware)
		// Web pages
		r.Get("/", s.IndexHandler)
		r.Get("/browse", s.BrowseHandler)
		r.Get("/download", s.DownloadHandler)
		r.Get("/repair", s.RepairHandler)
		r.Get("/stats", s.StatsHandler)
		r.Get("/settings", s.ConfigHandler)

		// API routes
		r.Route("/api", func(r chi.Router) {
			// Arr management
			r.Get("/arrs", s.handleGetArrs)
			r.Post("/add", s.handleAddContent)

			// Repair / health-checker operations
			r.Get("/repair/config", s.handleGetRepairConfig)
			r.Put("/repair/config", s.handleUpdateRepairConfig)
			r.Get("/repair/status", s.handleRepairStatus)
			r.Post("/repair/run", s.handleRunRepair)
			r.Post("/repair/stop", s.handleStopRepair)
			r.Post("/repair/recheck/media", s.handleRecheckMedia)
			r.Post("/repair/fix", s.handleFixBroken)
			r.Post("/repair/clear", s.handleClearBroken)
			r.Post("/repair/clear-state", s.handleClearRepairState)
			r.Get("/repair/runs", s.handleListRepairRuns)
			r.Get("/repair/runs/{id}", s.handleGetRepairRun)
			r.Delete("/repair/runs", s.handleClearRepairRuns)
			r.Get("/repair/health", s.handleListEntryHealth)
			r.Get("/repair/health/{name}", s.handleGetEntryHealth)
			r.Post("/repair/health/{name}/check", s.handleRecheckEntry)

			// Torrent management
			r.Get("/torrents", s.handleGetTorrents)
			r.Delete("/torrents/{category}/{hash}", s.handleDeleteTorrent)
			r.Delete("/torrents", s.handleDeleteTorrents) // Fixed trailing slash

			// Browse - WebDAV-style hierarchical file browser
			r.Route("/browse", func(r chi.Router) {
				// Hierarchical browse endpoints
				r.Get("/", s.handleBrowseMount)                                    // Mount: groups (__all__, __bad__, etc.)
				r.Get("/{group}", s.handleBrowseGroup)                             // Group: torrents
				r.Get("/{group}/{subgroup}/{torrent}", s.handleBrowseTorrentFiles) // Torrent files (with subgroup)
				r.Get("/{group}/{torrent}", s.handleBrowseTorrentFiles)            // Torrent files (without subgroup) - This route needs to come after the subgroup route

				// Torrent operations
				r.Delete("/torrents/{id}", s.handleDeleteBrowseTorrent)
				r.Delete("/torrents/batch", s.handleBatchDeleteBrowseTorrents)

				// File download
				r.Get("/download/{torrent}/{file}", s.handleDownloadFile)
			})

			// Config/Auth
			r.Get("/config", s.handleGetConfig)
			r.Post("/config", s.handleUpdateConfig)
			r.Post("/refresh-token", s.handleRefreshAPIToken)
			r.Post("/update-auth", s.handleUpdateAuth)
		})
	})

	return r
}
