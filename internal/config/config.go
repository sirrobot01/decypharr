package config

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	json "github.com/bytedance/sonic"
)

type (
	WebDavFolderNaming string
	MountType          string
	DownloadAction     string
	Protocol           string
)

const (
	ProtocolTorrent Protocol = "torrent"
	ProtocolNZB     Protocol = "nzb"
	ProtocolAll     Protocol = ""
)

const (
	MountTypeRclone         MountType = "rclone"
	MountTypeDFS            MountType = "dfs"
	MountTypeExternalRclone MountType = "external_rclone"
	MountTypeNone           MountType = "none"
)

const (
	DownloadActionSymlink  DownloadAction = "symlink"
	DownloadActionDownload DownloadAction = "download"
	DownloadActionStrm     DownloadAction = "strm"
	DownloadActionNone     DownloadAction = "none"
)

const (
	WebDavUseFileName          WebDavFolderNaming = "filename"
	WebDavUseOriginalName      WebDavFolderNaming = "original"
	WebDavUseFileNameNoExt     WebDavFolderNaming = "filename_no_ext"
	WebDavUseOriginalNameNoExt WebDavFolderNaming = "original_no_ext"
	WebdavUseHash              WebDavFolderNaming = "infohash"
)

var (
	instance   *Config
	once       sync.Once
	configPath string
)

// QBitTorrent is deprecated. Use Manager instead.
// Kept for backward compatibility with existing configs.
type QBitTorrent struct {
	DownloadFolder      string   `json:"download_folder,omitempty"`
	Categories          []string `json:"categories,omitempty"`
	RefreshInterval     int      `json:"refresh_interval,omitempty"`
	SkipPreCache        bool     `json:"skip_pre_cache,omitempty"`
	MaxDownloads        int      `json:"max_downloads,omitempty"`
	AlwaysRmTrackerUrls bool     `json:"always_rm_tracker_urls,omitempty"`
}

func (q QBitTorrent) IsZero() bool {
	return q.DownloadFolder == "" && len(q.Categories) == 0 && q.RefreshInterval == 0 && !q.SkipPreCache && q.MaxDownloads == 0 && !q.AlwaysRmTrackerUrls
}

type Arr struct {
	Name             string `json:"name,omitempty"`
	Host             string `json:"host,omitempty"`
	Token            string `json:"token,omitempty"`
	Cleanup          bool   `json:"cleanup,omitempty"`
	SkipRepair       bool   `json:"skip_repair,omitempty"`
	DownloadUncached *bool  `json:"download_uncached,omitempty"`
	SelectedDebrid   string `json:"selected_debrid,omitempty"`
	Source           string `json:"source,omitempty"` // The source of the arr, e.g. "auto", "config", "". Auto means it was automatically detected from the arr
}

func (a Arr) IsZero() bool {
	return a.Name == "" && a.Host == "" && a.Token == "" && !a.Cleanup && !a.SkipRepair && a.DownloadUncached == nil && a.SelectedDebrid == "" && a.Source == ""
}

type CustomFolders struct {
	Filters map[string]string `json:"filters,omitempty"`
}

type Auth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	APIToken string `json:"api_token,omitempty"`
}

// RepairSource selects where the health checker enumerates entries from.
type RepairSource string

const (
	RepairSourceArr     RepairSource = "arr"
	RepairSourceManaged RepairSource = "managed"
)

// RepairConfig is the single, global configuration for the health checker.
// When Enabled is true, a recurring sweep runs on Schedule and visits only
// entries that are unhealthy, dirty, or older than RecheckInterval.
type RepairConfig struct {
	Enabled               bool         `json:"enabled,omitempty"`
	Source                RepairSource `json:"source,omitempty"`
	Schedule              string       `json:"schedule,omitempty"`
	Workers               int          `json:"workers,omitempty"`
	NNTPConnectionPercent int          `json:"nntp_connection_percent,omitempty"`
	Strategy              string       `json:"strategy,omitempty"`
	RecheckInterval       string       `json:"recheck_interval,omitempty"`
	Arrs                  []string     `json:"arrs,omitempty"`
	AutoRepair            bool         `json:"auto_repair,omitempty"`
	NotifyOnComplete      bool         `json:"notify_on_complete,omitempty"`
}

