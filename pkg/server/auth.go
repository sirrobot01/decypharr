package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/sirrobot01/decypharr/internal/config"
)

func (s *Server) skipAuthHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	// Only allow skipping auth during initial setup (before setup is complete)
	if err := cfg.SetupComplete(); err == nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfg.UseAuth = false
	if err := cfg.Save(); err != nil {
		s.logger.Error().Err(err).Msg("failed to save config")
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// isValidAPIToken checks if the request contains a valid API token
func (s *Server) isValidAPIToken(r *http.Request) bool {
	// Check Authorization header for Bearer token
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		return false
	}

	// Support both "Bearer <token>" and "Token <token>" formats
	var token string
	if after, ok := strings.CutPrefix(authHeader, "Bearer "); ok {
		token = after
	} else if after, ok := strings.CutPrefix(authHeader, "Token "); ok {
		token = after
	} else {
		return false
	}

	if token == "" {
		return false
	}

	return config.VerifyToken(token)
}

// refreshAPIToken generates a new API token and saves it
func (s *Server) refreshAPIToken() (string, error) {
	auth := config.Get().GetAuth()
	if auth == nil {
		return "", fmt.Errorf("authentication not configured")
	}

	// Generate new token
	token, err := config.GenerateAPIToken()
	if err != nil {
		return "", err
	}

	// Update auth config
	auth.APIToken = token

	// Save auth config
	if err := config.Get().SaveAuth(auth); err != nil {
		return "", err
	}

	return token, nil
}
