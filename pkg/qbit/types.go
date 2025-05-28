package qbit

import (
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"sync"
)

type BuildInfo struct {
	Libtorrent string `json:"libtorrent"`
	Bitness    int    `json:"bitness"`
	Boost      string `json:"boost"`
	Openssl    string `json:"openssl"`
	Qt         string `json:"qt"`
	Zlib       string `json:"zlib"`
}

type AppPreferences struct {
	AddTrackers                        string   `json:"add_trackers"`
	AddTrackersEnabled                 bool     `json:"add_trackers_enabled"`
	AltDlLimit                         int      `json:"alt_dl_limit"`
	AltUpLimit                         int      `json:"alt_up_limit"`
	AlternativeWebuiEnabled            bool     `json:"alternative_webui_enabled"`
	AlternativeWebuiPath               string   `json:"alternative_webui_path"`
	AnnounceIp                         string   `json:"announce_ip"`
	AnnounceToAllTiers                 bool     `json:"announce_to_all_tiers"`
	AnnounceToAllTrackers              bool     `json:"announce_to_all_trackers"`
	AnonymousMode                      bool     `json:"anonymous_mode"`
	AsyncIoThreads                     int      `json:"async_io_threads"`
	AutoDeleteMode                     int      `json:"auto_delete_mode"`
	AutoTmmEnabled                     bool     `json:"auto_tmm_enabled"`
	AutorunEnabled                     bool     `json:"autorun_enabled"`
	AutorunProgram                     string   `json:"autorun_program"`
	BannedIPs                          string   `json:"banned_IPs"`
	BittorrentProtocol                 int      `json:"bittorrent_protocol"`
	BypassAuthSubnetWhitelist          string   `json:"bypass_auth_subnet_whitelist"`
	BypassAuthSubnetWhitelistEnabled   bool     `json:"bypass_auth_subnet_whitelist_enabled"`
	BypassLocalAuth                    bool     `json:"bypass_local_auth"`
	CategoryChangedTmmEnabled          bool     `json:"category_changed_tmm_enabled"`
	CheckingMemoryUse                  int      `json:"checking_memory_use"`
	CreateSubfolderEnabled             bool     `json:"create_subfolder_enabled"`
	CurrentInterfaceAddress            string   `json:"current_interface_address"`
	CurrentNetworkInterface            string   `json:"current_network_interface"`
	Dht                                bool     `json:"dht"`
	DiskCache                          int      `json:"disk_cache"`
	DiskCacheTtl                       int      `json:"disk_cache_ttl"`
	DlLimit                            int      `json:"dl_limit"`
	DontCountSlowTorrents              bool     `json:"dont_count_slow_torrents"`
	DyndnsDomain                       string   `json:"dyndns_domain"`
	DyndnsEnabled                      bool     `json:"dyndns_enabled"`
	DyndnsPassword                     string   `json:"dyndns_password"`
	DyndnsService                      int      `json:"dyndns_service"`
	DyndnsUsername                     string   `json:"dyndns_username"`
	EmbeddedTrackerPort                int      `json:"embedded_tracker_port"`
	EnableCoalesceReadWrite            bool     `json:"enable_coalesce_read_write"`
	EnableEmbeddedTracker              bool     `json:"enable_embedded_tracker"`
	EnableMultiConnectionsFromSameIp   bool     `json:"enable_multi_connections_from_same_ip"`
	EnableOsCache                      bool     `json:"enable_os_cache"`
	EnablePieceExtentAffinity          bool     `json:"enable_piece_extent_affinity"`
	EnableSuperSeeding                 bool     `json:"enable_super_seeding"`
	EnableUploadSuggestions            bool     `json:"enable_upload_suggestions"`
	Encryption                         int      `json:"encryption"`
	ExportDir                          string   `json:"export_dir"`
	ExportDirFin                       string   `json:"export_dir_fin"`
	FilePoolSize                       int      `json:"file_pool_size"`
	IncompleteFilesExt                 bool     `json:"incomplete_files_ext"`
	IpFilterEnabled                    bool     `json:"ip_filter_enabled"`
	IpFilterPath                       string   `json:"ip_filter_path"`
	IpFilterTrackers                   bool     `json:"ip_filter_trackers"`
	LimitLanPeers                      bool     `json:"limit_lan_peers"`
	LimitTcpOverhead                   bool     `json:"limit_tcp_overhead"`
	LimitUtpRate                       bool     `json:"limit_utp_rate"`
	ListenPort                         int      `json:"listen_port"`
	Locale                             string   `json:"locale"`
	Lsd                                bool     `json:"lsd"`
	MailNotificationAuthEnabled        bool     `json:"mail_notification_auth_enabled"`
	MailNotificationEmail              string   `json:"mail_notification_email"`
	MailNotificationEnabled            bool     `json:"mail_notification_enabled"`
	MailNotificationPassword           string   `json:"mail_notification_password"`
	MailNotificationSender             string   `json:"mail_notification_sender"`
	MailNotificationSmtp               string   `json:"mail_notification_smtp"`
	MailNotificationSslEnabled         bool     `json:"mail_notification_ssl_enabled"`
	MailNotificationUsername           string   `json:"mail_notification_username"`
	MaxActiveDownloads                 int      `json:"max_active_downloads"`
	MaxActiveTorrents                  int      `json:"max_active_torrents"`
	MaxActiveUploads                   int      `json:"max_active_uploads"`
	MaxConnec                          int      `json:"max_connec"`
	MaxConnecPerTorrent                int      `json:"max_connec_per_torrent"`
	MaxRatio                           int      `json:"max_ratio"`
	MaxRatioAct                        int      `json:"max_ratio_act"`
	MaxRatioEnabled                    bool     `json:"max_ratio_enabled"`
	MaxSeedingTime                     int      `json:"max_seeding_time"`
	MaxSeedingTimeEnabled              bool     `json:"max_seeding_time_enabled"`
	MaxUploads                         int      `json:"max_uploads"`
	MaxUploadsPerTorrent               int      `json:"max_uploads_per_torrent"`
	OutgoingPortsMax                   int      `json:"outgoing_ports_max"`
	OutgoingPortsMin                   int      `json:"outgoing_ports_min"`
	Pex                                bool     `json:"pex"`
	PreallocateAll                     bool     `json:"preallocate_all"`
	ProxyAuthEnabled                   bool     `json:"proxy_auth_enabled"`
	ProxyIp                            string   `json:"proxy_ip"`
	ProxyPassword                      string   `json:"proxy_password"`
	ProxyPeerConnections               bool     `json:"proxy_peer_connections"`
	ProxyPort                          int      `json:"proxy_port"`
	ProxyTorrentsOnly                  bool     `json:"proxy_torrents_only"`
	ProxyType                          int      `json:"proxy_type"`
	ProxyUsername                      string   `json:"proxy_username"`
	QueueingEnabled                    bool     `json:"queueing_enabled"`
	RandomPort                         bool     `json:"random_port"`
	RecheckCompletedTorrents           bool     `json:"recheck_completed_torrents"`
	ResolvePeerCountries               bool     `json:"resolve_peer_countries"`
	RssAutoDownloadingEnabled          bool     `json:"rss_auto_downloading_enabled"`
	RssMaxArticlesPerFeed              int      `json:"rss_max_articles_per_feed"`
	RssProcessingEnabled               bool     `json:"rss_processing_enabled"`
	RssRefreshInterval                 int      `json:"rss_refresh_interval"`
	SavePath                           string   `json:"save_path"`
	SavePathChangedTmmEnabled          bool     `json:"save_path_changed_tmm_enabled"`
	SaveResumeDataInterval             int      `json:"save_resume_data_interval"`
	ScanDirs                           ScanDirs `json:"scan_dirs"`
	ScheduleFromHour                   int      `json:"schedule_from_hour"`
	ScheduleFromMin                    int      `json:"schedule_from_min"`
	ScheduleToHour                     int      `json:"schedule_to_hour"`
	ScheduleToMin                      int      `json:"schedule_to_min"`
	SchedulerDays                      int      `json:"scheduler_days"`
	SchedulerEnabled                   bool     `json:"scheduler_enabled"`
	SendBufferLowWatermark             int      `json:"send_buffer_low_watermark"`
	SendBufferWatermark                int      `json:"send_buffer_watermark"`
	SendBufferWatermarkFactor          int      `json:"send_buffer_watermark_factor"`
	SlowTorrentDlRateThreshold         int      `json:"slow_torrent_dl_rate_threshold"`
	SlowTorrentInactiveTimer           int      `json:"slow_torrent_inactive_timer"`
	SlowTorrentUlRateThreshold         int      `json:"slow_torrent_ul_rate_threshold"`
	SocketBacklogSize                  int      `json:"socket_backlog_size"`
	StartPausedEnabled                 bool     `json:"start_paused_enabled"`
	StopTrackerTimeout                 int      `json:"stop_tracker_timeout"`
	TempPath                           string   `json:"temp_path"`
	TempPathEnabled                    bool     `json:"temp_path_enabled"`
	TorrentChangedTmmEnabled           bool     `json:"torrent_changed_tmm_enabled"`
	UpLimit                            int      `json:"up_limit"`
	UploadChokingAlgorithm             int      `json:"upload_choking_algorithm"`
	UploadSlotsBehavior                int      `json:"upload_slots_behavior"`
	Upnp                               bool     `json:"upnp"`
	UpnpLeaseDuration                  int      `json:"upnp_lease_duration"`
	UseHttps                           bool     `json:"use_https"`
	UtpTcpMixedMode                    int      `json:"utp_tcp_mixed_mode"`
	WebUiAddress                       string   `json:"web_ui_address"`
	WebUiBanDuration                   int      `json:"web_ui_ban_duration"`
	WebUiClickjackingProtectionEnabled bool     `json:"web_ui_clickjacking_protection_enabled"`
	WebUiCsrfProtectionEnabled         bool     `json:"web_ui_csrf_protection_enabled"`
	WebUiDomainList                    string   `json:"web_ui_domain_list"`
	WebUiHostHeaderValidationEnabled   bool     `json:"web_ui_host_header_validation_enabled"`
	WebUiHttpsCertPath                 string   `json:"web_ui_https_cert_path"`
	WebUiHttpsKeyPath                  string   `json:"web_ui_https_key_path"`
	WebUiMaxAuthFailCount              int      `json:"web_ui_max_auth_fail_count"`
	WebUiPort                          int      `json:"web_ui_port"`
	WebUiSecureCookieEnabled           bool     `json:"web_ui_secure_cookie_enabled"`
	WebUiSessionTimeout                int      `json:"web_ui_session_timeout"`
	WebUiUpnp                          bool     `json:"web_ui_upnp"`
	WebUiUsername                      string   `json:"web_ui_username"`
	WebUiPassword                      string   `json:"web_ui_password"`
	SSLKey                             string   `json:"ssl_key"`
	SSLCert                            string   `json:"ssl_cert"`
	RSSDownloadRepack                  string   `json:"rss_download_repack_proper_episodes"`
	RSSSmartEpisodeFilters             string   `json:"rss_smart_episode_filters"`
	WebUiUseCustomHttpHeaders          bool     `json:"web_ui_use_custom_http_headers"`
	WebUiUseCustomHttpHeadersEnabled   bool     `json:"web_ui_use_custom_http_headers_enabled"`
}