func (r RepairConfig) IsZero() bool {
	return !r.Enabled && r.Source == "" && r.Schedule == "" && r.Workers == 0 &&
		r.NNTPConnectionPercent == 0 && r.Strategy == "" && r.RecheckInterval == "" && len(r.Arrs) == 0 &&
		!r.AutoRepair && !r.NotifyOnComplete
}

type Config struct {
	// server
	BindAddress string `json:"bind_address,omitempty"`
	URLBase     string `json:"url_base,omitempty"`
	AppURL      string `json:"app_url,omitempty"`
	Port        string `json:"port,omitempty"`

	LogLevel string   `json:"log_level,omitempty"`
	Debrids  []Debrid `json:"debrids,omitzero"`

	Arrs        []Arr       `json:"arrs,omitzero"`
	Usenet      Usenet      `json:"usenet,omitzero"`      // Usenet configuration
	QBitTorrent QBitTorrent `json:"qbittorrent,omitzero"` // Deprecated: use Manager instead
	Rclone      Rclone      `json:"rclone,omitzero"`      // Deprecated: use Mounts instead
	Mount       Mount       `json:"mount,omitzero"`

	AllowedExt         []string `json:"allowed_file_types,omitempty"`
	AllowSamples       bool     `json:"allow_samples,omitempty"`
	MinFileSize        string   `json:"min_file_size,omitempty"`
	MaxFileSize        string   `json:"max_file_size,omitempty"`
	RemoveStalledAfter string   `json:"remove_stalled_after,omitzero"`
	EnableWebdavAuth   bool     `json:"enable_webdav_auth,omitempty"`
	UseAuth            bool     `json:"use_auth,omitempty"`
	NZBUserAgent       string   `json:"nzb_user_agent,omitempty"` // User agent for downloading NZBs
	Auth               *Auth    `json:"-"`

	DisableWebDav bool `json:"disable_webdav,omitempty"`

	// Notifications configuration
	Notifications Notifications `json:"notifications,omitempty"`

	// Deprecated: Use Notifications.WebhookURL instead
	DiscordWebhook string `json:"discord_webhook_url,omitempty"`
	// Deprecated: Use Notifications.CallbackURL instead
	CallbackURL string `json:"callback_url,omitempty"`

	// Manager settings
	DownloadFolder        string                   `json:"download_folder,omitempty"`
	RefreshInterval       string                   `json:"refresh_interval,omitempty"`
	MaxDownloads          int                      `json:"max_downloads,omitempty"`
	SkipPreCache          bool                     `json:"skip_pre_cache,omitempty"`
	SkipMultiSeason       bool                     `json:"skip_multi_season,omitempty"`
	AlwaysRmTrackerUrls   bool                     `json:"always_rm_tracker_urls,omitempty"`
	Categories            []string                 `json:"categories,omitempty"`
	FolderNaming          WebDavFolderNaming       `json:"folder_naming,omitempty"`
	CustomFolders         map[string]CustomFolders `json:"custom_folders,omitempty"`
	DefaultDownloadAction DownloadAction           `json:"default_download_action,omitempty"`

	RefreshDirs  string `json:"refresh_dirs,omitempty"`
	Retries      int    `json:"retries,omitempty"`
	SkipAutoMove bool   `json:"skip_auto_move,omitempty"`

	// RateLimitRetries controls how many times processTorrentDownload will retry
	// a GetLink call that returns HTTP 429 (Too Many Requests) before failing the
	// whole job. Each retry waits with exponential backoff (30 s, 60 s, 120 s …
	// capped at 5 min) and reports the entry as "paused" to the arr client during
	// the wait so the arr does not remove or re-grab the item.
	// Set to 0 to disable the retry behaviour and fail immediately on 429.
	RateLimitRetries int `json:"rate_limit_retries,omitempty"`

	Repair RepairConfig `json:"repair,omitzero"`
}

