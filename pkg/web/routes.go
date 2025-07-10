package web

import (
	"github.com/go-chi/chi/v5"
	"io/fs"
	"net/http"
)

func (wb *Web) Routes() http.Handler {
	r := chi.NewRouter()

	// Load static files from embedded filesystem
	staticFS, err := fs.Sub(assetsEmbed, "assets/build")
	if err != nil {
		panic(err)
	}
	imagesFS, err := fs.Sub(imagesEmbed, "assets/images")
	if err != nil {
		panic(err)
	}

	r.Handle("/assets/*", http.StripPrefix("/assets/", http.FileServer(http.FS(staticFS))))
	r.Handle("/images/*", http.StripPrefix("/images/", http.FileServer(http.FS(imagesFS))))

	r.Get("/login", wb.LoginHandler)
	r.Post("/login", wb.LoginHandler)
	r.Get("/register", wb.RegisterHandler)
	r.Post("/register", wb.RegisterHandler)
	r.Get("/skip-auth", wb.skipAuthHandler)
	r.Get("/version", wb.handleGetVersion)

	r.Group(func(r chi.Router) {
		r.Use(wb.authMiddleware)
		r.Use(wb.setupMiddleware)
		r.Get("/", wb.IndexHandler)
		r.Get("/download", wb.DownloadHandler)
		r.Get("/repair", wb.RepairHandler)
		r.Get("/config", wb.ConfigHandler)
		r.Route("/api", func(r chi.Router) {
			r.Get("/arrs", wb.handleGetArrs)
			r.Post("/add", wb.handleAddContent)
			r.Post("/repair", wb.handleRepairMedia)
			r.Get("/repair/jobs", wb.handleGetRepairJobs)
			r.Post("/repair/jobs/{id}/process", wb.handleProcessRepairJob)
			r.Post("/repair/jobs/{id}/stop", wb.handleStopRepairJob)
			r.Delete("/repair/jobs", wb.handleDeleteRepairJob)
			r.Get("/torrents", wb.handleGetTorrents)
			r.Delete("/torrents/{category}/{hash}", wb.handleDeleteTorrent)
			r.Delete("/torrents/", wb.handleDeleteTorrents)
			r.Get("/config", wb.handleGetConfig)
			r.Post("/config", wb.handleUpdateConfig)
		})
	})

	return r
}
