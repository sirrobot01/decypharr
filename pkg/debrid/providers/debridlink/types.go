package debridlink

type APIResponse[T any] struct {
	Success bool `json:"success"`
	Value   *T   `json:"value"` // Use pointer to allow nil
}

// errorResponse is the error part of a response envelope.
type errorResponse struct {
	Error       string `json:"error"`
	Description string `json:"error_description"`
}

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

type DownloadLinksResponse APIResponse[[]struct {
	Created     int64  `json:"created"`
	Id          string `json:"id"`
	Name        string `json:"name"`
	Url         string `json:"url"`
	DownloadUrl string `json:"downloadUrl"`
	Expired     bool   `json:"expired"`
	Chunk       int    `json:"chunk"`
	Host        string `json:"host"`
	Size        int    `json:"size"`
}]
