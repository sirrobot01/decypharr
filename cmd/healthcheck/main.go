package main

import (
	"cmp"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/config"
)

// HealthStatus represents the status of various components
type HealthStatus struct {
	QbitAPI       bool `json:"qbit_api"`
	WebUI         bool `json:"web_ui"`
	WebDAVService bool `json:"webdav_service"`
	OverallStatus bool `json:"overall_status"`
}

func main() {
	var (
		configPath string
		debug      bool
	)
	flag.StringVar(&configPath, "config", "/data", "path to the data folder")
	flag.BoolVar(&debug, "debug", false, "enable debug mode for detailed output")
	flag.Parse()
	config.SetConfigPath(configPath)
	cfg := config.Get()
	// GetReader port from environment variable or use default
	port := cmp.Or(os.Getenv("QBIT_PORT"), cfg.Port)

	// Initialize status
	status := HealthStatus{
		QbitAPI:       false,
		WebUI:         false,
		WebDAVService: false,
		OverallStatus: false,
	}

	// Create a context with timeout for all HTTP requests
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	baseUrl := cmp.Or(cfg.URLBase, "/")
	auth := cfg.GetAuth()

	status.QbitAPI = checkQbitAPI(ctx, client, baseUrl, port, auth, cfg.UseAuth)
	status.WebUI = checkWebUI(ctx, client, baseUrl, port, auth, cfg.UseAuth)
	status.WebDAVService = checkBaseWebdav(ctx, client, baseUrl, port, cfg)
	// Determine overall status
	// Consider the application healthy if core services are running
	status.OverallStatus = status.QbitAPI && status.WebUI && status.WebDAVService

	// Optional: output health status as JSON for logging
	if debug {
		statusJSON, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(statusJSON))
	}

	// Exit with appropriate code
	if status.OverallStatus {
		os.Exit(0)
	}

	os.Exit(1)
}

func checkQbitAPI(ctx context.Context, client *http.Client, baseUrl, port string, auth *config.Auth, authMayBeRequired bool) bool {
	url := localURL(port, baseUrl, "api/v2/app/version")
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}
	addBearerAuth(req, auth)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer drainAndClose(resp)

	return isHealthyStatus(resp.StatusCode, authMayBeRequired, http.StatusOK)
}

func checkWebUI(ctx context.Context, client *http.Client, baseUrl, port string, auth *config.Auth, authMayBeRequired bool) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", localURL(port, baseUrl, "version"), nil)
	if err != nil {
		return false
	}
	addBearerAuth(req, auth)

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer drainAndClose(resp)

	return isHealthyStatus(resp.StatusCode, authMayBeRequired, http.StatusOK) || isRedirect(resp.StatusCode)
}

func checkBaseWebdav(ctx context.Context, client *http.Client, baseUrl, port string, cfg *config.Config) bool {
	url := localURL(port, baseUrl, "webdav/")
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", url, nil)
	if err != nil {
		return false
	}

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer drainAndClose(resp)

	authMayBeRequired := cfg.UseAuth && cfg.EnableWebdavAuth
	return isHealthyStatus(resp.StatusCode, authMayBeRequired, http.StatusOK, http.StatusCreated, http.StatusMultiStatus)
}

func localURL(port, baseUrl, endpoint string) string {
	base := strings.Trim(baseUrl, "/")
	endpoint = strings.TrimLeft(endpoint, "/")

	switch {
	case base == "" && endpoint == "":
		return fmt.Sprintf("http://localhost:%s/", port)
	case base == "":
		return fmt.Sprintf("http://localhost:%s/%s", port, endpoint)
	case endpoint == "":
		return fmt.Sprintf("http://localhost:%s/%s/", port, base)
	default:
		return fmt.Sprintf("http://localhost:%s/%s/%s", port, base, endpoint)
	}
}

func addBearerAuth(req *http.Request, auth *config.Auth) {
	if auth == nil || auth.APIToken == "" {
		return
	}
	req.Header.Set("Authorization", "Bearer "+auth.APIToken)
}

func drainAndClose(resp *http.Response) {
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func isHealthyStatus(statusCode int, authMayBeRequired bool, expectedStatusCodes ...int) bool {
	if slices.Contains(expectedStatusCodes, statusCode) {
		return true
	}

	if !authMayBeRequired {
		return false
	}

	return statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden
}

func isRedirect(statusCode int) bool {
	return statusCode >= http.StatusMultipleChoices && statusCode < http.StatusBadRequest
}
