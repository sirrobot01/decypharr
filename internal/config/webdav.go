package config

type WebdavDirectories struct {
	Filters map[string]string `json:"filters,omitempty"`
	//SaveStrms bool              `json:"save_streams,omitempty"`
}

type WebDav struct {
	TorrentsRefreshInterval      string `json:"torrents_refresh_interval,omitempty"`
	DownloadLinksRefreshInterval string `json:"download_links_refresh_interval,omitempty"`
	Workers                      int    `json:"workers,omitempty"`
	AutoExpireLinksAfter         string `json:"auto_expire_links_after,omitempty"`
	ServeFromRclone              bool   `json:"serve_from_rclone,omitempty"`

	// Folder
	FolderNaming string `json:"folder_naming,omitempty"`

	// Rclone
	RcUrl         string `json:"rc_url,omitempty"`
	RcUser        string `json:"rc_user,omitempty"`
	RcPass        string `json:"rc_pass,omitempty"`
	RcRefreshDirs string `json:"rc_refresh_dirs,omitempty"` // comma separated list of directories to refresh

	// Directories
	Directories map[string]WebdavDirectories `json:"directories,omitempty"`
}
