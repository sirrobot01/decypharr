package config

import (
	"errors"
	"fmt"
	"runtime"
)

type Debrid struct {
	Provider                     string   `json:"provider,omitempty"` // realdebrid, alldebrid, debridlink, torbox, premiumize
	Name                         string   `json:"name,omitempty"`
	APIKey                       string   `json:"api_key,omitempty"`
	DownloadAPIKeys              []string `json:"download_api_keys,omitempty"`
	DownloadUncached             bool     `json:"download_uncached,omitempty"`
	RateLimit                    string   `json:"rate_limit,omitempty"` // 200/minute or 10/second
	RepairRateLimit              string   `json:"repair_rate_limit,omitempty"`
	DownloadRateLimit            string   `json:"download_rate_limit,omitempty"`
	Proxy                        string   `json:"proxy,omitempty"`
	UnpackRar                    bool     `json:"unpack_rar,omitempty"`
	MinimumFreeSlot              int      `json:"minimum_free_slot,omitempty"` // Minimum active pots to use this debrid
	Limit                        int      `json:"limit,omitempty"`             // Maximum number of total torrents
	TorrentsRefreshInterval      string   `json:"torrents_refresh_interval,omitempty"`
	DownloadLinksRefreshInterval string   `json:"download_links_refresh_interval,omitempty"`
	Workers                      int      `json:"workers,omitempty"`
	AutoExpireLinksAfter         string   `json:"auto_expire_links_after,omitempty"`
	UserAgent                    string   `json:"user_agent,omitempty"`

	// Folder
	Folder        string `json:"folder,omitempty"`          // Deprecated. Use Mount MountPath instead.
	FolderNaming  string `json:"folder_naming,omitempty"`   // Deprecated. Use global setting instead.
	RcUrl         string `json:"rc_url,omitempty"`          // Deprecated. Use global setting instead.
	RcUser        string `json:"rc_user,omitempty"`         // Deprecated. Use global setting instead.
	RcPass        string `json:"rc_pass,omitempty"`         // Deprecated. Use global setting instead.
	RcRefreshDirs string `json:"rc_refresh_dirs,omitempty"` // Deprecated. Use global setting instead.

	// Directories
	Directories map[string]WebdavDirectories `json:"directories,omitempty"` // Deprecated. Use global setting instead.
}

func (c *Config) updateDebrid(d Debrid) Debrid {
	workers := runtime.NumCPU() * 50
	perDebrid := workers / len(c.Debrids)

	if d.Provider == "" {
		d.Provider = d.Name
	}

	var downloadKeys []string

	if len(d.DownloadAPIKeys) > 0 {
		downloadKeys = d.DownloadAPIKeys
	} else {
		// If no download API keys are specified, use the main API key
		downloadKeys = []string{d.APIKey}
	}
	d.DownloadAPIKeys = downloadKeys

	if d.TorrentsRefreshInterval == "" {
		d.TorrentsRefreshInterval = DefaultTorrentsRefreshInterval
	}
	if d.DownloadLinksRefreshInterval == "" {
		d.DownloadLinksRefreshInterval = DefaultDownloadsRefreshInterval
	}
	if d.Workers == 0 {
		d.Workers = perDebrid
	}
	if d.AutoExpireLinksAfter == "" {
		d.AutoExpireLinksAfter = DefaultAutoExpireLinksAfter
	}

	return d
}

func validateDebrids(debrids []Debrid) error {
	if len(debrids) == 0 {
		return nil
	}

	for _, debrid := range debrids {
		// Basic field validation
		if debrid.APIKey == "" {
			return errors.New("debrid api key is required")
		}
	}

	return nil
}

func (c *Config) applyDebridEnvVars() {
	// Debrid providers array
	for i := 0; i < 10; i++ { // Support up to 10 debrid providers
		prefix := fmt.Sprintf("DEBRIDS__%d__", i)
		if val := getEnv(prefix + "NAME"); val != "" {
			// Ensure array is large enough
			if i >= len(c.Debrids) {
				c.Debrids = append(c.Debrids, make([]Debrid, i-len(c.Debrids)+1)...)
			}
			c.Debrids[i].Name = val

			// Set other debrid fields
			if apiKey := getEnv(prefix + "API_KEY"); apiKey != "" {
				c.Debrids[i].APIKey = apiKey
			}
			if folder := getEnv(prefix + "FOLDER"); folder != "" {
				c.Debrids[i].Folder = folder
			}
			if provider := getEnv(prefix + "PROVIDER"); provider != "" {
				c.Debrids[i].Provider = provider
			}
			if proxy := getEnv(prefix + "PROXY"); proxy != "" {
				c.Debrids[i].Proxy = proxy
			}
		}
	}
}