func (c *Config) JsonFile() string {
	return filepath.Join(GetMainPath(), "config.json")
}
func (c *Config) AuthFile() string {
	return filepath.Join(GetMainPath(), "auth.json")
}

func (c *Config) TorrentsFile() string {
	return filepath.Join(GetMainPath(), "torrents.json")
}

func (c *Config) loadConfig() error {
	// Load the config file
	// Read the JSON config file directly
	configFile := c.JsonFile()
	fmt.Printf("Loading config from %s\n", configFile)
	data, err := os.ReadFile(configFile)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("Config file not found, creating a new one at %s\n", configFile)
			// Create a default config file if it doesn't exist
			if err := c.createConfig(); err != nil {
				return fmt.Errorf("failed to create config file: %w", err)
			}
			return c.Save()
		}
		return fmt.Errorf("error reading config file: %w", err)
	}

	// Parse JSON
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("error parsing config JSON: %w", err)
	}

	// Set defaults for any missing values
	c.setDefaults()

	// Apply environment variable overrides
	c.applyEnvOverrides()

	return nil
}

func (c *Config) Validate() error {
	if err := validateDebrids(c.Debrids); err != nil {
		return err
	}

	if err := validateUsenet(c.Usenet.Providers); err != nil {
		return err
	}

	if c.DownloadFolder == "" {
		return errors.New("download folder is required")
	}

	// If either debrid or usenet is enabled, at least one must be configured
	if len(c.Debrids) == 0 && len(c.Usenet.Providers) == 0 {
		return errors.New("at least one debrid provider or usenet provider must be configured")
	}

	return nil
}

