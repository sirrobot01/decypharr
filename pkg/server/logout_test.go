package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/sessions"
	"github.com/sirrobot01/decypharr/internal/config"
)

func TestBrowserLogout(t *testing.T) {
	for _, tc := range []struct {
		name, base, credentials string
		tokenOnly               bool
	}{
		{"password", "/", `{"username":"admin","password":"test-password"}`, false},
		{"token on subpath", "/decypharr/", `{"password":"test-token"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config.Reset()
			config.SetConfigPath(t.TempDir())
			t.Cleanup(config.Reset)
			cfg := config.Get()
			cfg.URLBase = tc.base
			cfg.UseAuth = true
			cfg.DownloadFolder = t.TempDir()
			cfg.Debrids = []config.Debrid{{Name: "realdebrid", APIKey: "key"}}
			var err error
			if tc.tokenOnly {
				err = cfg.SaveAuth(&config.Auth{TokenOnly: true, APIToken: "test-token"})
			} else {
				err = cfg.SetCredentials("admin", "test-password")
			}
			if err != nil {
				t.Fatal(err)
			}

			s := newTestServer(t)
			s.urlBase = tc.base
			s.cookie = sessions.NewCookieStore([]byte(cfg.SecretKey()))
			router := chi.NewRouter()
			routePath := tc.base
			if routePath != "/" {
				routePath = strings.TrimSuffix(routePath, "/")
			}
			router.Route(routePath, func(r chi.Router) {
				r.Mount("/", s.WebRoutes())
			})
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			request := func(method, route, body string, status int, location string) *httptest.ResponseRecorder {
				t.Helper()
				req := httptest.NewRequest(method, "https://example.test"+tc.base+route, strings.NewReader(body))
				for _, cookie := range jar.Cookies(req.URL) {
					req.AddCookie(cookie)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, req)
				result := response.Result()
				defer result.Body.Close()
				jar.SetCookies(req.URL, result.Cookies())
				if response.Code != status || response.Header().Get("Location") != location {
					t.Fatalf("%s %s: got %d with Location %q, want %d with Location %q", method, req.URL.Path, response.Code, response.Header().Get("Location"), status, location)
				}
				return response
			}

			form := `<form method="post" action="` + tc.base + `logout">`
			request(http.MethodPost, "login", tc.credentials, http.StatusSeeOther, tc.base)
			request(http.MethodGet, "logout", "", http.StatusMethodNotAllowed, "")
			dashboard := request(http.MethodGet, "", "", http.StatusOK, "")
			if !strings.Contains(dashboard.Body.String(), form) {
				t.Fatal("signed-in dashboard is missing the sign-out form")
			}
			logout := request(http.MethodPost, "logout", "", http.StatusSeeOther, tc.base+"login")
			cookies := logout.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "auth-session" || cookies[0].MaxAge >= 0 {
				t.Fatal("logout did not expire the auth-session cookie")
			}
			request(http.MethodGet, "", "", http.StatusSeeOther, tc.base+"login")
			login := request(http.MethodGet, "login", "", http.StatusOK, "")
			if strings.Contains(login.Body.String(), form) {
				t.Fatal("login page displays the sign-out form")
			}
			request(http.MethodPost, "login", tc.credentials, http.StatusSeeOther, tc.base)
			request(http.MethodGet, "", "", http.StatusOK, "")

			cfg.UseAuth = false
			dashboard = request(http.MethodGet, "", "", http.StatusOK, "")
			if strings.Contains(dashboard.Body.String(), form) {
				t.Fatal("dashboard displays the sign-out form with authentication disabled")
			}
		})
	}
}
