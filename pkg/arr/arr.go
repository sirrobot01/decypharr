package arr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Type is a type of arr
type Type string

const (
	Sonarr  Type = "sonarr"
	Radarr  Type = "radarr"
	Lidarr  Type = "lidarr"
	Readarr Type = "readarr"
)

type Arr struct {
	Name             string `json:"name"`
	Host             string `json:"host"`
	Token            string `json:"token"`
	Type             Type   `json:"type"`
	Cleanup          bool   `json:"cleanup"`
	SkipRepair       bool   `json:"skip_repair"`
	DownloadUncached *bool  `json:"download_uncached"`
	client           *request.Client
}

func New(name, host, token string, cleanup, skipRepair bool, downloadUncached *bool) *Arr {
	return &Arr{
		Name:             name,
		Host:             host,
		Token:            strings.TrimSpace(token),
		Type:             InferType(host, name),
		Cleanup:          cleanup,
		SkipRepair:       skipRepair,
		DownloadUncached: downloadUncached,
		client:           request.New(),
	}
}

func (a *Arr) Request(method, endpoint string, payload interface{}) (*http.Response, error) {
	if a.Token == "" || a.Host == "" {
		return nil, fmt.Errorf("arr not configured")
	}
	url, err := request.JoinURL(a.Host, endpoint)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)

	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", a.Token)
	if a.client == nil {
		a.client = request.New()
	}

	var resp *http.Response

	for attempts := 0; attempts < 5; attempts++ {
		resp, err = a.client.Do(req)
		if err != nil {
			return nil, err
		}

		// If we got a 401, wait briefly and retry
		if resp.StatusCode == http.StatusUnauthorized {
			resp.Body.Close() // Don't leak response bodies
			if attempts < 4 { // Don't sleep on the last attempt
				time.Sleep(time.Duration(attempts+1) * 100 * time.Millisecond)
				continue
			}
		}

		return resp, nil
	}

	return resp, err
}

func (a *Arr) Validate() error {
	if a.Token == "" || a.Host == "" {
		return nil
	}
	resp, err := a.Request("GET", "/api/v3/health", nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("arr test failed: %s", resp.Status)
	}
	return nil
}

type Storage struct {
	Arrs   map[string]*Arr // name -> arr
	mu     sync.Mutex
	logger zerolog.Logger
}

func (as *Storage) Cleanup() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.Arrs = make(map[string]*Arr)
}

func InferType(host, name string) Type {
	switch {
	case strings.Contains(host, "sonarr") || strings.Contains(name, "sonarr"):
		return Sonarr
	case strings.Contains(host, "radarr") || strings.Contains(name, "radarr"):
		return Radarr
	case strings.Contains(host, "lidarr") || strings.Contains(name, "lidarr"):
		return Lidarr
	case strings.Contains(host, "readarr") || strings.Contains(name, "readarr"):
		return Readarr
	default:
		return ""
	}
}

func NewStorage() *Storage {
	arrs := make(map[string]*Arr)
	for _, a := range config.Get().Arrs {
		name := a.Name
		arrs[name] = New(name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached)
	}
	return &Storage{
		Arrs:   arrs,
		logger: logger.New("arr"),
	}
}

func (as *Storage) AddOrUpdate(arr *Arr) {
	as.mu.Lock()
	defer as.mu.Unlock()
	if arr.Name == "" {
		return
	}
	as.Arrs[arr.Name] = arr
}

func (as *Storage) Get(name string) *Arr {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.Arrs[name]
}

func (as *Storage) GetAll() []*Arr {
	as.mu.Lock()
	defer as.mu.Unlock()
	arrs := make([]*Arr, 0, len(as.Arrs))
	for _, arr := range as.Arrs {
		if arr.Host != "" && arr.Token != "" {
			arrs = append(arrs, arr)
		}
	}
	return arrs
}

func (as *Storage) Clear() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.Arrs = make(map[string]*Arr)
}

func (as *Storage) StartSchedule(ctx context.Context) error {

	ticker := time.NewTicker(10 * time.Second)

	select {
	case <-ticker.C:
		as.cleanupArrsQueue()
	case <-ctx.Done():
		ticker.Stop()
		return nil
	}
	return nil
}

func (as *Storage) cleanupArrsQueue() {
	arrs := make([]*Arr, 0)
	for _, arr := range as.Arrs {
		if !arr.Cleanup {
			continue
		}
		arrs = append(arrs, arr)
	}
	if len(arrs) > 0 {
		for _, arr := range arrs {
			if err := arr.CleanupQueue(); err != nil {
				as.logger.Error().Err(err).Msgf("Failed to cleanup arr %s", arr.Name)
			}
		}
	}
}

func (a *Arr) Refresh() error {
	payload := struct {
		Name string `json:"name"`
	}{
		Name: "RefreshMonitoredDownloads",
	}

	resp, err := a.Request(http.MethodPost, "api/v3/command", payload)
	if err == nil && resp != nil {
		statusOk := strconv.Itoa(resp.StatusCode)[0] == '2'
		if statusOk {
			return nil
		}
	}

	return fmt.Errorf("failed to refresh: %v", err)
}