// generateAPIToken creates a new random API token
func generateAPIToken() (string, error) {
	bytes := make([]byte, 32) // 256-bit token
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

func SetConfigPath(path string) {
	configPath = path
}

func GetMainPath() string {
	return configPath
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

func (c *Config) SecretKey() string {
	return cmp.Or(getEnv("SECRET_KEY"), "\"wqj(v%lj*!-+kf@4&i95rhh_!5_px5qnuwqbr%cjrvrozz_r*(\"")
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

func (c *Config) NeedsAuth() bool {
	return c.UseAuth && (c.Auth == nil || c.Auth.Username == "" || c.Auth.Password == "")
}

// migrateQBitTorrentToManager migrates deprecated QBitTorrent config to Manager
// This ensures backward compatibility with existing configs
func (c *Config) migrateQBitTorrentToManager() {
	// If Manager fields are not set but QBitTorrent fields are, migrate them
	if c.DownloadFolder == "" && c.QBitTorrent.DownloadFolder != "" {
		c.DownloadFolder = c.QBitTorrent.DownloadFolder
	}

	if len(c.Categories) == 0 && len(c.QBitTorrent.Categories) > 0 {
		c.Categories = c.QBitTorrent.Categories
	}

	if c.RefreshInterval == "" && c.QBitTorrent.RefreshInterval > 0 {
		c.RefreshInterval = fmt.Sprintf("%ds", c.QBitTorrent.RefreshInterval)
	}

	if !c.SkipPreCache && c.QBitTorrent.SkipPreCache {
		c.SkipPreCache = c.QBitTorrent.SkipPreCache
	}

	if c.MaxDownloads == 0 && c.QBitTorrent.MaxDownloads > 0 {
		c.MaxDownloads = c.QBitTorrent.MaxDownloads
	}

	if !c.AlwaysRmTrackerUrls && c.QBitTorrent.AlwaysRmTrackerUrls {
		c.AlwaysRmTrackerUrls = c.QBitTorrent.AlwaysRmTrackerUrls
	}

	// Set default download folder if not set
	if c.DownloadFolder == "" {
		c.DownloadFolder = filepath.Join(GetMainPath(), "downloads")
	}

	// Set default categories if not set
	if len(c.Categories) == 0 {
		c.Categories = []string{"sonarr", "radarr"}
	}

	// Set default refresh interval if not set
	if c.RefreshInterval == "" {
		c.RefreshInterval = "30s"
	}
}

// migrateNotifications migrates deprecated DiscordWebhook and CallbackURL to Notifications
// This ensures backward compatibility with existing configs
func (c *Config) migrateNotifications() {
	// Migrate deprecated webhook URL to Notifications
	if c.Notifications.WebhookURL == "" && c.DiscordWebhook != "" {
		c.Notifications.WebhookURL = c.DiscordWebhook
		c.Notifications.Enabled = true
	}

	// Migrate deprecated callback URL to Notifications
	if c.Notifications.CallbackURL == "" && c.CallbackURL != "" {
		c.Notifications.CallbackURL = c.CallbackURL
		c.Notifications.Enabled = true
	}

	// Auto-enable notifications if any URL is configured
	if c.Notifications.WebhookURL != "" || c.Notifications.CallbackURL != "" {
		c.Notifications.Enabled = true
	}
}

func (c *Config) setDefaults() {
	// Migrate deprecated fields to Manager (backward compatibility)
	c.migrateQBitTorrentToManager()
	c.migrateNotifications()

	if c.DefaultDownloadAction == "" {
		c.DefaultDownloadAction = DownloadActionSymlink
	}

	for i, debrid := range c.Debrids {
		c.Debrids[i] = c.updateDebrid(debrid)
	}

	// Set usenet defaults
	c.updateUsenetConfig()

	firstDebrid := Debrid{}
	if len(c.Debrids) > 0 {
		firstDebrid = c.Debrids[0]
	}

	if c.Mount.Type == "" {
		if c.Rclone.Enabled {
			c.Mount.Type = MountTypeRclone
			c.Mount.Rclone = c.Rclone
		}
	}

	if c.Mount.MountPath == "" {
		// Set MountPath from debridConfig.Folder by splliting it
		// debrid.Folder is usually {mount_path}/{debrid_name}/__all__ or {mount_path}/{debrid_name}/torrents
		if len(c.Debrids) > 0 {
			folder := filepath.Clean(firstDebrid.Folder)
			c.Mount.MountPath = filepath.Dir(folder)
		}
	}

	// Move WebDav global settings to Manager if not set
	if c.Mount.ExternalRclone.RCUrl == "" {
		c.Mount.ExternalRclone.RCUrl = firstDebrid.RcUrl
	}
	if c.Mount.ExternalRclone.RCUsername == "" {
		c.Mount.ExternalRclone.RCUsername = firstDebrid.RcUser
	}
	if c.Mount.ExternalRclone.RCPassword == "" {
		c.Mount.ExternalRclone.RCPassword = firstDebrid.RcPass
	}

	if c.FolderNaming == "" {
		c.FolderNaming = WebDavFolderNaming(firstDebrid.FolderNaming)
	}

	// Set default allowed extensions if not set in Manager
	if len(c.AllowedExt) == 0 {
		c.AllowedExt = getDefaultExtensions()
	}

	// Set default error threshold for multi-debrid switching
	if c.Retries == 0 {
		c.Retries = 3 // Default to 3 consecutive errors before switching
	}

	if c.RateLimitRetries == 0 {
		c.RateLimitRetries = 3 // Default: retry up to 3 times on HTTP 429 with 30s/60s/120s backoff
	}

	// Basic defaults
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

	if c.Port == "" {
		c.Port = DefaultPort
	}

	if c.LogLevel == "" {
		c.LogLevel = DefaultLogLevel
	}

	// Rclone defaults
	if c.Mount.Type == MountTypeRclone {
		c.Mount.Rclone.Port = cmp.Or(c.Rclone.Port, DefaultRclonePort)
		if c.Mount.Rclone.AsyncRead == nil {
			_asyncTrue := true
			c.Mount.Rclone.AsyncRead = &_asyncTrue
		}
		c.Mount.Rclone.VfsCacheMode = cmp.Or(c.Mount.Rclone.VfsCacheMode, "off")
		if c.Mount.Rclone.UID == 0 {
			c.Mount.Rclone.UID = uint32(os.Getuid())
		}
		if c.Mount.Rclone.GID == 0 {
			if runtime.GOOS == "windows" {
				// On Windows, we use the current user's SID as GID
				c.Mount.Rclone.GID = uint32(os.Getuid()) // Windows does not have GID, using UID instead
			} else {
				c.Mount.Rclone.GID = uint32(os.Getgid())
			}
		}
		if c.Mount.Rclone.Transfers == 0 {
			c.Mount.Rclone.Transfers = 4 // Default number of transfers
		}
		if c.Mount.Rclone.VfsCacheMode != "off" {
			c.Mount.Rclone.VfsCachePollInterval = cmp.Or(c.Rclone.VfsCachePollInterval, "1m") // Clean cache every minute
		}
		c.Mount.Rclone.DirCacheTime = cmp.Or(c.Rclone.DirCacheTime, "5m")
		c.Mount.Rclone.LogLevel = cmp.Or(c.Rclone.LogLevel, strings.ToUpper(DefaultLogLevel))
	}

	// DFS defaults
	if c.Mount.Type == MountTypeDFS {
		if c.Mount.DFS.ChunkSize == "" {
			c.Mount.DFS.ChunkSize = DefaultDFSChunkSize
		}
		if c.Mount.DFS.ReadAheadSize == "" {
			c.Mount.DFS.ReadAheadSize = DefaultDFSReadAheadSize
		}
		if c.Mount.DFS.CacheExpiry == "" {
			c.Mount.DFS.CacheExpiry = DefaultDFSCacheExpiry
		}
		if c.Mount.DFS.DiskCacheSize == "" {
			c.Mount.DFS.DiskCacheSize = DefaultDFSDiskCacheSize
		}

		if c.Mount.DFS.UID == 0 {
			c.Mount.DFS.UID = uint32(os.Getuid())
		}
		if c.Mount.DFS.GID == 0 {
			if runtime.GOOS == "windows" {
				// On Windows, we use the current user's SID as GID
				c.Mount.DFS.GID = uint32(os.Getuid()) // Windows does not have GID, using UID instead
			} else {
				c.Mount.DFS.GID = uint32(os.Getgid())
			}
		}
	}
	// Load the auth file
	c.Auth = c.GetAuth()

	// Generate API token if auth is enabled and no token exists
	if c.UseAuth {
		if c.Auth == nil {
			c.Auth = &Auth{}
		}
		if c.Auth.APIToken == "" {
			if token, err := generateAPIToken(); err == nil {
				c.Auth.APIToken = token
				// Save the updated auth config
				_ = c.SaveAuth(c.Auth)
			}
		}
	}

	// Set folder naming from first debrid if available
	if len(c.Debrids) > 0 && c.FolderNaming == "" {
		c.FolderNaming = WebDavFolderNaming(c.Debrids[0].FolderNaming)
	}

	c.applyRepairDefaults()
}

func (c *Config) applyRepairDefaults() {
	if c.Repair.Source == "" {
		c.Repair.Source = RepairSourceArr
	}
	if c.Repair.Workers <= 0 {
		c.Repair.Workers = 5
	}
	if c.Repair.Strategy == "" {
		c.Repair.Strategy = "per_entry"
	}
	if c.Repair.RecheckInterval == "" {
		c.Repair.RecheckInterval = "168h"
	}

	if c.Repair.NNTPConnectionPercent == 0 {
		c.Repair.NNTPConnectionPercent = 20
	}
}

func (c *Config) Save() error {
	c.setDefaults()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(c.JsonFile(), data, 0644); err != nil {
		fmt.Printf("Failed to write config file: %v\n", err)
		return err
	}
	return nil
}

func Reset() {
	once = sync.Once{}
	instance = nil
}

func (c *Config) createConfig() error {
	// Create the directory if it doesn't exist
	if err := os.MkdirAll(GetMainPath(), 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}
	c.URLBase = "/"
	c.Port = DefaultPort
	c.LogLevel = DefaultLogLevel
	c.UseAuth = true
	return nil
}

func (c *Config) SetupComplete() error {
	return c.Validate()
}

func (c *Config) SetupError() string {
	if err := c.Validate(); err != nil {
		return err.Error()
	}
	return ""
}
