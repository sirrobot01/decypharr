package qbit

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (q *QBit) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(q.categoryContext)
	r.Group(func(r chi.Router) {
		r.Post("/auth/login", q.handleLogin)
		r.Route("/torrents", func(r chi.Router) {
			r.Use(q.authContext)
			r.Use(hashesContext)

			r.Get("/info", q.handleTorrentsInfo)
			r.Post("/info", q.handleTorrentsInfo)

			r.Post("/add", q.handleTorrentsAdd)
			r.Post("/delete", q.handleTorrentsDelete)

			r.Get("/categories", q.handleCategories)
			r.Post("/categories", q.handleCategories)

			r.Post("/createCategory", q.handleCreateCategory)
			r.Post("/setCategory", q.handleSetCategory)
			r.Post("/addTags", q.handleAddTorrentTags)
			r.Post("/removeTags", q.handleRemoveTorrentTags)
			r.Post("/createTags", q.handleCreateTags)

			r.Get("/tags", q.handleGetTags)
			r.Get("/pause", q.handleTorrentsPause)
			r.Get("/resume", q.handleTorrentsResume)
			r.Get("/recheck", q.handleTorrentRecheck)
			r.Get("/properties", q.handleTorrentProperties)
			r.Get("/files", q.handleTorrentFiles)

			// Create POST equivalents for pause, resume, recheck
			r.Post("/tags", q.handleGetTags)
			r.Post("/pause", q.handleTorrentsPause)
			r.Post("/resume", q.handleTorrentsResume)
			r.Post("/recheck", q.handleTorrentRecheck)
			r.Post("/properties", q.handleTorrentProperties)
			r.Post("/files", q.handleTorrentFiles)

		})

		r.Route("/app", func(r chi.Router) {
			r.Get("/version", q.handleVersion)
			r.Get("/webapiVersion", q.handleWebAPIVersion)
			r.Get("/preferences", q.handlePreferences)
			r.Get("/buildInfo", q.handleBuildInfo)
			r.Get("/shutdown", q.handleShutdown)
		})
	})
	return r
}
