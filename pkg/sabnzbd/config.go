package sabnzbd

// ConfigResponse represents configuration response
type ConfigResponse struct {
	Config *Config `json:"config"`
}

type ConfigNewzbin struct {
	Username     string `json:"username"`
	BookmarkRate int    `json:"bookmark_rate"`
	Url          string `json:"url"`
	Bookmarks    int    `json:"bookmarks"`
	Password     string `json:"password"`
	Unbookmark   int    `json:"unbookmark"`
}

// Category represents a SABnzbd category
type Category struct {
	Name     string `json:"name"`
	Order    int    `json:"order"`
	Pp       string `json:"pp"`
	Script   string `json:"script"`
	Dir      string `json:"dir"`
	NewzBin  string `json:"newzbin"`
	Priority string `json:"priority"`
}

// Server represents a usenet server
type Server struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	Connections int    `json:"connections"`
	Retention   int    `json:"retention"`
	Priority    int    `json:"priority"`
	SSL         bool   `json:"ssl"`
	Optional    bool   `json:"optional"`
}

type Config struct {
	Misc       MiscConfig `json:"misc"`
	Categories []Category `json:"categories"`
	Servers    []Server   `json:"servers"`
}

type MiscConfig struct {
	// Directory Configuration
	CompleteDir  string `json:"complete_dir"`
	DownloadDir  string `json:"download_dir"`
	AdminDir     string `json:"admin_dir"`
	NzbBackupDir string `json:"nzb_backup_dir"`
	ScriptDir    string `json:"script_dir"`
	EmailDir     string `json:"email_dir"`
	WebDir       string `json:"web_dir"`

	// Processing Options
	ParOption             string `json:"par_option"`
	ParOptionConvert      string `json:"par_option_convert"`
	ParOptionDuplicate    string `json:"par_option_duplicate"`
	DirectUnpack          string `json:"direct_unpack"`
	FlatUnpack            string `json:"flat_unpack"`
	EnableRecursiveUnpack string `json:"enable_recursive_unpack"`
	OverwriteFiles        string `json:"overwrite_files"`
	IgnoreWrongUnrar      string `json:"ignore_wrong_unrar"`
	IgnoreUnrarDates      string `json:"ignore_unrar_dates"`
	PreCheck              string `json:"pre_check"`

	// File Handling
	Permissions               string   `json:"permissions"`
	FolderRename              string   `json:"folder_rename"`
	FileRename                string   `json:"file_rename"`
	ReplaceIllegal            string   `json:"replace_illegal"`
	ReplaceDots               string   `json:"replace_dots"`
	ReplaceSpaces             string   `json:"replace_spaces"`
	SanitizeSafe              string   `json:"sanitize_safe"`
	IgnoreSamples             string   `json:"ignore_samples"`
	UnwantedExtensions        []string `json:"unwanted_extensions"`
	ActionOnUnwanted          string   `json:"action_on_unwanted"`
	ActionOnDuplicate         string   `json:"action_on_duplicate"`
	BackupForDuplicates       string   `json:"backup_for_duplicates"`
	CleanupList               []string `json:"cleanup_list"`
	DeobfuscateFinalFilenames string   `json:"deobfuscate_final_filenames"`

	// Scripts and Processing
	PreScript             string `json:"pre_script"`
	PostScript            string `json:"post_script"`
	EmptyPostproc         string `json:"empty_postproc"`
	PauseOnPostProcessing string `json:"pause_on_post_processing"`

	// System Resources
	Nice       string `json:"nice"`
	NiceUnpack string `json:"nice_unpack"`
	Ionice     string `json:"ionice"`
	Fsync      string `json:"fsync"`

	// Bandwidth and Performance
	BandwidthMax     string `json:"bandwidth_max"`
	BandwidthPerc    string `json:"bandwidth_perc"`
	RefreshRate      string `json:"refresh_rate"`
	DirscanSpeed     string `json:"dirscan_speed"`
	FolderMaxLength  string `json:"folder_max_length"`
	PropagationDelay string `json:"propagation_delay"`

	// Storage Management
	DownloadFree string `json:"download_free"`
	CompleteFree string `json:"complete_free"`

	// Queue Management
	QueueComplete     string `json:"queue_complete"`
	QueueCompletePers string `json:"queue_complete_pers"`
	AutoSort          string `json:"auto_sort"`
	NewNzbOnFailure   string `json:"new_nzb_on_failure"`
	PauseOnPwrar      string `json:"pause_on_pwrar"`
	WarnedOldQueue    string `json:"warned_old_queue"`

	// Web Interface
	WebHost     string `json:"web_host"`
	WebPort     string `json:"web_port"`
	WebUsername string `json:"web_username"`
	WebPassword string `json:"web_password"`
	WebColor    string `json:"web_color"`
	WebColor2   string `json:"web_color2"`
	AutoBrowser string `json:"auto_browser"`
	Autobrowser string `json:"autobrowser"` // Duplicate field - may need to resolve

	// HTTPS Configuration
	EnableHTTPS             string `json:"enable_https"`
	EnableHTTPSVerification string `json:"enable_https_verification"`
	HTTPSPort               string `json:"https_port"`
	HTTPSCert               string `json:"https_cert"`
	HTTPSKey                string `json:"https_key"`
	HTTPSChain              string `json:"https_chain"`

	// Security and API
	APIKey        string   `json:"api_key"`
	NzbKey        string   `json:"nzb_key"`
	HostWhitelist string   `json:"host_whitelist"`
	LocalRanges   []string `json:"local_ranges"`
	InetExposure  string   `json:"inet_exposure"`
	APILogging    string   `json:"api_logging"`
	APIWarnings   string   `json:"api_warnings"`

	// Logging
	LogLevel   string `json:"log_level"`
	LogSize    string `json:"log_size"`
	MaxLogSize string `json:"max_log_size"`
	LogBackups string `json:"log_backups"`
	LogNew     string `json:"log_new"`

	// Notifications
	MatrixUsername string `json:"matrix_username"`
	MatrixPassword string `json:"matrix_password"`
	MatrixServer   string `json:"matrix_server"`
	MatrixRoom     string `json:"matrix_room"`

	// Miscellaneous
	ConfigLock      string `json:"config_lock"`
	Language        string `json:"language"`
	CheckNewRel     string `json:"check_new_rel"`
	RSSFilenames    string `json:"rss_filenames"`
	IPv6Hosting     string `json:"ipv6_hosting"`
	EnableBonjour   string `json:"enable_bonjour"`
	Cherryhost      string `json:"cherryhost"`
	WinMenu         string `json:"win_menu"`
	AMPM            string `json:"ampm"`
	NotifiedNewSkin string `json:"notified_new_skin"`
	HelpURI         string `json:"helpuri"`
	SSDURI          string `json:"ssduri"`
}