type ScanDirs struct{}

type TorrentCategory struct {
	Name     string `json:"name"`
	SavePath string `json:"savePath"`
}

type Torrent struct {
	ID            string         `json:"id"`
	DebridTorrent *types.Torrent `json:"-"`
	Debrid        string         `json:"debrid"`
	TorrentPath   string         `json:"-"`

	AddedOn           int64   `json:"added_on,omitempty"`
	AmountLeft        int64   `json:"amount_left"`
	AutoTmm           bool    `json:"auto_tmm"`
	Availability      float64 `json:"availability,omitempty"`
	Category          string  `json:"category,omitempty"`
	Completed         int64   `json:"completed"`
	CompletionOn      int     `json:"completion_on,omitempty"`
	ContentPath       string  `json:"content_path"`
	DlLimit           int     `json:"dl_limit"`
	Dlspeed           int64   `json:"dlspeed"`
	Downloaded        int64   `json:"downloaded"`
	DownloadedSession int64   `json:"downloaded_session"`
	Eta               int     `json:"eta"`
	FlPiecePrio       bool    `json:"f_l_piece_prio,omitempty"`
	ForceStart        bool    `json:"force_start,omitempty"`
	Hash              string  `json:"hash"`
	LastActivity      int64   `json:"last_activity,omitempty"`
	MagnetUri         string  `json:"magnet_uri,omitempty"`
	MaxRatio          int     `json:"max_ratio,omitempty"`
	MaxSeedingTime    int     `json:"max_seeding_time,omitempty"`
	Name              string  `json:"name,omitempty"`
	NumComplete       int     `json:"num_complete,omitempty"`
	NumIncomplete     int     `json:"num_incomplete,omitempty"`
	NumLeechs         int     `json:"num_leechs,omitempty"`
	NumSeeds          int     `json:"num_seeds,omitempty"`
	Priority          int     `json:"priority,omitempty"`
	Progress          float64 `json:"progress"`
	Ratio             int     `json:"ratio,omitempty"`
	RatioLimit        int     `json:"ratio_limit,omitempty"`
	SavePath          string  `json:"save_path"`
	SeedingTimeLimit  int     `json:"seeding_time_limit,omitempty"`
	SeenComplete      int64   `json:"seen_complete,omitempty"`
	SeqDl             bool    `json:"seq_dl"`
	Size              int64   `json:"size,omitempty"`
	State             string  `json:"state,omitempty"`
	SuperSeeding      bool    `json:"super_seeding"`
	Tags              string  `json:"tags,omitempty"`
	TimeActive        int     `json:"time_active,omitempty"`
	TotalSize         int64   `json:"total_size,omitempty"`
	Tracker           string  `json:"tracker,omitempty"`
	UpLimit           int64   `json:"up_limit,omitempty"`
	Uploaded          int64   `json:"uploaded,omitempty"`
	UploadedSession   int64   `json:"uploaded_session,omitempty"`
	Upspeed           int64   `json:"upspeed,omitempty"`
	Source            string  `json:"source,omitempty"`

	Mu sync.Mutex `json:"-"`
}

