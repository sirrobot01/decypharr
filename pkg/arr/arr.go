package arr

import (
	"bytes"
	"cmp"
	"fmt"
	json "github.com/bytedance/sonic"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/request"
	"github.com/sirrobot01/decypharr/internal/utils"
	"golang.org/x/sync/singleflight"
)

// Type is a type of arr
type Type string

type Source string

const (
	SourceAuto   Source = "auto"
	SourceManual Source = "manual"
)

var (
	sharedOnce   sync.Once
	sharedClient *request.Client
)

func getSharedClient() *request.Client {
	sharedOnce.Do(func() {
		sharedClient = request.Default()
	})
	return sharedClient
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
	Source           Source `json:"source,omitempty"`          // The source of the arr, e.g. "auto", "manual". Auto means it was automatically detected from the arr
}

type DownloadClientSchema struct {
	RemoveFailedDownloads bool `json:"removeFailedDownloads"`
}

func New(name, host, token string, cleanup, skipRepair bool, downloadUncached *bool, selectedDebrid, source string) *Arr {
	return &Arr{
		Name:             name,
		Host:             host,
		Token:            strings.TrimSpace(token),
		Type:             inferType(host, name),
		Cleanup:          cleanup,
		SkipRepair:       skipRepair,
		DownloadUncached: downloadUncached,
		SelectedDebrid:   selectedDebrid,
		Source:           Source(source),
	}
}

func (a *Arr) Request(method, endpoint string, payload interface{}, res any) (*http.Response, error) {
	if a.Token == "" || a.Host == "" {
		return nil, fmt.Errorf("arr not configured")
	}
	url, err := utils.JoinURL(a.Host, endpoint)
	if err != nil {
		return nil, err
	}

	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal payload: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", a.Token)

	resp, err := getSharedClient().Do(req)
	if err != nil {
		return nil, err
	}

	// Parse success result if provided
	if res != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return resp, fmt.Errorf("failed to read response: %w", err)
		}
		if len(respBody) > 0 {
			if err := json.Unmarshal(respBody, res); err != nil {
				return resp, fmt.Errorf("failed to unmarshal response: %w", err)
			}
		}
	}

	return resp, nil
}

func (a *Arr) Validate() error {
	if a.Token == "" || a.Host == "" {
		return fmt.Errorf("arr not configured")
	}

	if utils.ValidateURL(a.Host) != nil {
		return fmt.Errorf("invalid arr host URL")
	}
	resp, err := a.Request("GET", "/api/v3/health", nil, nil)
	if err != nil {
		return err
	}
	if resp.Body != nil {
		defer resp.Body.Close()
	}
	// If response is not 200 or 404(this is the case for Lidarr, etc), return an error
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("failed to validate arr %s: %s", a.Name, resp.Status)
	}
	return nil
}

func (a *Arr) GetRemoveFailedDownloads() bool {
	var data []DownloadClientSchema
	resp, err := a.Request(http.MethodGet, "api/v3/downloadclient", nil, &data)
	if err != nil || resp.StatusCode != http.StatusOK || len(data) == 0 {
		return false
	}

	for _, client := range data {
		if client.RemoveFailedDownloads {
			return true
		}
	}

	return false
}

type Storage struct {
	arrs   *xsync.Map[string, *Arr]
	logger zerolog.Logger
	sg     singleflight.Group
}

func (s *Storage) Cleanup() {
	s.arrs.Clear()
}

func NewStorage() *Storage {
	s := &Storage{
		logger: logger.New("arr"),
		arrs:   xsync.NewMap[string, *Arr](),
	}
	for _, a := range config.Get().Arrs {
		if a.Host == "" || a.Token == "" || a.Name == "" {
			continue // Skip if host or token is not set
		}
		name := a.Name
		as := New(name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source)
		if utils.ValidateURL(as.Host) != nil {
			continue
		}
		s.arrs.Store(name, as)
	}
	return s
}

func (s *Storage) AddOrUpdate(arr *Arr) {
	if arr.Host == "" || arr.Token == "" || arr.Name == "" {
		return
	}

	// Check the host URL
	if utils.ValidateURL(arr.Host) != nil {
		return
	}
	s.arrs.Store(arr.Name, arr)
}

func (s *Storage) GetOrCreate(name string) *Arr {
	if name == "" {
		name = "uncategorized"
	}
	arr, exists := s.arrs.Load(name)
	if !exists {
		return New(name, "", "", false, false, nil, "", "manual")
	}
	return arr
}

func (s *Storage) Get(name string) *Arr {
	a, ok := s.arrs.Load(name)
	if !ok {
		return nil
	}
	return a
}

func (s *Storage) GetAll() []*Arr {
	arrs := make([]*Arr, 0, s.arrs.Size())
	s.arrs.Range(func(key string, value *Arr) bool {
		arrs = append(arrs, value)
		return true
	})
	return arrs
}

