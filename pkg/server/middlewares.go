package server

import (
	"fmt"
	"net/http"
	"strings"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/config"
)

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if setup is needed
		cfg := config.Get()
		if !cfg.UseAuth {
			next.ServeHTTP(w, r)
			return
		}

		isAPI := s.isAPIRequest(r)

		if cfg.NeedsAuth() {
			if isAPI {
				s.sendJSONError(w, "Authentication setup required", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/register", http.StatusSeeOther)
			}
			return
		}

		// Check for API token first
		if s.isValidAPIToken(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Fall back to session authentication
		session, _ := s.cookie.Get(r, "auth-session")
		auth, ok := session.Values["authenticated"].(bool)

		if !ok || !auth {
			if isAPI {
				s.sendJSONError(w, "Authentication required. Please provide a valid API token in the Authorization header (Bearer <token>) or authenticate via session cookies.", http.StatusUnauthorized)
			} else {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}

// isAPIRequest checks if the request is for an API endpoint
func (s *Server) isAPIRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, "/api/")
}

// sendJSONError sends a JSON error response
func (s *Server) sendJSONError(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	err := json.ConfigDefault.NewEncoder(w).Encode(map[string]any{
		"error":  message,
		"status": statusCode,
	})
	if err != nil {
		return
	}
}

// setupRedirectMiddleware redirects to /setup if setup is not completed
func (s *Server) setupRedirectMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := config.Get()

		// Skip setup check for setup-related routes
		if strings.HasPrefix(r.URL.Path, "/setup") ||
			strings.HasPrefix(r.URL.Path, "/api/setup") ||
			strings.HasPrefix(r.URL.Path, "/api/login") ||
			strings.HasPrefix(r.URL.Path, "/api/logout") ||
			strings.HasPrefix(r.URL.Path, "/api/config") ||
			strings.HasPrefix(r.URL.Path, "/assets") ||
			strings.HasPrefix(r.URL.Path, "/images") ||
			r.URL.Path == "/version" {
			next.ServeHTTP(w, r)
			return
		}

		// Check if setup is completed
		if err := cfg.SetupComplete(); err != nil {
			isAPI := s.isAPIRequest(r)
			if isAPI {
				s.sendJSONError(w, fmt.Sprintf("[error] %s Setup wizard must be completed first. Please visit /setup", err), http.StatusServiceUnavailable)
			} else {
				http.Redirect(w, r, "/setup", http.StatusSeeOther)
			}
			return
		}

		next.ServeHTTP(w, r)
	})
}
