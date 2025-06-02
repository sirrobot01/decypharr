package torbox

import "time"

type APIResponse[T any] struct {
	Success bool   `json:"success"`
	Error   any    `json:"error"`
	Detail  string `json:"detail"`
	Data    *T     `json:"data"` // Use pointer to allow nil
}

type AvailableResponse APIResponse[map[string]struct {
	Name string `json:"name"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
}]

type AddMagnetResponse APIResponse[struct {
	Id   int    `json:"torrent_id"`
	Hash string `json:"hash"`
}]

type torboxInfo struct {
	Id              int         `json:"id"`
	AuthId          string      `json:"auth_id"`
	Server          int         `json:"server"`
	Hash            string      `json:"hash"`
	Name            string      `json:"name"`
	Magnet          interface{} `json:"magnet"`
	Size            int64       `json:"size"`
	Active          bool        `json:"active"`
	CreatedAt       time.Time   `json:"created_at"`
	UpdatedAt       time.Time   `json:"updated_at"`
	DownloadState   string      `json:"download_state"`
	Seeds           int         `json:"seeds"`
	Peers           int         `json:"peers"`
	Ratio           float64     `json:"ratio"`
	Progress        float64     `json:"progress"`
	DownloadSpeed   int64       `json:"download_speed"`
	UploadSpeed     int         `json:"upload_speed"`
	Eta             int         `json:"eta"`
	TorrentFile     bool        `json:"torrent_file"`
	ExpiresAt       interface{} `json:"expires_at"`
	DownloadPresent bool        `json:"download_present"`
	Files           []struct {
		Id           int         `json:"id"`
		Md5          interface{} `json:"md5"`
		Hash         string      `json:"hash"`
		Name         string      `json:"name"`
		Size         int64       `json:"size"`
		Zipped       bool        `json:"zipped"`
		S3Path       string      `json:"s3_path"`
		Infected     bool        `json:"infected"`
		Mimetype     string      `json:"mimetype"`
		ShortName    string      `json:"short_name"`
		AbsolutePath string      `json:"absolute_path"`
	} `json:"files"`
	DownloadPath     string      `json:"download_path"`
	InactiveCheck    int         `json:"inactive_check"`
	Availability     int         `json:"availability"`
	DownloadFinished bool        `json:"download_finished"`
	Tracker          interface{} `json:"tracker"`
	TotalUploaded    int         `json:"total_uploaded"`
	TotalDownloaded  int         `json:"total_downloaded"`
	Cached           bool        `json:"cached"`
	Owner            string      `json:"owner"`
	SeedTorrent      bool        `json:"seed_torrent"`
	AllowZipped      bool        `json:"allow_zipped"`
	LongTermSeeding  bool        `json:"long_term_seeding"`
	TrackerMessage   interface{} `json:"tracker_message"`
}

type InfoResponse APIResponse[torboxInfo]

type DownloadLinksResponse APIResponse[string]
