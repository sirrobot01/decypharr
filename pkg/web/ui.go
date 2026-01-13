package web

import (
	"encoding/json"
	"net/http"

	"github.com/sirrobot01/decypharr/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func (wb *Web) LoginHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	if cfg.NeedsAuth() {
		wb.redirectTo(w, r, "/register")
		return
	}
	if r.Method == "GET" {
		data := map[string]interface{}{
			"URLBase": cfg.URLBase,
			"Page":    "login",
			"Title":   "Login",
		}
		_ = wb.templates.ExecuteTemplate(w, "layout", data)
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

	if wb.verifyAuth(credentials.Username, credentials.Password) {
		session, _ := wb.cookie.Get(r, "auth-session")
		session.Values["authenticated"] = true
		session.Values["username"] = credentials.Username
		if err := session.Save(r, w); err != nil {
			http.Error(w, "Error saving session", http.StatusInternalServerError)
			return
		}
		wb.redirectTo(w, r, "/")
		return
	}

	http.Error(w, "Invalid credentials", http.StatusUnauthorized)
}

func (wb *Web) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := wb.cookie.Get(r, "auth-session")
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	err := session.Save(r, w)
	if err != nil {
		return
	}
	wb.redirectTo(w, r, "/login")
}

func (wb *Web) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	authCfg := cfg.GetAuth()

	if r.Method == "GET" {
		data := map[string]interface{}{
			"URLBase": cfg.URLBase,
			"Page":    "register",
			"Title":   "Register",
		}
		_ = wb.templates.ExecuteTemplate(w, "layout", data)
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
	session, _ := wb.cookie.Get(r, "auth-session")
	session.Values["authenticated"] = true
	session.Values["username"] = username
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Error saving session", http.StatusInternalServerError)
		return
	}

	wb.redirectTo(w, r, "/")
}

func (wb *Web) IndexHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase":    cfg.URLBase,
		"Page":       "index",
		"Title":      "Torrents",
		"NeedSetup":  cfg.CheckSetup() != nil,
		"SetupError": cfg.CheckSetup(),
	}
	_ = wb.templates.ExecuteTemplate(w, "layout", data)
}

func (wb *Web) DownloadHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	debrids := make([]string, 0)
	for _, d := range cfg.Debrids {
		debrids = append(debrids, d.Name)
	}
	data := map[string]interface{}{
		"URLBase":             	cfg.URLBase,
		"Page":                	"download",
		"Title":               	"Download",
		"Debrids":             	debrids,
		"HasMultiDebrid":      	len(debrids) > 1,
		"DownloadFolder":      	cfg.QBitTorrent.DownloadFolder,
		"AlwaysRmTrackerUrls": 	cfg.QBitTorrent.AlwaysRmTrackerUrls,
		"NeedSetup":           	cfg.CheckSetup() != nil,
		"SetupError":          	cfg.CheckSetup(),
	}
	_ = wb.templates.ExecuteTemplate(w, "layout", data)
}

func (wb *Web) RepairHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase":    cfg.URLBase,
		"Page":       "repair",
		"Title":      "Repair",
		"NeedSetup":  cfg.CheckSetup() != nil,
		"SetupError": cfg.CheckSetup(),
	}
	_ = wb.templates.ExecuteTemplate(w, "layout", data)
}

func (wb *Web) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase":    cfg.URLBase,
		"Page":       "config",
		"Title":      "Config",
		"NeedSetup":  cfg.CheckSetup() != nil,
		"SetupError": cfg.CheckSetup(),
	}
	_ = wb.templates.ExecuteTemplate(w, "layout", data)
}

func (wb *Web) StatsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]interface{}{
		"URLBase": cfg.URLBase,
		"Page":    "stats",
		"Title":   "Statistics",
	}
	_ = wb.templates.ExecuteTemplate(w, "layout", data)
}
