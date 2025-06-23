package arr

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Type is a type of arr
type Type string

var sharedClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
	},
	Timeout: 60 * time.Second,
}

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
	SelectedDebrid   string `json:"selected_debrid,omitempty"` // The debrid service selected for this arr
	Source           string `json:"source,omitempty"`          // The source of the arr, e.g. "auto", "manual". Auto means it was automatically detected from the arr
}

func New(name, host, token string, cleanup, skipRepair bool, downloadUncached *bool, selectedDebrid, source string) *Arr {
	return &Arr{
		Name:             name,
		Host:             host,
		Token:            strings.TrimSpace(token),
		Type:             InferType(host, name),
		Cleanup:          cleanup,
		SkipRepair:       skipRepair,
		DownloadUncached: downloadUncached,
		SelectedDebrid:   selectedDebrid,
		Source:           source,
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

	var resp *http.Response

	for attempts := 0; attempts < 5; attempts++ {
		resp, err = sharedClient.Do(req)
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
		return fmt.Errorf("arr not configured: %s", a.Name)
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

func (s *Storage) Cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Arrs = make(map[string]*Arr)
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
		if a.Host == "" || a.Token == "" || a.Name == "" {
			continue // Skip if host or token is not set
		}
		name := a.Name
		arrs[name] = New(name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source)
	}
	return &Storage{
		Arrs:   arrs,
		logger: logger.New("arr"),
	}
}

func (s *Storage) AddOrUpdate(arr *Arr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arr.Host == "" || arr.Token == "" || arr.Name == "" {
		return
	}
	s.Arrs[arr.Name] = arr
}

func (s *Storage) Get(name string) *Arr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Arrs[name]
}

func (s *Storage) GetAll() []*Arr {
	s.mu.Lock()
	defer s.mu.Unlock()
	arrs := make([]*Arr, 0, len(s.Arrs))
	for _, arr := range s.Arrs {
		arrs = append(arrs, arr)
	}
	return arrs
}

func (s *Storage) StartSchedule(ctx context.Context) error {

	ticker := time.NewTicker(10 * time.Second)

	select {
	case <-ticker.C:
		s.cleanupArrsQueue()
	case <-ctx.Done():
		ticker.Stop()
		return nil
	}
	return nil
}

func (s *Storage) cleanupArrsQueue() {
	arrs := make([]*Arr, 0)
	for _, arr := range s.Arrs {
		if !arr.Cleanup {
			continue
		}
		arrs = append(arrs, arr)
	}
	if len(arrs) > 0 {
		for _, arr := range arrs {
			if err := arr.CleanupQueue(); err != nil {
				s.logger.Error().Err(err).Msgf("Failed to cleanup arr %s", arr.Name)
			}
		}
	}
}

func (a *Arr) Refresh() {
	payload := struct {
		Name string `json:"name"`
	}{
		Name: "RefreshMonitoredDownloads",
	}

	_, _ = a.Request(http.MethodPost, "api/v3/command", payload)
}
