package server

import (
	"net/http"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) LoginHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	if cfg.NeedsAuth() {
		http.Redirect(w, r, "/register", http.StatusSeeOther)
		return
	}
	if r.Method == "GET" {
		data := map[string]any{
			"URLBase": cfg.URLBase,
			"Page":    "login",
			"Title":   "Login",
		}
		err := s.templates.ExecuteTemplate(w, "layout", data)
		if err != nil {
			s.logger.Warn().Err(err).Msg("error rendering /login template")
		}
		return
	}

	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}

	if err := json.ConfigDefault.NewDecoder(r.Body).Decode(&credentials); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if s.verifyAuth(credentials.Username, credentials.Password) {
		session, _ := s.cookie.Get(r, "auth-session")
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

func (s *Server) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	session, _ := s.cookie.Get(r, "auth-session")
	session.Values["authenticated"] = false
	session.Options.MaxAge = -1
	err := session.Save(r, w)
	if err != nil {
		return
	}
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	authCfg := cfg.GetAuth()

	if r.Method == "GET" {
		data := map[string]any{
			"URLBase": cfg.URLBase,
			"Page":    "register",
			"Title":   "registerVolume",
		}
		err := s.templates.ExecuteTemplate(w, "layout", data)
		if err != nil {
			s.logger.Warn().Err(err).Msg("error rendering /register template")
		}
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
	session, _ := s.cookie.Get(r, "auth-session")
	session.Values["authenticated"] = true
	session.Values["username"] = username
	if err := session.Save(r, w); err != nil {
		http.Error(w, "Error saving session", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) IndexHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]any{
		"URLBase":    cfg.URLBase,
		"Page":       "index",
		"Title":      "Queues",
		"SetupError": cfg.SetupError(),
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /index template")
	}
}

func (s *Server) DownloadHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	debrids := make([]string, 0)
	for _, d := range cfg.Debrids {
		debrids = append(debrids, d.Name)
	}
	data := map[string]any{
		"URLBase":                 cfg.URLBase,
		"Page":                    "download",
		"Title":                   "Download",
		"Debrids":                 debrids,
		"HasMultiDebrid":          len(debrids) > 1,
		"downloadFolder":          cfg.DownloadFolder,
		"alwaysRemoveTrackerURLS": cfg.AlwaysRmTrackerUrls,
		"SetupError":              cfg.SetupError(),
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /download template")
	}
}

func (s *Server) RepairHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]any{
		"URLBase":    cfg.URLBase,
		"Page":       "repair",
		"Title":      "Repair",
		"SetupError": cfg.SetupError(),
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /repair template")
	}
}

func (s *Server) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]any{
		"URLBase":    cfg.URLBase,
		"Page":       "config",
		"Title":      "Config",
		"SetupError": cfg.SetupError(),
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /config template")
	}
}

func (s *Server) StatsHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]any{
		"URLBase": cfg.URLBase,
		"Page":    "stats",
		"Title":   "Statistics",
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /stats template")
	}
}

func (s *Server) BrowseHandler(w http.ResponseWriter, r *http.Request) {
	cfg := config.Get()
	data := map[string]any{
		"URLBase":    cfg.URLBase,
		"Page":       "browse",
		"Title":      "Browse Torrents",
		"SetupError": cfg.SetupError(),
	}
	err := s.templates.ExecuteTemplate(w, "layout", data)
	if err != nil {
		s.logger.Warn().Err(err).Msg("error rendering /browse template")
	}
}
