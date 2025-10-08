package web

import (
	"io/fs"
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (wb *Web) Routes() http.Handler {
	r := chi.NewRouter()

	// Static assets - always public
	staticFS, _ := fs.Sub(assetsEmbed, "assets/build")
	imagesFS, _ := fs.Sub(imagesEmbed, "assets/images")
	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(staticFS))))
	r.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.FS(imagesFS))))

	// Public routes - no auth needed
	r.Get("/version", wb.handleGetVersion)
	r.Get("/login", wb.LoginHandler)
	r.Post("/login", wb.LoginHandler)
	r.Get("/register", wb.RegisterHandler)
	r.Post("/register", wb.RegisterHandler)
	r.Post("/skip-auth", wb.skipAuthHandler)

	// Protected routes - require auth
	r.Group(func(r chi.Router) {
		r.Use(wb.authMiddleware)
		// Web pages
		r.Get("/", wb.IndexHandler)
		r.Get("/download", wb.DownloadHandler)
		r.Get("/repair", wb.RepairHandler)
		r.Get("/stats", wb.StatsHandler)
		r.Get("/settings", wb.ConfigHandler)

		// API routes
		r.Route("/api", func(r chi.Router) {
			// Arr management
			r.Get("/arrs", wb.handleGetArrs)
			r.Post("/add", wb.handleAddContent)

			// Repair operations
			r.Post("/repair", wb.handleRepairMedia)
			r.Get("/repair/jobs", wb.handleGetRepairJobs)
			r.Post("/repair/jobs/{id}/process", wb.handleProcessRepairJob)
			r.Post("/repair/jobs/{id}/stop", wb.handleStopRepairJob)
			r.Delete("/repair/jobs", wb.handleDeleteRepairJob)

			// Torrent management
			r.Get("/torrents", wb.handleGetTorrents)
			r.Delete("/torrents/{category}/{hash}", wb.handleDeleteTorrent)
			r.Delete("/torrents", wb.handleDeleteTorrents) // Fixed trailing slash

			// Config/Auth
			r.Get("/config", wb.handleGetConfig)
			r.Post("/config", wb.handleUpdateConfig)
			r.Post("/refresh-token", wb.handleRefreshAPIToken)
			r.Post("/update-auth", wb.handleUpdateAuth)
		})
	})

	return r
}
