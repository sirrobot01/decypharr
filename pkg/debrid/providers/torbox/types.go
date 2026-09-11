package torbox

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

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
	Id              int       `json:"id"`
	AuthId          string    `json:"auth_id"`
	Server          int       `json:"server"`
	Hash            string    `json:"hash"`
	Name            string    `json:"name"`
	Magnet          any       `json:"magnet"`
	Size            int64     `json:"size"`
	Active          bool      `json:"active"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	DownloadState   string    `json:"download_state"`
	Seeds           int       `json:"seeds"`
	Peers           int       `json:"peers"`
	Ratio           float64   `json:"ratio"`
	Progress        float64   `json:"progress"`
	DownloadSpeed   int64     `json:"download_speed"`
	UploadSpeed     int       `json:"upload_speed"`
	Eta             int       `json:"eta"`
	TorrentFile     bool      `json:"torrent_file"`
	ExpiresAt       any       `json:"expires_at"`
	DownloadPresent bool      `json:"download_present"`
	Files           []struct {
		Id           int    `json:"id"`
		Md5          any    `json:"md5"`
		Hash         string `json:"hash"`
		Name         string `json:"name"`
		Size         int64  `json:"size"`
		Zipped       bool   `json:"zipped"`
		S3Path       string `json:"s3_path"`
		Infected     bool   `json:"infected"`
		Mimetype     string `json:"mimetype"`
		ShortName    string `json:"short_name"`
		AbsolutePath string `json:"absolute_path"`
	} `json:"files"`
	DownloadPath     string  `json:"download_path"`
	InactiveCheck    int     `json:"inactive_check"`
	Availability     float64 `json:"availability"`
	DownloadFinished bool    `json:"download_finished"`
	Tracker          any     `json:"tracker"`
	TotalUploaded    int     `json:"total_uploaded"`
	TotalDownloaded  int     `json:"total_downloaded"`
	Cached           bool    `json:"cached"`
	Owner            string  `json:"owner"`
	SeedTorrent      bool    `json:"seed_torrent"`
	AllowZipped      bool    `json:"allow_zipped"`
	LongTermSeeding  bool    `json:"long_term_seeding"`
	TrackerMessage   any     `json:"tracker_message"`
}

type InfoResponse struct {
	Success bool        `json:"success"`
	Error   any         `json:"error"`
	Detail  string      `json:"detail"`
	Data    *torboxInfo `json:"data"`
}

// UnmarshalJSON accepts both documented object responses and the one-element
// array response TorBox occasionally returns for /torrents/mylist?id=... .
func (r *InfoResponse) UnmarshalJSON(data []byte) error {
	var envelope struct {
		Success bool            `json:"success"`
		Error   any             `json:"error"`
		Detail  string          `json:"detail"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}

	r.Success = envelope.Success
	r.Error = envelope.Error
	r.Detail = envelope.Detail
	r.Data = nil

	raw := bytes.TrimSpace(envelope.Data)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}

	if raw[0] == '[' {
		var items []torboxInfo
		if err := json.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("decode TorBox torrent array: %w", err)
		}
		if len(items) > 0 {
			r.Data = &items[0]
		}
		return nil
	}

	var item torboxInfo
	if err := json.Unmarshal(raw, &item); err != nil {
		return fmt.Errorf("decode TorBox torrent object: %w", err)
	}
	r.Data = &item
	return nil
}

type DownloadLinksResponse APIResponse[string]

type TorrentsListResponse APIResponse[[]torboxInfo]

type profileResponse struct {
	Id                        int64  `json:"id"`
	AuthId                    string `json:"auth_id"`
	CreatedAt                 string `json:"created_at"`
	UpdatedAt                 string `json:"updated_at"`
	Plan                      int64  `json:"plan"`
	TotalDownloaded           int64  `json:"total_downloaded"`
	Customer                  string `json:"customer"`
	IsSubscribed              bool   `json:"is_subscribed"`
	PremiumExpiresAt          string `json:"premium_expires_at"`
	CooldownUntil             string `json:"cooldown_until"`
	Email                     string `json:"email"`
	UserReferral              string `json:"user_referral"`
	BaseEmail                 string `json:"base_email"`
	TotalBytesDownloaded      int64  `json:"total_bytes_downloaded"`
	TotalBytesUploaded        int64  `json:"total_bytes_uploaded"`
	TorrentsDownloaded        int64  `json:"torrents_downloaded"`
	WebDownloadsDownloaded    int64  `json:"web_downloads_downloaded"`
	UsenetDownloadsDownloaded int64  `json:"usenet_downloads_downloaded"`
	AdditionalConcurrentSlots int64  `json:"additional_concurrent_slots"`
	LongTermSeeding           bool   `json:"long_term_seeding"`
	LongTermStorage           bool   `json:"long_term_storage"`
	IsVendor                  bool   `json:"is_vendor"`
	VendorId                  any    `json:"vendor_id"`
	PurchasesReferred         int64  `json:"purchases_referred"`
}

type ProfileResponse APIResponse[profileResponse]
