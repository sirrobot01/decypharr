package web

import (
	"github.com/sirrobot01/decypharr/internal/config"
	"golang.org/x/crypto/bcrypt"
	"net/http"
)

func (ui *Handler) verifyAuth(username, password string) bool {
	// If you're storing hashed password, use bcrypt to compare
	if username == "" {
		return false
	}
	auth := config.Get().GetAuth()
	if auth == nil {
		return false
	}
	if username != auth.Username {
		return false
	}
	err := bcrypt.CompareHashAndPassword([]byte(auth.Password), []byte(password))
	return err == nil
}

func (ui *Handler) skipAuthHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	cfg.UseAuth = false
	if err := cfg.Save(); err != nil {
		ui.logger.Error().Err(err).Msg("failed to save config")
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