func (s *Storage) SyncToConfig() []config.Arr {
	cfg := config.Get()
	arrConfigs := make(map[string]config.Arr)
	for _, a := range cfg.Arrs {
		if a.Host == "" || a.Token == "" {
			continue // Skip empty arrs
		}
		arrConfigs[a.Name] = a
	}

	s.arrs.Range(func(name string, arr *Arr) bool {
		exists, ok := arrConfigs[name]
		if ok {
			// Update existing arr config
			// Check if the host URL is valid
			if utils.ValidateURL(arr.Host) == nil {
				exists.Host = arr.Host
			}
			exists.Token = cmp.Or(exists.Token, arr.Token)
			exists.Cleanup = arr.Cleanup
			exists.SkipRepair = arr.SkipRepair
			exists.DownloadUncached = arr.DownloadUncached
			exists.SelectedDebrid = arr.SelectedDebrid
			arrConfigs[name] = exists
		} else {
			// AddOrUpdate new arr config
			arrConfigs[name] = config.Arr{
				Name:             arr.Name,
				Host:             arr.Host,
				Token:            arr.Token,
				Cleanup:          arr.Cleanup,
				SkipRepair:       arr.SkipRepair,
				DownloadUncached: arr.DownloadUncached,
				SelectedDebrid:   arr.SelectedDebrid,
				Source:           string(arr.Source),
			}
		}
		return true
	})
	// Convert map to slice
	arrs := make([]config.Arr, 0, len(arrConfigs))
	for _, a := range arrConfigs {
		arrs = append(arrs, a)
	}
	return arrs
}

func (s *Storage) SyncFromConfig(arrs []config.Arr) {
	newMaps := xsync.NewMap[string, *Arr]()
	for _, a := range arrs {
		newMaps.Store(a.Name, New(a.Name, a.Host, a.Token, a.Cleanup, a.SkipRepair, a.DownloadUncached, a.SelectedDebrid, a.Source))
	}

	// AddOrUpdate or update arrs from config
	s.arrs.Range(func(name string, arr *Arr) bool {
		if ac, ok := newMaps.Load(name); ok {
			// Update existing arr
			// is the host URL valid?
			if utils.ValidateURL(ac.Host) == nil {
				ac.Host = arr.Host
			}
			ac.Token = cmp.Or(ac.Token, arr.Token)
			ac.Cleanup = arr.Cleanup
			ac.SkipRepair = arr.SkipRepair
			ac.DownloadUncached = arr.DownloadUncached
			ac.SelectedDebrid = arr.SelectedDebrid
			ac.Source = arr.Source
			newMaps.Store(name, ac)
		} else {
			newMaps.Store(name, arr)
		}
		return true
	})
	s.arrs = newMaps
}

func (s *Storage) Monitor() {
	wg := sync.WaitGroup{}
	wg.Add(s.arrs.Size())
	s.arrs.Range(func(name string, arr *Arr) bool {
		_, _, _ = s.sg.Do(fmt.Sprintf("cleanup_%s", arr.Name), func() (interface{}, error) {
			go func() {
				defer wg.Done()
				if err := arr.CleanupQueue(); err != nil {
					s.logger.Error().Err(err).Msgf("Failed to cleanup arr %s", arr.Name)
				}
			}()
			return nil, nil
		})
		return true
	})
	wg.Wait()
}

func (a *Arr) Refresh() {
	payload := struct {
		Name string `json:"name"`
	}{
		Name: "RefreshMonitoredDownloads",
	}

	_, _ = a.Request(http.MethodPost, "api/v3/command", payload, nil)
}

type filesystemFile struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type filesystemListing struct {
	Path  string           `json:"path"`
	Files []filesystemFile `json:"files"`
}

// CanSeePath checks whether arr can see the provided path via its filesystem endpoint.
func (a *Arr) CanSeePath(path string, expectedFiles []string) (bool, []filesystemFile, error) {
	params := url.Values{}
	params.Set("path", path)
	params.Set("includeFiles", "true")
	params.Set("allowFoldersWithoutTrailingSlashes", "true")
	endpoint := "api/v3/filesystem?" + params.Encode()

	resp, err := a.Request(http.MethodGet, endpoint, nil, nil)
	if err != nil {
		return false, nil, err
	}
	if resp.Body == nil {
		return false, nil, fmt.Errorf("filesystem check failed: empty response body")
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return false, nil, fmt.Errorf("filesystem check failed: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, fmt.Errorf("filesystem check failed to read response: %w", err)
	}

	var listings []filesystemListing
	if err := json.Unmarshal(body, &listings); err != nil {
		var single filesystemListing
		if errSingle := json.Unmarshal(body, &single); errSingle != nil {
			return false, nil, fmt.Errorf("filesystem check failed to parse response: %w", err)
		}
		listings = []filesystemListing{single}
	}

	if len(listings) == 0 {
		return false, nil, nil
	}

	allFiles := make([]filesystemFile, 0)
	for _, listing := range listings {
		allFiles = append(allFiles, listing.Files...)
	}

	if len(expectedFiles) == 0 {
		for _, listing := range listings {
			if len(listing.Files) > 0 || strings.TrimRight(listing.Path, "/") == strings.TrimRight(path, "/") {
				return true, allFiles, nil
			}
		}
		return false, allFiles, nil
	}

	seenCounts := make(map[string]int)
	for _, listing := range listings {
		for _, f := range listing.Files {
			name := strings.ToLower(strings.TrimSpace(f.Name))
			if name == "" {
				name = strings.ToLower(strings.TrimSpace(filepath.Base(f.Path)))
			}
			if name != "" {
				seenCounts[name]++
			}
		}
	}

	expectedCounts := make(map[string]int)

	for _, expected := range expectedFiles {
		name := strings.ToLower(strings.TrimSpace(filepath.Base(expected)))
		if name == "" {
			continue
		}
		expectedCounts[name]++
	}

	for name, expectedCount := range expectedCounts {
		if seenCounts[name] < expectedCount {
			return false, allFiles, nil
		}
	}

	return true, allFiles, nil
}

func inferType(host, name string) Type {
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
