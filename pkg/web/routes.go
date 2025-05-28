package web

import (
	"github.com/go-chi/chi/v5"
	"net/http"
)

func (ui *Handler) Routes() http.Handler {
	r := chi.NewRouter()

	r.Get("/login", ui.LoginHandler)
	r.Post("/login", ui.LoginHandler)
	r.Get("/register", ui.RegisterHandler)
	r.Post("/register", ui.RegisterHandler)
	r.Get("/skip-auth", ui.skipAuthHandler)
	r.Get("/version", ui.handleGetVersion)

	r.Group(func(r chi.Router) {
		r.Use(ui.authMiddleware)
		r.Use(ui.setupMiddleware)
		r.Get("/", ui.IndexHandler)
		r.Get("/download", ui.DownloadHandler)
		r.Get("/repair", ui.RepairHandler)
		r.Get("/config", ui.ConfigHandler)
		r.Route("/api", func(r chi.Router) {
			r.Get("/arrs", ui.handleGetArrs)
			r.Post("/add", ui.handleAddContent)
			r.Post("/repair", ui.handleRepairMedia)
			r.Get("/repair/jobs", ui.handleGetRepairJobs)
			r.Post("/repair/jobs/{id}/process", ui.handleProcessRepairJob)
			r.Delete("/repair/jobs", ui.handleDeleteRepairJob)
			r.Get("/torrents", ui.handleGetTorrents)
			r.Delete("/torrents/{category}/{hash}", ui.handleDeleteTorrent)
			r.Delete("/torrents/", ui.handleDeleteTorrents)
			r.Get("/config", ui.handleGetConfig)
			r.Post("/config", ui.handleUpdateConfig)
		})
	})

	return r
}
