package premiumize

import (
	"encoding/json"
	"strconv"
)

type apiError struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

type accountInfoResponse struct {
	Status        string          `json:"status"`
	CustomerID    json.RawMessage `json:"customer_id"`
	PremiumUntil  *int64          `json:"premium_until"`
	LimitUsed     float64         `json:"limit_used"`
	BoosterPoints int             `json:"booster_points"`
	Message       string          `json:"message"`
	Code          string          `json:"code"`
}

func (r accountInfoResponse) customerIDInt64() int64 {
	var id int64
	if err := json.Unmarshal(r.CustomerID, &id); err == nil {
		return id
	}
	var idStr string
	if err := json.Unmarshal(r.CustomerID, &idStr); err == nil {
		parsed, _ := strconv.ParseInt(idStr, 10, 64)
		return parsed
	}
	return 0
}

type transferCreateResponse struct {
	Status  string `json:"status"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"`
	Message string `json:"message"`
	Code    string `json:"code"`
}

type transferListResponse struct {
	Status    string               `json:"status"`
	Transfers []premiumizeTransfer `json:"transfers"`
	Message   string               `json:"message"`
	Code      string               `json:"code"`
}

type premiumizeTransfer struct {
	ID       string           `json:"id"`
	Name     string           `json:"name"`
	Status   string           `json:"status"`
	Progress float64          `json:"progress"`
	Message  string           `json:"message"`
	FolderID nullableString   `json:"folder_id"`
	FileID   nullableString   `json:"file_id"`
	Src      string           `json:"src,omitempty"`
	Created  flexibleUnixTime `json:"created_at,omitempty"`
}

type cacheCheckResponse struct {
	Status   string   `json:"status"`
	Response []bool   `json:"response"`
	Filename []string `json:"filename"`
	Filesize []any    `json:"filesize"`
	Message  string   `json:"message"`
	Code     string   `json:"code"`
}

type itemDetailsResponse struct {
	Status    string           `json:"status"`
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Size      int64            `json:"size"`
	CreatedAt flexibleUnixTime `json:"created_at"`
	FolderID  string           `json:"folder_id"`
	MimeType  string           `json:"mime_type"`
	Link      string           `json:"link"`
	Message   string           `json:"message"`
	Code      string           `json:"code"`
}

type folderListResponse struct {
	Status   string           `json:"status"`
	Name     string           `json:"name"`
	ParentID string           `json:"parent_id"`
	FolderID string           `json:"folder_id"`
	Content  []premiumizeItem `json:"content"`
	Message  string           `json:"message"`
	Code     string           `json:"code"`
}

type premiumizeItem struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Type      string           `json:"type"`
	CreatedAt flexibleUnixTime `json:"created_at"`
	Size      int64            `json:"size"`
	MimeType  string           `json:"mime_type"`
	Link      string           `json:"link"`
}

type nullableString string

func (s *nullableString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = ""
		return nil
	}
	var v string
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	*s = nullableString(v)
	return nil
}

func (s nullableString) String() string {
	return string(s)
}

type flexibleUnixTime struct {
	Unix int64
}

func (t *flexibleUnixTime) UnmarshalJSON(data []byte) error {
	if string(data) == "null" || string(data) == `""` {
		t.Unix = 0
		return nil
	}
	var i int64
	if err := json.Unmarshal(data, &i); err == nil {
		t.Unix = i
		return nil
	}
	var f float64
	if err := json.Unmarshal(data, &f); err == nil {
		t.Unix = int64(f)
		return nil
	}
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	t.Unix = parsed
	return nil
}
