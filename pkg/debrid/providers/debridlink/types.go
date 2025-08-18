package debridlink

type APIResponse[T any] struct {
	Success bool `json:"success"`
	Value   *T   `json:"value"` // Use pointer to allow nil
}

type AvailableResponse APIResponse[map[string]map[string]struct {
	Name       string `json:"name"`
	HashString string `json:"hashString"`
	Files      []struct {
		Name string `json:"name"`
		Size int    `json:"size"`
	} `json:"files"`
}]

type _torrentInfo struct {
	ID             string  `json:"id"`
	Name           string  `json:"name"`
	HashString     string  `json:"hashString"`
	UploadRatio    float64 `json:"uploadRatio"`
	ServerID       string  `json:"serverId"`
	Wait           bool    `json:"wait"`
	PeersConnected int     `json:"peersConnected"`
	Status         int     `json:"status"`
	TotalSize      int64   `json:"totalSize"`
	Files          []struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		DownloadURL     string `json:"downloadUrl"`
		Size            int64  `json:"size"`
		DownloadPercent int    `json:"downloadPercent"`
	} `json:"files"`
	Trackers []struct {
		Announce string `json:"announce"`
	} `json:"trackers"`
	Created         int64   `json:"created"`
	DownloadPercent float64 `json:"downloadPercent"`
	DownloadSpeed   int64   `json:"downloadSpeed"`
	UploadSpeed     int64   `json:"uploadSpeed"`
}

type torrentInfo APIResponse[[]_torrentInfo]

type SubmitTorrentInfo APIResponse[_torrentInfo]

type UserInfo APIResponse[struct {
	Username     string `json:"username"`
	Email        string `json:"email"`
	AccountType  int    `json:"accountType"`
	PremiumLeft  int64  `json:"premiumLeft"`
	Points       int    `json:"pts"`
	Trafficshare int    `json:"trafficshare"`
}]
