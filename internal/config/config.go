package config

import (
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

type RepairStrategy string

const (
	RepairStrategyPerFile    RepairStrategy = "per_file"
	RepairStrategyPerTorrent RepairStrategy = "per_torrent"
)

var (
	instance   *Config
	once       sync.Once
	configPath string
)

type Debrid struct {
	Name              string   `json:"name,omitempty"`
	APIKey            string   `json:"api_key,omitempty"`
	DownloadAPIKeys   []string `json:"download_api_keys,omitempty"`
	Folder            string   `json:"folder,omitempty"`
	RcloneMountPath   string   `json:"rclone_mount_path,omitempty"` // Custom rclone mount path for this debrid service
	DownloadUncached  bool     `json:"download_uncached,omitempty"`
	CheckCached       bool     `json:"check_cached,omitempty"`
	RateLimit         string   `json:"rate_limit,omitempty"` // 200/minute or 10/second
	RepairRateLimit   string   `json:"repair_rate_limit,omitempty"`
	DownloadRateLimit string   `json:"download_rate_limit,omitempty"`
	Proxy             string   `json:"proxy,omitempty"`
	UnpackRar         bool     `json:"unpack_rar,omitempty"`
	AddSamples        bool     `json:"add_samples,omitempty"`
	MinimumFreeSlot   int      `json:"minimum_free_slot,omitempty"` // Minimum active pots to use this debrid
	Limit             int      `json:"limit,omitempty"`             // Maximum number of total torrents

	UseWebDav bool `json:"use_webdav,omitempty"`
	WebDav
}

type QBitTorrent struct {
	Username          	string   `json:"username,omitempty"`
	Password          	string   `json:"password,omitempty"`
	Port              	string   `json:"port,omitempty"` // deprecated
	DownloadFolder    	string   `json:"download_folder,omitempty"`
	Categories        	[]string `json:"categories,omitempty"`
	RefreshInterval   	int      `json:"refresh_interval,omitempty"`
	SkipPreCache      	bool     `json:"skip_pre_cache,omitempty"`
	MaxDownloads      	int      `json:"max_downloads,omitempty"`
	AlwaysRmTrackerUrls bool     `json:"always_rm_tracker_urls,omitempty"`
}

type LocalCache struct {
	Enabled bool `json:"enabled,omitempty"`
	// CacheDirs are the directories scanned for symlinks to replace with local
	// copies. Defaults to the qBittorrent download folder + its categories.
	// Point this at the *media library* when the arrs import the symlink itself:
	// the download folder is emptied/bypassed on import, so caching it would use
	// disk without ever changing what the media server actually reads.
	CacheDirs        []string `json:"cache_dirs,omitempty"`
	RcloneIndicator  string   `json:"rclone_indicator,omitempty"`  // Substring in symlink target that identifies rclone mounts (e.g. "decypharr/realdebrid")
	MinFreeMB        int      `json:"min_free_mb,omitempty"`       // Stop downloading below this free space (default: 20000 = 20GB)
	StaleHours       int      `json:"stale_hours,omitempty"`       // Remove .part files older than this (default: 24)
	MaxParallel      int      `json:"max_parallel,omitempty"`      // Max simultaneous file copies (default: 2)
	ScanInterval     int      `json:"scan_interval,omitempty"`     // Seconds between background scans for missed symlinks (default: 1800)
	UseDebridCDN     bool     `json:"use_debrid_cdn,omitempty"`    // Try to download directly from debrid CDN instead of FUSE copy
	Aria2Connections int      `json:"aria2_connections,omitempty"` // Parallel connections per download when using CDN (default: 16)
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

type Repair struct {
	Enabled     bool           `json:"enabled,omitempty"`
	Interval    string         `json:"interval,omitempty"`
	ZurgURL     string         `json:"zurg_url,omitempty"`
	AutoProcess bool           `json:"auto_process,omitempty"`
	UseWebDav   bool           `json:"use_webdav,omitempty"`
	Workers     int            `json:"workers,omitempty"`
	ReInsert    bool           `json:"reinsert,omitempty"`
	Strategy    RepairStrategy `json:"strategy,omitempty"`
}

type Auth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	APIToken string `json:"api_token,omitempty"`
}

type Rclone struct {
	// Global mount folder where all providers will be mounted as subfolders
	Enabled   bool   `json:"enabled,omitempty"`
	MountPath string `json:"mount_path,omitempty"`
	RcPort    string `json:"rc_port,omitempty"`

	// Cache settings
	CacheDir string `json:"cache_dir,omitempty"`

	// VFS settings
	VfsCacheMode          string `json:"vfs_cache_mode,omitempty"`            // off, minimal, writes, full
	VfsCacheMaxAge        string `json:"vfs_cache_max_age,omitempty"`         // Maximum age of objects in the cache (default 1h)
	VfsDiskSpaceTotal     string `json:"vfs_disk_space_total,omitempty"`      // Total disk space available for the cache (default off)
	VfsCacheMaxSize       string `json:"vfs_cache_max_size,omitempty"`        // Maximum size of the cache (default off)
	VfsCachePollInterval  string `json:"vfs_cache_poll_interval,omitempty"`   // How often to poll for changes (default 1m)
	VfsReadChunkSize      string `json:"vfs_read_chunk_size,omitempty"`       // Read chunk size (default 128M)
	VfsReadChunkSizeLimit string `json:"vfs_read_chunk_size_limit,omitempty"` // Max chunk size (default off)
	VfsReadAhead          string `json:"vfs_read_ahead,omitempty"`            // read ahead size
	BufferSize            string `json:"buffer_size,omitempty"`               // Buffer size for reading files (default 16M)
	BwLimit               string `json:"bw_limit,omitempty"`                  // Bandwidth limit (default off)

	VfsCacheMinFreeSpace string `json:"vfs_cache_min_free_space,omitempty"`
	VfsFastFingerprint   bool   `json:"vfs_fast_fingerprint,omitempty"`
	VfsReadChunkStreams  int    `json:"vfs_read_chunk_streams,omitempty"`
	AsyncRead            *bool  `json:"async_read,omitempty"` // Use async read for files
	Transfers            int    `json:"transfers,omitempty"`  // Number of transfers to use (default 4)
	UseMmap              bool   `json:"use_mmap,omitempty"`

	// File system settings
	UID   uint32 `json:"uid,omitempty"` // User ID for mounted files
	GID   uint32 `json:"gid,omitempty"` // Group ID for mounted files
	Umask string `json:"umask,omitempty"`

	// Timeout settings
	AttrTimeout  string `json:"attr_timeout,omitempty"`   // Attribute cache timeout (default 1s)
	DirCacheTime string `json:"dir_cache_time,omitempty"` // Directory cache time (default 5m)

	// Performance settings
	NoModTime  bool `json:"no_modtime,omitempty"`  // Don't read/write modification time
	NoChecksum bool `json:"no_checksum,omitempty"` // Don't checksum files on upload

	LogLevel string `json:"log_level,omitempty"`
}

type Config struct {
	// server
	BindAddress string `json:"bind_address,omitempty"`
	URLBase     string `json:"url_base,omitempty"`
	Port        string `json:"port,omitempty"`

	LogLevel           string      `json:"log_level,omitempty"`
	Debrids            []Debrid    `json:"debrids,omitempty"`
	QBitTorrent        QBitTorrent `json:"qbittorrent,omitempty"`
	Arrs               []Arr       `json:"arrs,omitempty"`
	Repair             Repair      `json:"repair,omitempty"`
	WebDav             WebDav      `json:"webdav,omitempty"`
	Rclone             Rclone      `json:"rclone,omitempty"`
	LocalCache         LocalCache  `json:"local_cache,omitempty"`
	AllowedExt         []string    `json:"allowed_file_types,omitempty"`
	MinFileSize        string      `json:"min_file_size,omitempty"` // Minimum file size to download, 10MB, 1GB, etc
	MaxFileSize        string      `json:"max_file_size,omitempty"` // Maximum file size to download (0 means no limit)
	Path               string      `json:"-"`                       // Path to save the config file
	UseAuth            bool        `json:"use_auth,omitempty"`
	Auth               *Auth       `json:"-"`
	DiscordWebhook     string      `json:"discord_webhook_url,omitempty"`
	RemoveStalledAfter string      `json:"remove_stalled_after,omitzero"`
	CallbackURL        string      `json:"callback_url,omitempty"`
	EnableWebdavAuth   bool        `json:"enable_webdav_auth,omitempty"`
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

// GetDebridAPIKey returns the API key for the named debrid service, or the first one found.
func (c *Config) GetDebridAPIKey(name string) string {
	// Check env var first
	if v := Env("RD_API_KEY", ""); v != "" && (name == "" || name == "realdebrid") {
		return v
	}
	for _, d := range c.Debrids {
		if name == "" || strings.EqualFold(d.Name, name) {
			return d.APIKey
		}
	}
	return ""
}

func (c *Config) SecretKey() string {
	return cmp.Or(os.Getenv("DECYPHARR_SECRET_KEY"), "\"wqj(v%lj*!-+kf@4&i95rhh_!5_px5qnuwqbr%cjrvrozz_r*(\"")
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

func (c *Config) CheckSetup() error {
	return ValidateConfig(c)
}

func (c *Config) NeedsAuth() bool {
	return c.UseAuth && (c.Auth == nil || c.Auth.Username == "" || c.Auth.Password == "")
}

func (c *Config) updateDebrid(d Debrid) Debrid {
	workers := runtime.NumCPU() * 50
	perDebrid := workers / len(c.Debrids)

	var downloadKeys []string

	if len(d.DownloadAPIKeys) > 0 {
		downloadKeys = d.DownloadAPIKeys
	} else {
		// If no download API keys are specified, use the main API key
		downloadKeys = []string{d.APIKey}
	}
	d.DownloadAPIKeys = downloadKeys

	if !d.UseWebDav {
		return d
	}

	if d.TorrentsRefreshInterval == "" {
		d.TorrentsRefreshInterval = cmp.Or(c.WebDav.TorrentsRefreshInterval, "45s") // 45 seconds
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

	// Set repair defaults
	if c.Repair.Strategy == "" {
		c.Repair.Strategy = RepairStrategyPerTorrent
	}

	// Rclone defaults
	if c.Rclone.Enabled {
		c.Rclone.RcPort = cmp.Or(c.Rclone.RcPort, "5572")
		if c.Rclone.AsyncRead == nil {
			_asyncTrue := true
			c.Rclone.AsyncRead = &_asyncTrue
		}
		c.Rclone.VfsCacheMode = cmp.Or(c.Rclone.VfsCacheMode, "off")
		if c.Rclone.UID == 0 {
			c.Rclone.UID = uint32(os.Getuid())
		}
		if c.Rclone.GID == 0 {
			if runtime.GOOS == "windows" {
				// On Windows, we use the current user's SID as GID
				c.Rclone.GID = uint32(os.Getuid()) // Windows does not have GID, using UID instead
			} else {
				c.Rclone.GID = uint32(os.Getgid())
			}
		}
		if c.Rclone.Transfers == 0 {
			c.Rclone.Transfers = 4 // Default number of transfers
		}
		if c.Rclone.VfsCacheMode != "off" {
			c.Rclone.VfsCachePollInterval = cmp.Or(c.Rclone.VfsCachePollInterval, "1m") // Clean cache every minute
		}
		c.Rclone.DirCacheTime = cmp.Or(c.Rclone.DirCacheTime, "5m")
		c.Rclone.LogLevel = cmp.Or(c.Rclone.LogLevel, "INFO")
	}
	// Apply .env overrides for paths
	if v := Env("DOWNLOAD_FOLDER", ""); v != "" {
		c.QBitTorrent.DownloadFolder = v
	}
	if v := Env("RCLONE_MOUNT_PATH", ""); v != "" {
		c.Rclone.MountPath = v
	}

	// Apply .env overrides for local cache
	if EnvBool("LOCAL_CACHE_ENABLED", c.LocalCache.Enabled) {
		c.LocalCache.Enabled = true
	}
	if v := Env("RCLONE_INDICATOR", ""); v != "" {
		c.LocalCache.RcloneIndicator = v
	}
	if v := Env("LOCAL_CACHE_DIRS", ""); v != "" {
		var dirs []string
		for _, d := range strings.Split(v, ",") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
		c.LocalCache.CacheDirs = dirs
	}
	if v := Env("SCAN_INTERVAL", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.LocalCache.ScanInterval = n
		}
	}
	if v := Env("MIN_FREE_MB", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.LocalCache.MinFreeMB = n
		}
	}
	if v := Env("STALE_HOURS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.LocalCache.StaleHours = n
		}
	}
	if v := Env("MAX_PARALLEL", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.LocalCache.MaxParallel = n
		}
	}
	if EnvBool("USE_DEBRID_CDN", c.LocalCache.UseDebridCDN) {
		c.LocalCache.UseDebridCDN = true
	}
	if v := Env("ARIA2_CONNECTIONS", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			c.LocalCache.Aria2Connections = n
		}
	}

	// Allow RD_API_KEY env to set the first debrid's API key
	if v := Env("RD_API_KEY", ""); v != "" && len(c.Debrids) > 0 {
		for i := range c.Debrids {
			if c.Debrids[i].Name == "realdebrid" && c.Debrids[i].APIKey == "" {
				c.Debrids[i].APIKey = v
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
