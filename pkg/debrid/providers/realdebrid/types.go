package realdebrid

import (
	"encoding/json"
	"fmt"
	"time"
)

type AvailabilityResponse map[string]Hoster

func (r *AvailabilityResponse) UnmarshalJSON(data []byte) error {
	// First, try to unmarshal as an object
	var objectData map[string]Hoster
	err := json.Unmarshal(data, &objectData)
	if err == nil {
		*r = objectData
		return nil
	}

	// If that fails, try to unmarshal as an array
	var arrayData []map[string]Hoster
	err = json.Unmarshal(data, &arrayData)
	if err != nil {
		return fmt.Errorf("failed to unmarshal as both object and array: %v", err)
	}

	// If it's an array, use the first element
	if len(arrayData) > 0 {
		*r = arrayData[0]
		return nil
	}

	// If it's an empty array, initialize as an empty map
	*r = make(map[string]Hoster)
	return nil
}

type Hoster struct {
	Rd []map[string]FileVariant `json:"rd"`
}

func (h *Hoster) UnmarshalJSON(data []byte) error {
	// Attempt to unmarshal into the expected structure (an object with an "rd" key)
	type Alias Hoster
	var obj Alias
	if err := json.Unmarshal(data, &obj); err == nil {
		*h = Hoster(obj)
		return nil
	}

	// If unmarshalling into an object fails, check if it's an empty array
	var arr []interface{}
	if err := json.Unmarshal(data, &arr); err == nil && len(arr) == 0 {
		// It's an empty array; initialize with no entries
		*h = Hoster{Rd: nil}
		return nil
	}

	// If both attempts fail, return an error
	return fmt.Errorf("hoster: cannot unmarshal JSON data: %s", string(data))
}

type FileVariant struct {
	Filename string `json:"filename"`
	Filesize int    `json:"filesize"`
}

type AddMagnetSchema struct {
	Id  string `json:"id"`
	Uri string `json:"uri"`
}

type torrentInfo struct {
	ID               string  `json:"id"`
	Filename         string  `json:"filename"`
	OriginalFilename string  `json:"original_filename"`
	Hash             string  `json:"hash"`
	Bytes            int64   `json:"bytes"`
	OriginalBytes    int64   `json:"original_bytes"`
	Host             string  `json:"host"`
	Split            int     `json:"split"`
	Progress         float64 `json:"progress"`
	Status           string  `json:"status"`
	Added            string  `json:"added"`
	Files            []struct {
		ID       int    `json:"id"`
		Path     string `json:"path"`
		Bytes    int64  `json:"bytes"`
		Selected int    `json:"selected"`
	} `json:"files"`
	Links   []string `json:"links"`
	Ended   string   `json:"ended,omitempty"`
	Speed   int64    `json:"speed,omitempty"`
	Seeders int      `json:"seeders,omitempty"`
}

type UnrestrictResponse struct {
	Id         string `json:"id"`
	Filename   string `json:"filename"`
	MimeType   string `json:"mimeType"`
	Filesize   int64  `json:"filesize"`
	Link       string `json:"link"`
	Host       string `json:"host"`
	Chunks     int    `json:"chunks"`
	Crc        int    `json:"crc"`
	Download   string `json:"download"`
	Streamable int    `json:"streamable"`
}

type TorrentsResponse struct {
	Id       string    `json:"id"`
	Filename string    `json:"filename"`
	Hash     string    `json:"hash"`
	Bytes    int64     `json:"bytes"`
	Host     string    `json:"host"`
	Split    int64     `json:"split"`
	Progress float64   `json:"progress"`
	Status   string    `json:"status"`
	Added    time.Time `json:"added"`
	Links    []string  `json:"links"`
	Ended    time.Time `json:"ended"`
}

type DownloadsResponse struct {
	Id         string    `json:"id"`
	Filename   string    `json:"filename"`
	MimeType   string    `json:"mimeType"`
	Filesize   int64     `json:"filesize"`
	Link       string    `json:"link"`
	Host       string    `json:"host"`
	HostIcon   string    `json:"host_icon"`
	Chunks     int64     `json:"chunks"`
	Download   string    `json:"download"`
	Streamable int       `json:"streamable"`
	Generated  time.Time `json:"generated"`
}

type ErrorResponse struct {
	Error     string `json:"error"`
	ErrorCode int    `json:"error_code"`
}

type profileResponse struct {
	Id         int64     `json:"id"`
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	Points     int64     `json:"points"`
	Locale     string    `json:"locale"`
	Avatar     string    `json:"avatar"`
	Type       string    `json:"type"`
	Premium    int       `json:"premium"`
	Expiration time.Time `json:"expiration"`
}

type AvailableSlotsResponse struct {
	ActiveSlots int `json:"nb"`
	TotalSlots  int `json:"limit"`
}
