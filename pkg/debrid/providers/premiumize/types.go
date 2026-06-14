package premiumize

import (
	"strconv"
	"strings"
)

// flexInt tolerates Premiumize returning sizes as either JSON numbers or strings.
type flexInt int64

func (f *flexInt) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*f = flexInt(v)
		return nil
	}
	if fv, err := strconv.ParseFloat(s, 64); err == nil {
		*f = flexInt(int64(fv))
		return nil
	}
	*f = 0 // tolerate unexpected values rather than failing the whole decode
	return nil
}

// GET /account/info
type accountInfo struct {
	Status       string  `json:"status"`
	Message      string  `json:"message"`
	CustomerID   string  `json:"customer_id"`
	PremiumUntil float64 `json:"premium_until"`
	LimitUsed    float64 `json:"limit_used"`
	SpaceUsed    float64 `json:"space_used"`
}

// GET /cache/check
type cacheCheckResponse struct {
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Response []bool   `json:"response"`
	Filename []string `json:"filename"`
}

// POST /transfer/directdl
type directDLResponse struct {
	Status   string         `json:"status"`
	Message  string         `json:"message"`
	Location string         `json:"location"`
	Filename string         `json:"filename"`
	Filesize flexInt        `json:"filesize"`
	Content  []directDLFile `json:"content"`
}

type directDLFile struct {
	Path            string  `json:"path"`
	Size            flexInt `json:"size"`
	Link            string  `json:"link"`
	StreamLink      string  `json:"stream_link"`
	TranscodeStatus string  `json:"transcode_status"`
}

// POST /transfer/create
type transferCreateResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
}

// GET /transfer/list
type transferListResponse struct {
	Status    string         `json:"status"`
	Message   string         `json:"message"`
	Transfers []transferItem `json:"transfers"`
}

type transferItem struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Message  string  `json:"message"`
	Status   string  `json:"status"`
	Progress float64 `json:"progress"`
	FolderID string  `json:"folder_id"`
	FileID   string  `json:"file_id"`
	Src      string  `json:"src"`
}

// GET /folder/list and GET /item/details
type folderListResponse struct {
	Status   string       `json:"status"`
	Message  string       `json:"message"`
	Content  []folderItem `json:"content"`
	Name     string       `json:"name"`
	FolderID string       `json:"folder_id"`
	ParentID string       `json:"parent_id"`
}

type folderItem struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Type       string  `json:"type"` // "file" | "folder"
	Size       flexInt `json:"size"`
	Link       string  `json:"link"`
	StreamLink string  `json:"stream_link"`
	CreatedAt  int64   `json:"created_at"`
}

// Generic status-only response (e.g. /transfer/delete)
type statusResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// contentRoot returns the shared top-level folder of a directdl content list
// (e.g. "Big Buck Bunny" for "Big Buck Bunny/*"), or "" if the files don't
// share one — directdl's top-level `filename` is only the first file's path,
// so it's unsuitable as a torrent name.
func contentRoot(content []directDLFile) string {
	if len(content) == 0 {
		return ""
	}
	idx := strings.IndexByte(content[0].Path, '/')
	if idx <= 0 {
		return ""
	}
	root := content[0].Path[:idx]
	for _, f := range content {
		i := strings.IndexByte(f.Path, '/')
		if i <= 0 || f.Path[:i] != root {
			return ""
		}
	}
	return root
}
