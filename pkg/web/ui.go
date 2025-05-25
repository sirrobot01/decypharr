package web

import (
	"encoding/json"
	"github.com/sirrobot01/decypharr/internal/config"
	"golang.org/x/crypto/bcrypt"
	"net/http"
)

func (ui *Handler) LoginHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	if cfg.NeedsAuth() {
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}
	if r.Method == "GET" {
		data := map[string]interface{}{
			"URLBase": cfg.URLBase,
			"Page":    "login",
			"Title":   "Login",
		}
		_ = templates.ExecuteTemplate(w, "layout", data)
		return
	}

	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.NewDecoder(r.Body).Decode(&credentials); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if ui.verifyAuth(credentials.Username, credentials.Password) {
		session, _ := store.Get(r, "auth-session")
		session.Values["authenticated"] = true
		session.Values["username"] = credentials.Username
		if err := session.Save(r, w); err != nil {
			http.Error(w, "Error saving session", http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
}

func (ui *Handler) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := store.Get(r, "auth-session")
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	err := session.Save(r, w)
	if err != nil {
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (ui *Handler) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	authCfg := cfg.GetAuth()

	if r.Method == "GET" {
		data := map[string]interface{}{
			"URLBase": cfg.URLBase,
			"Page":    "register",
			"Title":   "Register",
		}
		_ = templates.ExecuteTemplate(w, "layout", data)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")
	confirmPassword := r.FormValue("confirmPassword")

	if password != confirmPassword {
		http.Error(w, "Passwords do not match", http.StatusBadRequest)
		return
	}

	// Hash the password
	hashedPassword, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "Error processing password", http.StatusInternalServerError)
		return
	}

	// Set the credentials
	authCfg.Username = username
	authCfg.Password = string(hashedPassword)

	if err := cfg.SaveAuth(authCfg); err != nil {
		http.Error(w, "Error saving credentials", http.StatusInternalServerError)
		return
	}

	// Create a session
	session, _ := store.Get(r, "auth-session")
	session.Values["authenticated"] = true
	session.Values["username"] = username
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Error saving session", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (ui *Handler) IndexHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase": cfg.URLBase,
		"Page":    "index",
		"Title":   "Torrents",
	}
	_ = templates.ExecuteTemplate(w, "layout", data)
}

func (ui *Handler) DownloadHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase": cfg.URLBase,
		"Page":    "download",
		"Title":   "Download",
	}
	_ = templates.ExecuteTemplate(w, "layout", data)
}

func (ui *Handler) RepairHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase": cfg.URLBase,
		"Page":    "repair",
		"Title":   "Repair",
	}
	_ = templates.ExecuteTemplate(w, "layout", data)
}

func (ui *Handler) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase": cfg.URLBase,
		"Page":    "config",
		"Title":   "Config",
	}
	_ = templates.ExecuteTemplate(w, "layout", data)
}
