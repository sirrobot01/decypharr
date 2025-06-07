package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	instance   *Config
	once       sync.Once
	configPath string
)

type Debrid struct {
	Name             string   `json:"name,omitempty"`
	APIKey           string   `json:"api_key,omitempty"`
	DownloadAPIKeys  []string `json:"download_api_keys,omitempty"`
	Folder           string   `json:"folder,omitempty"`
	DownloadUncached bool     `json:"download_uncached,omitempty"`
	CheckCached      bool     `json:"check_cached,omitempty"`
	RateLimit        string   `json:"rate_limit,omitempty"` // 200/minute or 10/second
	Proxy            string   `json:"proxy,omitempty"`
	UnpackRar        bool     `json:"unpack_rar,omitempty"`
	AddSamples       bool     `json:"add_samples,omitempty"`
	MinimumFreeSlot  int      `json:"minimum_free_slot,omitempty"` // Minimum active pots to use this debrid

	UseWebDav bool `json:"use_webdav,omitempty"`
	WebDav
}

type QBitTorrent struct {
	Username        string   `json:"username,omitempty"`
	Password        string   `json:"password,omitempty"`
	Port            string   `json:"port,omitempty"` // deprecated
	DownloadFolder  string   `json:"download_folder,omitempty"`
	Categories      []string `json:"categories,omitempty"`
	RefreshInterval int      `json:"refresh_interval,omitempty"`
	SkipPreCache    bool     `json:"skip_pre_cache,omitempty"`
	MaxDownloads    int      `json:"max_downloads,omitempty"`
}

type Arr struct {
	Name             string `json:"name,omitempty"`
	Host             string `json:"host,omitempty"`
	Token            string `json:"token,omitempty"`
	Cleanup          bool   `json:"cleanup,omitempty"`
	SkipRepair       bool   `json:"skip_repair,omitempty"`
	DownloadUncached *bool  `json:"download_uncached,omitempty"`
}

type Repair struct {
	Enabled     bool   `json:"enabled,omitempty"`
	Interval    string `json:"interval,omitempty"`
	RunOnStart  bool   `json:"run_on_start,omitempty"`
	ZurgURL     string `json:"zurg_url,omitempty"`
	AutoProcess bool   `json:"auto_process,omitempty"`
	UseWebDav   bool   `json:"use_webdav,omitempty"`
	Workers     int    `json:"workers,omitempty"`
	ReInsert    bool   `json:"reinsert,omitempty"`
}

type Auth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

type Config struct {
	// server
	BindAddress string `json:"bind_address,omitempty"`
	URLBase     string `json:"url_base,omitempty"`
	Port        string `json:"port,omitempty"`

	LogLevel       string      `json:"log_level,omitempty"`
	Debrids        []Debrid    `json:"debrids,omitempty"`
	QBitTorrent    QBitTorrent `json:"qbittorrent,omitempty"`
	Arrs           []Arr       `json:"arrs,omitempty"`
	Repair         Repair      `json:"repair,omitempty"`
	WebDav         WebDav      `json:"webdav,omitempty"`
	AllowedExt     []string    `json:"allowed_file_types,omitempty"`
	MinFileSize    string      `json:"min_file_size,omitempty"` // Minimum file size to download, 10MB, 1GB, etc
	MaxFileSize    string      `json:"max_file_size,omitempty"` // Maximum file size to download (0 means no limit)
	Path           string      `json:"-"`                       // Path to save the config file
	UseAuth        bool        `json:"use_auth,omitempty"`
	Auth           *Auth       `json:"-"`
	DiscordWebhook string      `json:"discord_webhook_url,omitempty"`
}

func (c *Config) JsonFile() string {
	return filepath.Join(c.Path, "config.json")
}
func (c *Config) AuthFile() string {
	return filepath.Join(c.Path, "auth.json")
}

func (c *Config) TorrentsFile() string {
	return filepath.Join(c.Path, "torrents.json")
}

func (c *Config) loadConfig() error {
	// Load the config file
	if configPath == "" {
		return fmt.Errorf("config path not set")
	}
	c.Path = configPath
	file, err := os.ReadFile(c.JsonFile())
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Config file not found, creating a new one at %s\n", c.JsonFile())
			// Create a default config file if it doesn't exist
			if err := c.createConfig(c.Path); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}
			return c.Save()
		}
		return fmt.Errorf("error reading config file: %w", err)
	}

	if err := json.Unmarshal(file, &c); err != nil {
		return fmt.Errorf("error unmarshaling config: %w", err)
	}
	c.setDefaults()
	return nil
}

func validateDebrids(debrids []Debrid) error {
	if len(debrids) == 0 {
		return errors.New("no debrids configured")
	}

	for _, debrid := range debrids {
		// Basic field validation
		if debrid.APIKey == "" {
			return errors.New("debrid api key is required")
		}
		if debrid.Folder == "" {
			return errors.New("debrid folder is required")
		}
	}

	return nil
}

func validateQbitTorrent(config *QBitTorrent) error {
	if config.DownloadFolder == "" {
		return errors.New("qbittorent download folder is required")
	}
	if _, err := os.Stat(config.DownloadFolder); os.IsNotExist(err) {
		return fmt.Errorf("qbittorent download folder(%s) does not exist", config.DownloadFolder)
	}
	return nil
}

func validateRepair(config *Repair) error {
	if !config.Enabled {
		return nil
	}
	if config.Interval == "" {
		return errors.New("repair interval is required")
	}
	return nil
}

