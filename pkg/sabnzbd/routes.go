package sabnzbd

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func (s *SABnzbd) Routes() http.Handler {
	r := chi.NewRouter()
	r.Use(s.categoryContext)
	r.Use(s.authContext)

	// SABnzbd API endpoints - all under /api with mode parameter
	r.Route("/api", func(r chi.Router) {
		r.Use(s.modeContext)

		// Queue operations
		r.Get("/", s.handleAPI)
		r.Post("/", s.handleAPI)
	})

	return r
}
