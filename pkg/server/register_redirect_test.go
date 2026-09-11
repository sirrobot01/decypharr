package server

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		urlBase: "/",
		logger:  zerolog.Nop(),
		templates: template.Must(template.ParseFS(
			content,
			"templates/layout.html",
			"templates/setup_layout.html",
			"templates/index.html",
			"templates/download.html",
			"templates/repair.html",
			"templates/stats.html",
			"templates/config.html",
			"templates/browse.html",
			"templates/login.html",
			"templates/register.html",
			"templates/setup.html",
		)),
	}
}

// serve runs one request through the middleware chain the router actually
// builds for that route. setupRedirectMiddleware is global; authMiddleware
// wraps the protected group only, so /register and /login never see it.
func serve(s *Server, method, path string, protected bool) *httptest.ResponseRecorder {
	var h http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch path {
		case "/register":
			s.RegisterHandler(w, r)
		case "/login":
			s.LoginHandler(w, r)
		}
	})
	if protected {
		h = s.authMiddleware(h)
	}
	w := httptest.NewRecorder()
	s.setupRedirectMiddleware(h).ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w
}

func TestRegisterRedirects(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	// A fresh install: createConfig writes a config with UseAuth on and nothing
	// else, so auth is enabled before any credential exists.
	cfg := config.Get()
	s := newTestServer(t)

	if !cfg.UseAuth {
		t.Fatal("fresh install has UseAuth off, want on")
	}
	if err := cfg.SetupComplete(); err == nil {
		t.Fatal("fresh install reports setup complete")
	}

	// The wizard comes first: even though NeedsAuth is true, the setup
	// middleware runs ahead of the auth middleware and wins.
	if w := serve(s, http.MethodGet, "/", true); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Errorf("fresh install: GET / = %d %q, want 303 /setup", w.Code, w.Header().Get("Location"))
	}

	// Setup incomplete: the global middleware sends /register to /setup. It is a
	// single hop — /setup is on the skip list, so nothing bounces back.
	if w := serve(s, http.MethodGet, "/register", false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/setup" {
		t.Errorf("setup incomplete: GET /register = %d %q, want 303 /setup", w.Code, w.Header().Get("Location"))
	}

	// Setup complete, auth on, no credential stored: a browser is sent to
	// /register, and /register serves the form to an unauthenticated caller.
	cfg.DownloadFolder = t.TempDir()
	cfg.Debrids = []config.Debrid{{Name: "realdebrid", APIKey: "key"}}
	if err := cfg.SetupComplete(); err != nil {
		t.Fatalf("setup should be complete: %v", err)
	}
	if w := serve(s, http.MethodGet, "/", true); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/register" {
		t.Errorf("GET / = %d %q, want 303 /register", w.Code, w.Header().Get("Location"))
	}
	if w := serve(s, http.MethodGet, "/register", false); w.Code != http.StatusOK {
		t.Errorf("GET /register = %d, want 200 with the form", w.Code)
	}

	// Credential stored: registration closes. This is the account-takeover
	// hole — before the guard, this POST overwrote the stored credentials.
	if err := cfg.SaveAuth(&config.Auth{Username: "admin", Password: "hash", APIToken: "tok"}); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, http.MethodGet, "/register", false); w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Errorf("configured: GET /register = %d %q, want 303 /", w.Code, w.Header().Get("Location"))
	}
	if w := serve(s, http.MethodPost, "/register", false); w.Code != http.StatusForbidden {
		t.Errorf("configured: POST /register = %d, want 403", w.Code)
	}

	// Token-only mode has no password, so registration stays closed there too.
	if err := cfg.SaveAuth(&config.Auth{APIToken: "tok", TokenOnly: true}); err != nil {
		t.Fatal(err)
	}
	if w := serve(s, http.MethodPost, "/register", false); w.Code != http.StatusForbidden {
		t.Errorf("token-only: POST /register = %d, want 403", w.Code)
	}
}