func ValidateConfig(config *Config) error {
	// Run validations concurrently

	if err := validateDebrids(config.Debrids); err != nil {
		return err
	}

	if err := validateQbitTorrent(&config.QBitTorrent); err != nil {
		return err
	}

	if err := validateRepair(&config.Repair); err != nil {
		return err
	}

	return nil
}

func SetConfigPath(path string) {
	configPath = path
}

func Get() *Config {
	once.Do(func() {
		instance = &Config{} // Initialize instance first
		if err := instance.loadConfig(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "configuration Error: %v\n", err)
			os.Exit(1)
		}
	})
	return instance
}

func (c *Config) GetMinFileSize() int64 {
	// 0 means no limit
	if c.MinFileSize == "" {
		return 0
	}
	s, err := ParseSize(c.MinFileSize)
	if err != nil {
		return 0
	}
	return s
}

func (c *Config) GetMaxFileSize() int64 {
	// 0 means no limit
	if c.MaxFileSize == "" {
		return 0
	}
	s, err := ParseSize(c.MaxFileSize)
	if err != nil {
		return 0
	}
	return s
}

func (c *Config) IsSizeAllowed(size int64) bool {
	if size == 0 {
		return true // Maybe the debrid hasn't reported the size yet
	}
	if c.GetMinFileSize() > 0 && size < c.GetMinFileSize() {
		return false
	}
	if c.GetMaxFileSize() > 0 && size > c.GetMaxFileSize() {
		return false
	}
	return true
}

func (c *Config) GetAuth() *Auth {
	if !c.UseAuth {
		return nil
	}
	if c.Auth == nil {
		c.Auth = &Auth{}
		if _, err := os.Stat(c.AuthFile()); err == nil {
			file, err := os.ReadFile(c.AuthFile())
			if err == nil {
				_ = json.Unmarshal(file, c.Auth)
			}
		}
	}
	return c.Auth
}

func (c *Config) SaveAuth(auth *Auth) error {
	c.Auth = auth
	data, err := json.Marshal(auth)
	if err != nil {
		return err
	}
	return os.WriteFile(c.AuthFile(), data, 0644)
}

func (c *Config) NeedsSetup() error {
	return ValidateConfig(c)
}

func (c *Config) NeedsAuth() bool {
	if c.UseAuth {
		return c.GetAuth().Username == ""
	}
	return false
}

func (c *Config) updateDebrid(d Debrid) Debrid {
	workers := runtime.NumCPU() * 50
	perDebrid := workers / len(c.Debrids)

	if len(d.DownloadAPIKeys) == 0 {
		d.DownloadAPIKeys = append(d.DownloadAPIKeys, d.APIKey)
	}

	if !d.UseWebDav {
		return d
	}

	if d.TorrentsRefreshInterval == "" {
		d.TorrentsRefreshInterval = cmp.Or(c.WebDav.TorrentsRefreshInterval, "15s") // 15 seconds
	}
	if d.WebDav.DownloadLinksRefreshInterval == "" {
		d.DownloadLinksRefreshInterval = cmp.Or(c.WebDav.DownloadLinksRefreshInterval, "40m") // 40 minutes
	}
	if d.Workers == 0 {
		d.Workers = perDebrid
	}
	if d.FolderNaming == "" {
		d.FolderNaming = cmp.Or(c.WebDav.FolderNaming, "original_no_ext")
	}
	if d.AutoExpireLinksAfter == "" {
		d.AutoExpireLinksAfter = cmp.Or(c.WebDav.AutoExpireLinksAfter, "3d") // 2 days
	}

	// Merge debrid specified directories with global directories

	directories := c.WebDav.Directories
	if directories == nil {
		directories = make(map[string]WebdavDirectories)
	}

	for name, dir := range d.Directories {
		directories[name] = dir
	}
	d.Directories = directories

	d.RcUrl = cmp.Or(d.RcUrl, c.WebDav.RcUrl)
	d.RcUser = cmp.Or(d.RcUser, c.WebDav.RcUser)
	d.RcPass = cmp.Or(d.RcPass, c.WebDav.RcPass)

	return d
}

func (c *Config) setDefaults() {
	for i, debrid := range c.Debrids {
		c.Debrids[i] = c.updateDebrid(debrid)
	}

	if len(c.AllowedExt) == 0 {
		c.AllowedExt = getDefaultExtensions()
	}

	c.Port = cmp.Or(c.Port, c.QBitTorrent.Port)

	if c.URLBase == "" {
		c.URLBase = "/"
	}
	// validate url base starts with /
	if !strings.HasPrefix(c.URLBase, "/") {
		c.URLBase = "/" + c.URLBase
	}
	if !strings.HasSuffix(c.URLBase, "/") {
		c.URLBase += "/"
	}

	// Load the auth file
	c.Auth = c.GetAuth()
}

func (c *Config) Save() error {

	c.setDefaults()

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(c.JsonFile(), data, 0644); err != nil {
		return err
	}
	return nil
}

func (c *Config) createConfig(path string) error {
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	c.Path = path
	c.URLBase = "/"
	c.Port = "8282"
	c.LogLevel = "info"
	c.UseAuth = true
	c.QBitTorrent = QBitTorrent{
		DownloadFolder:  filepath.Join(path, "downloads"),
		Categories:      []string{"sonarr", "radarr"},
		RefreshInterval: 15,
	}
	return nil
}

// Reload forces a reload of the configuration from disk
func Reload() {
	instance = nil
	once = sync.Once{}
}

func DefaultFreeSlot() int {
	return 10
}
