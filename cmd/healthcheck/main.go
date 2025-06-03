package main

import (
	"cmp"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/sirrobot01/decypharr/internal/config"
	"net/http"
	"os"
	"strings"
	"time"
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
		configPath   string
		isBasicCheck bool
		debug        bool
	)
	flag.StringVar(&configPath, "config", "/data", "path to the data folder")
	flag.BoolVar(&isBasicCheck, "basic", false, "perform basic health check without WebDAV")
	flag.BoolVar(&debug, "debug", false, "enable debug mode for detailed output")
	flag.Parse()
	config.SetConfigPath(configPath)
	cfg := config.Get()
	// Get port from environment variable or use default
	port := getEnvOrDefault("QBIT_PORT", cfg.Port)
	webdavPath := ""
	for _, debrid := range cfg.Debrids {
		if debrid.UseWebDav {
			webdavPath = debrid.Name
		}
	}

	// Initialize status
	status := HealthStatus{
		QbitAPI:       false,
		WebUI:         false,
		WebDAVService: false,
		OverallStatus: false,
	}

	// Create a context with timeout for all HTTP requests
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	baseUrl := cmp.Or(cfg.URLBase, "/")
	if !strings.HasPrefix(baseUrl, "/") {
		baseUrl = "/" + baseUrl
	}

	// Check qBittorrent API
	if checkQbitAPI(ctx, baseUrl, port) {
		status.QbitAPI = true
	}

	// Check Web UI
	if checkWebUI(ctx, baseUrl, port) {
		status.WebUI = true
	}

	if isBasicCheck {
		status.WebDAVService = checkBaseWebdav(ctx, baseUrl, port)
	} else {
		// If not a basic check, check WebDAV with debrid path
		if webdavPath != "" {
			status.WebDAVService = checkDebridWebDAV(ctx, baseUrl, port, webdavPath)
		} else {
			// If no WebDAV path is set, consider it healthy
			status.WebDAVService = true
		}
	}
	// Determine overall status
	// Consider the application healthy if core services are running
	status.OverallStatus = status.QbitAPI && status.WebUI
	if webdavPath != "" {
		status.OverallStatus = status.OverallStatus && status.WebDAVService
	}

	// Optional: output health status as JSON for logging
	if debug {
		statusJSON, _ := json.MarshalIndent(status, "", "  ")
		fmt.Println(string(statusJSON))
	}

	// Exit with appropriate code
	if status.OverallStatus {
		os.Exit(0)
	} else {
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return defaultValue
}

func checkQbitAPI(ctx context.Context, baseUrl, port string) bool {
	url := fmt.Sprintf("http://localhost:%s%sapi/v2/app/version", port, baseUrl)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func checkWebUI(ctx context.Context, baseUrl, port string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("http://localhost:%s%s", port, baseUrl), nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

func checkBaseWebdav(ctx context.Context, baseUrl, port string) bool {
	url := fmt.Sprintf("http://localhost:%s%swebdav/", port, baseUrl)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusMultiStatus ||
		resp.StatusCode == http.StatusOK
}

func checkDebridWebDAV(ctx context.Context, baseUrl, port, path string) bool {
	url := fmt.Sprintf("http://localhost:%s%swebdav/%s", port, baseUrl, path)
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", url, nil)
	if err != nil {
		return false
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusMultiStatus ||
		resp.StatusCode == http.StatusOK

}
