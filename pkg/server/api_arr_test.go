package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/sessions"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/arr/reacquire"
)

func TestArrReacquireRoutesRequireAuthentication(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)

	cfg := config.Get()
	cfg.DownloadFolder = t.TempDir()
	cfg.Debrids = []config.Debrid{{Name: "realdebrid", APIKey: "key"}}
	if err := cfg.SaveAuth(&config.Auth{APIToken: "token", TokenOnly: true}); err != nil {
		t.Fatal(err)
	}

	server := newTestServer(t)
	server.cookie = sessions.NewCookieStore([]byte("test-secret"))
	handler := server.WebRoutes()
	tests := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/api/arr/reacquire/jobs"},
		{method: http.MethodGet, path: "/api/arr/reacquire/jobs/job-id"},
		{method: http.MethodDelete, path: "/api/arr/reacquire/jobs"},
		{method: http.MethodPost, path: "/api/arr/reacquire"},
		{method: http.MethodPost, path: "/api/arr/index/refresh"},
		{method: http.MethodGet, path: "/api/arr/index"},
		{method: http.MethodGet, path: "/api/arr/bindings"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(test.method, test.path, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", contentType)
			}
		})
	}
}

func TestValidateArrReacquireRequest(t *testing.T) {
	valid := reacquire.Request{
		EntryID: "entry",
		FileID:  "file",
		Cause:   reacquire.CauseManual,
	}
	if message := validateArrReacquireRequest(valid); message != "" {
		t.Fatalf("valid request rejected: %s", message)
	}

	valid.Strategy = "unknown"
	if message := validateArrReacquireRequest(valid); message == "" {
		t.Fatal("invalid strategy accepted")
	}
}
