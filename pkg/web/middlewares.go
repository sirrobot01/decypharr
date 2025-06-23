package web

import (
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"net/http"
)

func (wb *Web) setupMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()
		needsAuth := cfg.NeedsSetup()
		if needsAuth != nil && r.URL.Path != "/config" && r.URL.Path != "/api/config" {
			http.Redirect(w, r, fmt.Sprintf("/config?inco=%s", needsAuth.Error()), http.StatusSeeOther)
			return
		}

		// strip inco from URL
		if inco := r.URL.Query().Get("inco"); inco != "" && needsAuth == nil && r.URL.Path == "/config" {
			// redirect to the same URL without the inco parameter
			http.Redirect(w, r, "/config", http.StatusSeeOther)
		}
		next.ServeHTTP(w, r)
	})
}

func (wb *Web) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if setup is needed
		cfg := config.Get()
		if !cfg.UseAuth {
			next.ServeHTTP(w, r)
			return
		}

		if cfg.NeedsAuth() {
			http.Redirect(w, r, "/register", http.StatusSeeOther)
			return
		}

		session, _ := wb.cookie.Get(r, "auth-session")
		auth, ok := session.Values["authenticated"].(bool)

		if !ok || !auth {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}
