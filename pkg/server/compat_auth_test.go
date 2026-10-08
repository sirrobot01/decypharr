package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/server/qbit"
	"github.com/sirrobot01/decypharr/pkg/server/sabnzbd"
)

func TestCompatibilityAPIsAuthenticateBeforeProbing(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	cfg := config.Get()
	cfg.UseAuth = true
	cfg.Auth = &config.Auth{APIToken: "server-token", TokenOnly: true}
	cfg.Arrs = []config.Arr{{Name: "saved", Host: "http://saved.invalid", Token: "saved-token", Source: "auto"}}
	mgr := manager.New()
	t.Cleanup(func() {
		if err := mgr.Stop(); err != nil {
			t.Error(err)
		}
	})

	var probes atomic.Int64
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_, _ = w.Write([]byte(`{"appName":"Sonarr"}`))
	}))
	t.Cleanup(endpoint.Close)
	mgr.Arr().AddOrUpdate(arr.Arr{Name: "manual", Host: endpoint.URL, Token: "arr-token", Source: arr.SourceManual})
	mgr.Arr().AddOrUpdate(arr.Arr{Name: "discovered", Host: endpoint.URL + "/discovered", Token: "arr-token", Source: arr.SourceAuto})

	for _, protocol := range []struct {
		name    string
		handler http.Handler
	}{
		{name: "qbit", handler: qbit.New(mgr).Routes()},
		{name: "sabnzbd", handler: sabnzbd.New(mgr).Routes()},
	} {
		t.Run(protocol.name, func(t *testing.T) {
			for _, tc := range []struct {
				name, category, username, password string
				wantStatus                         int
			}{
				{"untrusted endpoint", "unknown", endpoint.URL, "arr-token", http.StatusUnauthorized},
				{"wrong configured token", "manual", endpoint.URL, "wrong", http.StatusUnauthorized},
				{"wrong configured host", "manual", "http://untrusted.invalid", "arr-token", http.StatusUnauthorized},
				{"discovered credentials", "discovered", endpoint.URL + "/discovered", "arr-token", http.StatusUnauthorized},
				{"discovered credentials without category", "", endpoint.URL + "/discovered", "arr-token", http.StatusUnauthorized},
				{"configured credentials", "manual", endpoint.URL, "arr-token", http.StatusOK},
				{"configured credentials without category", "", endpoint.URL, "arr-token", http.StatusOK},
				{"saved auto credentials", "saved", "http://saved.invalid", "saved-token", http.StatusOK},
				{"saved auto credentials without category", "", "http://saved.invalid", "saved-token", http.StatusOK},
				{"wrong saved token", "saved", "http://saved.invalid", "wrong", http.StatusUnauthorized},
				{"wrong saved category", "manual", "http://saved.invalid", "saved-token", http.StatusUnauthorized},
				{"local token", "manual", "", "server-token", http.StatusOK},
			} {
				t.Run(tc.name, func(t *testing.T) {
					query := url.Values{"category": {tc.category}}
					path := "/torrents/categories"
					if protocol.name == "sabnzbd" {
						path = "/api/"
						query.Set("mode", "version")
						query.Set("ma_username", tc.username)
						query.Set("ma_password", tc.password)
						query.Set("apikey", "legacy-placeholder")
					}
					req := httptest.NewRequest(http.MethodGet, path+"?"+query.Encode(), nil)
					req.SetBasicAuth(tc.username, tc.password)
					response := httptest.NewRecorder()
					protocol.handler.ServeHTTP(response, req)
					if response.Code != tc.wantStatus {
						t.Fatalf("status = %d, want %d: %s", response.Code, tc.wantStatus, response.Body.String())
					}
					if got := probes.Load(); got != 0 {
						t.Fatalf("authentication sent %d probe requests", got)
					}
				})
			}
		})
	}
	t.Run("qbit login and cookie without category", func(t *testing.T) {
		routes := qbit.New(mgr).Routes()
		for _, credentials := range []struct{ username, password string }{
			{endpoint.URL, "arr-token"},
			{"http://saved.invalid", "saved-token"},
			{"client", "server-token"},
		} {
			form := url.Values{"username": {credentials.username}, "password": {credentials.password}}
			req := httptest.NewRequest(http.MethodPost, "/auth/login", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, req)
			if response.Code != http.StatusOK || response.Body.String() != "Ok." {
				t.Fatalf("login status = %d: %s", response.Code, response.Body.String())
			}
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Name != "SID" {
				t.Fatalf("login cookies = %v, want SID", cookies)
			}
			for _, path := range []string{"/app/preferences", "/torrents/categories"} {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				req.AddCookie(cookies[0])
				response := httptest.NewRecorder()
				routes.ServeHTTP(response, req)
				if response.Code != http.StatusOK {
					t.Fatalf("%s status = %d: %s", path, response.Code, response.Body.String())
				}
			}
		}
		if got := probes.Load(); got != 0 {
			t.Fatalf("authentication sent %d probe requests", got)
		}
	})
	t.Run("qbit bearer token", func(t *testing.T) {
		routes := qbit.New(mgr).Routes()
		for _, token := range []string{"server-token", "wrong", ""} {
			req := httptest.NewRequest(http.MethodGet, "/app/preferences", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()
			routes.ServeHTTP(response, req)
			want := http.StatusUnauthorized
			if token == "server-token" {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("token %q: status = %d, want %d", token, response.Code, want)
			}
		}
	})
	t.Run("sabnzbd API key", func(t *testing.T) {
		routes := sabnzbd.New(mgr).Routes()
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			for _, token := range []string{"server-token", "wrong", ""} {
				values := url.Values{"mode": {"version"}, "apikey": {token}}
				req := httptest.NewRequest(method, "/api/?"+values.Encode(), nil)
				if method == http.MethodPost {
					req = httptest.NewRequest(method, "/api/", strings.NewReader(values.Encode()))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				}
				response := httptest.NewRecorder()
				routes.ServeHTTP(response, req)
				want := http.StatusUnauthorized
				if token == "server-token" {
					want = http.StatusOK
				}
				if response.Code != want {
					t.Fatalf("%s token %q: status = %d, want %d", method, token, response.Code, want)
				}
			}
		}
	})
	response := httptest.NewRecorder()
	(&Server{manager: mgr}).handleGetConfig(response, httptest.NewRequest(http.MethodGet, "/api/config", nil))
	var publicConfig map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &publicConfig); err != nil {
		t.Fatal(err)
	}
	if _, exposed := publicConfig["session_secret"]; exposed {
		t.Fatal("the config API exposed the session signing key")
	}
}