func (t *Torrent) IsReady() bool {
	return (t.AmountLeft <= 0 || t.Progress == 1) && t.TorrentPath != ""
}

func (t *Torrent) discordContext() string {
	format := `
		**Name:** %s
		**Arr:** %s
		**Hash:** %s
		**MagnetURI:** %s
		**Debrid:** %s
	`
	return fmt.Sprintf(format, t.Name, t.Category, t.Hash, t.MagnetUri, t.Debrid)
}

type TorrentProperties struct {
	AdditionDate           int64  `json:"addition_date,omitempty"`
	Comment                string `json:"comment,omitempty"`
	CompletionDate         int64  `json:"completion_date,omitempty"`
	CreatedBy              string `json:"created_by,omitempty"`
	CreationDate           int64  `json:"creation_date,omitempty"`
	DlLimit                int    `json:"dl_limit,omitempty"`
	DlSpeed                int64  `json:"dl_speed,omitempty"`
	DlSpeedAvg             int    `json:"dl_speed_avg,omitempty"`
	Eta                    int    `json:"eta,omitempty"`
	LastSeen               int64  `json:"last_seen,omitempty"`
	NbConnections          int    `json:"nb_connections,omitempty"`
	NbConnectionsLimit     int    `json:"nb_connections_limit,omitempty"`
	Peers                  int    `json:"peers,omitempty"`
	PeersTotal             int    `json:"peers_total,omitempty"`
	PieceSize              int64  `json:"piece_size,omitempty"`
	PiecesHave             int64  `json:"pieces_have,omitempty"`
	PiecesNum              int64  `json:"pieces_num,omitempty"`
	Reannounce             int    `json:"reannounce,omitempty"`
	SavePath               string `json:"save_path,omitempty"`
	SeedingTime            int    `json:"seeding_time,omitempty"`
	Seeds                  int    `json:"seeds,omitempty"`
	SeedsTotal             int    `json:"seeds_total,omitempty"`
	ShareRatio             int    `json:"share_ratio,omitempty"`
	TimeElapsed            int64  `json:"time_elapsed,omitempty"`
	TotalDownloaded        int64  `json:"total_downloaded,omitempty"`
	TotalDownloadedSession int64  `json:"total_downloaded_session,omitempty"`
	TotalSize              int64  `json:"total_size,omitempty"`
	TotalUploaded          int64  `json:"total_uploaded,omitempty"`
	TotalUploadedSession   int64  `json:"total_uploaded_session,omitempty"`
	TotalWasted            int64  `json:"total_wasted,omitempty"`
	UpLimit                int    `json:"up_limit,omitempty"`
	UpSpeed                int64  `json:"up_speed,omitempty"`
	UpSpeedAvg             int    `json:"up_speed_avg,omitempty"`
}

