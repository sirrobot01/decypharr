package alldebrid

import (
	"encoding/json"
	"fmt"
)

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type MagnetFile struct {
	Name     string       `json:"n"`
	Size     int64        `json:"s"`
	Link     string       `json:"l"`
	Elements []MagnetFile `json:"e"`
}
type magnetInfo struct {
	Id             int          `json:"id"`
	Filename       string       `json:"filename"`
	Size           int64        `json:"size"`
	Hash           string       `json:"hash"`
	Status         string       `json:"status"`
	StatusCode     int          `json:"statusCode"`
	UploadDate     int64        `json:"uploadDate"`
	Downloaded     int64        `json:"downloaded"`
	Uploaded       int64        `json:"uploaded"`
	DownloadSpeed  int64        `json:"downloadSpeed"`
	UploadSpeed    int64        `json:"uploadSpeed"`
	Seeders        int          `json:"seeders"`
	CompletionDate int64        `json:"completionDate"`
	Type           string       `json:"type"`
	Notified       bool         `json:"notified"`
	Version        int          `json:"version"`
	NbLinks        int          `json:"nbLinks"`
	Files          []MagnetFile `json:"files"`
}

type Magnets []magnetInfo

type TorrentInfoResponse struct {
	Status string `json:"status"`
	Data   struct {
		Magnets magnetInfo `json:"magnets"`
	} `json:"data"`
	Error *errorResponse `json:"error"`
}

type TorrentsListResponse struct {
	Status string `json:"status"`
	Data   struct {
		Magnets Magnets `json:"magnets"`
	} `json:"data"`
	Error *errorResponse `json:"error"`
}

type UploadMagnetResponse struct {
	Status string `json:"status"`
	Data   struct {
		Magnets []struct {
			Magnet           string `json:"magnet"`
			Hash             string `json:"hash"`
			Name             string `json:"name"`
			FilenameOriginal string `json:"filename_original"`
			Size             int64  `json:"size"`
			Ready            bool   `json:"ready"`
			ID               int    `json:"id"`
		} `json:"magnets"`
	}
	Error *errorResponse `json:"error"`
}

type DownloadLink struct {
	Status string `json:"status"`
	Data   struct {
		Link      string        `json:"link"`
		Host      string        `json:"host"`
		Filename  string        `json:"filename"`
		Streaming []interface{} `json:"streaming"`
		Paws      bool          `json:"paws"`
		Filesize  int           `json:"filesize"`
		Id        string        `json:"id"`
		Path      []struct {
			Name string `json:"n"`
			Size int    `json:"s"`
		} `json:"path"`
	} `json:"data"`
	Error *errorResponse `json:"error"`
}

// UnmarshalJSON implements custom unmarshaling for Magnets type
// It can handle both an array of magnetInfo objects or a map with string keys.
// If the input is an array, it will be unmarshaled directly into the Magnets slice.
// If the input is a map, it will extract the values and append them to the Magnets slice.
// If the input is neither, it will return an error.
func (m *Magnets) UnmarshalJSON(data []byte) error {
	// Try to unmarshal as array
	var arr []magnetInfo
	if err := json.Unmarshal(data, &arr); err == nil {
		*m = arr
		return nil
	}

	// Try to unmarshal as map
	var obj map[string]magnetInfo
	if err := json.Unmarshal(data, &obj); err == nil {
		for _, v := range obj {
			*m = append(*m, v)
		}
		return nil
	}
	return fmt.Errorf("magnets: unsupported JSON format")
}

type UserProfileResponse struct {
	Status string         `json:"status"`
	Error  *errorResponse `json:"error"`
	Data   struct {
		User struct {
			Username             string         `json:"username"`
			Email                string         `json:"email"`
			IsPremium            bool           `json:"isPremium"`
			IsSubscribed         bool           `json:"isSubscribed"`
			IsTrial              bool           `json:"isTrial"`
			PremiumUntil         int64          `json:"premiumUntil"`
			Lang                 string         `json:"lang"`
			FidelityPoints       int            `json:"fidelityPoints"`
			LimitedHostersQuotas map[string]int `json:"limitedHostersQuotas"`
			Notifications        []string       `json:"notifications"`
		} `json:"user"`
	} `json:"data"`
}
