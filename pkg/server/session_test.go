package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/sessions"
	"github.com/sirrobot01/decypharr/internal/config"
)

func TestCredentialChangesInvalidateBrowserSessions(t *testing.T) {
	for _, change := range []string{"password", "token", "mode"} {
		t.Run(change, func(t *testing.T) {
			config.Reset()
			config.SetConfigPath(t.TempDir())
			t.Cleanup(config.Reset)
			cfg := config.Get()
			cfg.UseAuth = true
			body := `{"username":"admin","password":"old-password"}`
			if change == "token" {
				if err := cfg.SaveAuth(&config.Auth{TokenOnly: true, APIToken: "old-token"}); err != nil {
					t.Fatal(err)
				}
				body = `{"password":"old-token"}`
			} else if err := cfg.SetCredentials("admin", "old-password"); err != nil {
				t.Fatal(err)
			}
			s := &Server{cookie: sessions.NewCookieStore([]byte(cfg.SecretKey()))}
			login := httptest.NewRecorder()
			s.LoginHandler(login, httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body)))
			if login.Code != http.StatusSeeOther {
				t.Fatalf("login status = %d: %s", login.Code, login.Body.String())
			}
			cookies := login.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatalf("login set %d cookies, want 1", len(cookies))
			}
			handler := s.authMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}))
			request := httptest.NewRequest(http.MethodGet, "/api/test", nil)
			request.AddCookie(cookies[0])
			before := httptest.NewRecorder()
			handler.ServeHTTP(before, request)
			if before.Code != http.StatusNoContent {
				t.Fatalf("fresh session status = %d", before.Code)
			}
			switch change {
			case "password":
				if err := cfg.SetCredentials("admin", "new-password"); err != nil {
					t.Fatal(err)
				}
			case "token":
				if _, err := s.refreshAPIToken(); err != nil {
					t.Fatal(err)
				}
			case "mode":
				if err := cfg.SaveAuth(&config.Auth{TokenOnly: true, APIToken: "new-token"}); err != nil {
					t.Fatal(err)
				}
			}
			after := httptest.NewRecorder()
			handler.ServeHTTP(after, request)
			if after.Code != http.StatusUnauthorized {
				t.Fatalf("old session status = %d, want 401", after.Code)
			}
		})
	}
}
