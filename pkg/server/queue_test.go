package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/server/qbit"
	"github.com/sirrobot01/decypharr/pkg/server/sabnzbd"
)

func TestQueueReadFailuresReachHTTPClients(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	config.Get().UseAuth = false
	mgr := manager.New()
	t.Cleanup(func() { _ = mgr.Stop() })
	server := &Server{manager: mgr}
	qbitRoutes := qbit.New(mgr).Routes()
	sabRoutes := sabnzbd.New(mgr).Routes()
	if err := mgr.Storage().Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		handler    http.Handler
	}{
		{"web queue", "/api/torrents", http.HandlerFunc(server.handleGetTorrents)},
		{"qbit queue", "/torrents/info", qbitRoutes},
		{"sab queue", "/api/?mode=queue", sabRoutes},
		{"sab history", "/api/?mode=history", sabRoutes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			tc.handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if response.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500: %s", response.Code, response.Body.String())
			}
		})
	}
}
