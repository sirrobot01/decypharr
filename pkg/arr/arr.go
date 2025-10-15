package arr

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
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
	Others  Type = "others"
)

type Arr struct {
	Name  string `json:"name"`
	Host  string `json:"host"`
	Token string `json:"token"`

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
		return fmt.Errorf("arr not configured")
	}

	if request.ValidateURL(a.Host) != nil {
		return fmt.Errorf("invalid arr host URL")
	}
	resp, err := a.Request("GET", "/api/v3/health", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// If response is not 200 or 404(this is the case for Lidarr, etc), return an error
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to validate arr %s: %s", a.Name, resp.Status)
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
		return Others
	}
}

func NewStorage() *Storage {
	arrs := make(map[string]*Arr)
	log := logger.New("arr")
	
	for _, a := range config.Get().Arrs {
		if a.Host == "" || a.Token == "" || a.Name == "" {
			continue // Skip if host or token is not set
		}
		name := a.Name
		as := New(name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source)
		if err := request.ValidateURL(as.Host); err != nil {
			log.Warn().Str("arr", name).Str("host", as.Host).Err(err).Msg("Arr host URL validation failed, but adding to storage anyway")
			// Don't skip - add it anyway, validation will happen during requests
		}
		arrs[a.Name] = as
	}
	return &Storage{
		Arrs:   arrs,
		logger: log,
	}
}

func (s *Storage) AddOrUpdate(arr *Arr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arr.Host == "" || arr.Token == "" || arr.Name == "" {
		return
	}

	// If this is an auto-detected Arr, only add if no config Arr exists
	if arr.Source == "auto" {
		if existing, ok := s.Arrs[arr.Name]; ok && existing.Source != "auto" {
			// Don't overwrite config Arr with auto-detected one
			return
		}
	}

	// Only log warning for config Arrs
	if arr.Source != "auto" {
		if err := request.ValidateURL(arr.Host); err != nil {
			s.logger.Warn().Str("arr", arr.Name).Str("host", arr.Host).Err(err).Msg("Arr host URL validation failed, but adding to storage anyway")
		}
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
		if arr.Host == "" || arr.Token == "" {
			continue // skip invalid arrs
		}
		if arr.Source == "auto" && request.ValidateURL(arr.Host) != nil {
			continue // skip auto-detected arrs with invalid host
		}
		arrs = append(arrs, arr)
	}
	return arrs
}

func (s *Storage) SyncToConfig() []config.Arr {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := config.Get()
	arrConfigs := make(map[string]config.Arr)
	for _, a := range cfg.Arrs {
		if a.Host == "" || a.Token == "" {
			continue // Skip empty arrs
		}
		arrConfigs[a.Name] = a
	}

	for name, arr := range s.Arrs {
		exists, ok := arrConfigs[name]
		if ok {
			// Update existing arr config
			exists.Host = arr.Host // Always update host regardless of validation
			exists.Token = cmp.Or(exists.Token, arr.Token)
			exists.Cleanup = arr.Cleanup
			exists.SkipRepair = arr.SkipRepair
			exists.DownloadUncached = arr.DownloadUncached
			exists.SelectedDebrid = arr.SelectedDebrid
			arrConfigs[name] = exists
		} else {
			// Add new arr config
			arrConfigs[name] = config.Arr{
				Name:             arr.Name,
				Host:             arr.Host,
				Token:            arr.Token,
				Cleanup:          arr.Cleanup,
				SkipRepair:       arr.SkipRepair,
				DownloadUncached: arr.DownloadUncached,
				SelectedDebrid:   arr.SelectedDebrid,
				Source:           arr.Source,
			}
		}
	}
	// Convert map to slice
	arrs := make([]config.Arr, 0, len(arrConfigs))
	for _, a := range arrConfigs {
		arrs = append(arrs, a)
	}
	return arrs
}

func (s *Storage) SyncFromConfig(arrs []config.Arr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	arrConfigs := make(map[string]*Arr)
	for _, a := range arrs {
		arrConfigs[a.Name] = New(a.Name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source)
	}

	// Add or update arrs from config
	for name, arr := range s.Arrs {
		if ac, ok := arrConfigs[name]; ok {
			// Update existing arr from storage values
			ac.Host = arr.Host // Use storage host value
			ac.Token = cmp.Or(ac.Token, arr.Token)
			ac.Cleanup = arr.Cleanup
			ac.SkipRepair = arr.SkipRepair
			ac.DownloadUncached = arr.DownloadUncached
			ac.SelectedDebrid = arr.SelectedDebrid
			ac.Source = arr.Source
			arrConfigs[name] = ac
		} else {
			arrConfigs[name] = arr
		}
	}

	// Replace the arrs map
	s.Arrs = arrConfigs

}

func (s *Storage) StartWorker(ctx context.Context) error {

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
