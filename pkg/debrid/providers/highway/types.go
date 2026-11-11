package highway

// Response shapes of the High-Way transfer API (https://cloud.high-way.me/center-api/transfers.php).
// The API only adds fields over time, so unknown fields are ignored.

type addResponse struct {
	Id      string `json:"id"`
	Service string `json:"service"`
	State   string `json:"state"`
}

type transferFile struct {
	Id          string  `json:"id"`
	Index       *int    `json:"index"`
	Path        string  `json:"path"`
	Size        int64   `json:"size"`
	Partial     bool    `json:"partial"`
	DownloadURL *string `json:"download_url"`
}

type transfer struct {
	Id          string         `json:"id"`
	Service     string         `json:"service"`
	Name        string         `json:"name"`
	State       string         `json:"state"`
	StateDetail string         `json:"state_detail"`
	Progress    float64        `json:"progress"`
	Size        int64          `json:"size"`
	Hash        *string        `json:"hash"`
	AddedAt     *int64         `json:"added_at"`
	Seeder      int            `json:"seeder"`
	Files       []transferFile `json:"files"`
}

type listResponse struct {
	Transfers []transfer `json:"transfers"`
	Total     int        `json:"total"`
}

type fileResponse struct {
	Id    string       `json:"id"`
	State string       `json:"state"`
	File  transferFile `json:"file"`
}

type checkItem struct {
	Known bool    `json:"known"`
	Ready bool    `json:"ready"`
	Id    *string `json:"id"`
}

type checkResponse struct {
	Results map[string]checkItem `json:"results"`
}

type removeResponse struct {
	Removed bool `json:"removed"`
	// Only set when removed is false: "not_found" or "not_owner".
	Reason string `json:"reason"`
}

type statsResponse struct {
	Username      string `json:"username"`
	UserId        int64  `json:"user_id"`
	Plan          string `json:"plan"`
	Kind          string `json:"kind"`
	Premium       bool   `json:"premium"`
	PlanExpiresAt *int64 `json:"plan_expires_at"`
	Transfers     struct {
		Active    int `json:"active"`
		Limit     int `json:"limit"`
		Available int `json:"available"`
	} `json:"transfers"`
}