type TorrentFile struct {
	Index        int     `json:"index,omitempty"`
	Name         string  `json:"name,omitempty"`
	Size         int64   `json:"size,omitempty"`
	Progress     int     `json:"progress,omitempty"`
	Priority     int     `json:"priority,omitempty"`
	IsSeed       bool    `json:"is_seed,omitempty"`
	PieceRange   []int   `json:"piece_range,omitempty"`
	Availability float64 `json:"availability,omitempty"`
}

func NewAppPreferences() *AppPreferences {
	preferences := &AppPreferences{
		AddTrackers:                        "",
		AddTrackersEnabled:                 false,
		AltDlLimit:                         10240,
		AltUpLimit:                         10240,
		AlternativeWebuiEnabled:            false,
		AlternativeWebuiPath:               "",
		AnnounceIp:                         "",
		AnnounceToAllTiers:                 true,
		AnnounceToAllTrackers:              false,
		AnonymousMode:                      false,
		AsyncIoThreads:                     4,
		AutoDeleteMode:                     0,
		AutoTmmEnabled:                     false,
		AutorunEnabled:                     false,
		AutorunProgram:                     "",
		BannedIPs:                          "",
		BittorrentProtocol:                 0,
		BypassAuthSubnetWhitelist:          "",
		BypassAuthSubnetWhitelistEnabled:   false,
		BypassLocalAuth:                    false,
		CategoryChangedTmmEnabled:          false,
		CheckingMemoryUse:                  32,
		CreateSubfolderEnabled:             true,
		CurrentInterfaceAddress:            "",
		CurrentNetworkInterface:            "",
		Dht:                                true,
		DiskCache:                          -1,
		DiskCacheTtl:                       60,
		DlLimit:                            0,
		DontCountSlowTorrents:              false,
		DyndnsDomain:                       "changeme.dyndns.org",
		DyndnsEnabled:                      false,
		DyndnsPassword:                     "",
		DyndnsService:                      0,
		DyndnsUsername:                     "",
		EmbeddedTrackerPort:                9000,
		EnableCoalesceReadWrite:            true,
		EnableEmbeddedTracker:              false,
		EnableMultiConnectionsFromSameIp:   false,
		EnableOsCache:                      true,
		EnablePieceExtentAffinity:          false,
		EnableSuperSeeding:                 false,
		EnableUploadSuggestions:            false,
		Encryption:                         0,
		ExportDir:                          "",
		ExportDirFin:                       "",
		FilePoolSize:                       40,
		IncompleteFilesExt:                 false,
		IpFilterEnabled:                    false,
		IpFilterPath:                       "",
		IpFilterTrackers:                   false,
		LimitLanPeers:                      true,
		LimitTcpOverhead:                   false,
		LimitUtpRate:                       true,
		ListenPort:                         31193,
		Locale:                             "en",
		Lsd:                                true,
		MailNotificationAuthEnabled:        false,
		MailNotificationEmail:              "",
		MailNotificationEnabled:            false,
		MailNotificationPassword:           "",
		MailNotificationSender:             "qBittorrentNotification@example.com",
		MailNotificationSmtp:               "smtp.changeme.com",
		MailNotificationSslEnabled:         false,
		MailNotificationUsername:           "",
		MaxActiveDownloads:                 3,
		MaxActiveTorrents:                  5,
		MaxActiveUploads:                   3,
		MaxConnec:                          500,
		MaxConnecPerTorrent:                100,
		MaxRatio:                           -1,
		MaxRatioAct:                        0,
		MaxRatioEnabled:                    false,
		MaxSeedingTime:                     -1,
		MaxSeedingTimeEnabled:              false,
		MaxUploads:                         -1,
		MaxUploadsPerTorrent:               -1,
		OutgoingPortsMax:                   0,
		OutgoingPortsMin:                   0,
		Pex:                                true,
		PreallocateAll:                     false,
		ProxyAuthEnabled:                   false,
		ProxyIp:                            "0.0.0.0",
		ProxyPassword:                      "",
		ProxyPeerConnections:               false,
		ProxyPort:                          8080,
		ProxyTorrentsOnly:                  false,
		ProxyType:                          0,
		ProxyUsername:                      "",
		QueueingEnabled:                    false,
		RandomPort:                         false,
		RecheckCompletedTorrents:           false,
		ResolvePeerCountries:               true,
		RssAutoDownloadingEnabled:          false,
		RssMaxArticlesPerFeed:              50,
		RssProcessingEnabled:               false,
		RssRefreshInterval:                 30,
		SavePathChangedTmmEnabled:          false,
		SaveResumeDataInterval:             60,
		ScanDirs:                           ScanDirs{},
		ScheduleFromHour:                   8,
		ScheduleFromMin:                    0,
		ScheduleToHour:                     20,
		ScheduleToMin:                      0,
		SchedulerDays:                      0,
		SchedulerEnabled:                   false,
		SendBufferLowWatermark:             10,
		SendBufferWatermark:                500,
		SendBufferWatermarkFactor:          50,
		SlowTorrentDlRateThreshold:         2,
		SlowTorrentInactiveTimer:           60,
		SlowTorrentUlRateThreshold:         2,
		SocketBacklogSize:                  30,
		StartPausedEnabled:                 false,
		StopTrackerTimeout:                 1,
		TempPathEnabled:                    false,
		TorrentChangedTmmEnabled:           true,
		UpLimit:                            0,
		UploadChokingAlgorithm:             1,
		UploadSlotsBehavior:                0,
		Upnp:                               true,
		UpnpLeaseDuration:                  0,
		UseHttps:                           false,
		UtpTcpMixedMode:                    0,
		WebUiAddress:                       "*",
		WebUiBanDuration:                   3600,
		WebUiClickjackingProtectionEnabled: true,
		WebUiCsrfProtectionEnabled:         true,
		WebUiDomainList:                    "*",
		WebUiHostHeaderValidationEnabled:   true,
		WebUiHttpsCertPath:                 "",
		WebUiHttpsKeyPath:                  "",
		WebUiMaxAuthFailCount:              5,
		WebUiPort:                          8080,
		WebUiSecureCookieEnabled:           true,
		WebUiSessionTimeout:                3600,
		WebUiUpnp:                          false,

		// Fields in the struct but not in the JSON (set to zero values):
		WebUiPassword:                    "",
		SSLKey:                           "",
		SSLCert:                          "",
		RSSDownloadRepack:                "",
		RSSSmartEpisodeFilters:           "",
		WebUiUseCustomHttpHeaders:        false,
		WebUiUseCustomHttpHeadersEnabled: false,
	}
	return preferences
}
