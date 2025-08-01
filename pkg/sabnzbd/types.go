package sabnzbd

// SABnzbd API response types based on official documentation

var (
	Version = "4.5.0"
)

// QueueResponse represents the queue status response
type QueueResponse struct {
	Queue   Queue  `json:"queue"`
	Status  bool   `json:"status"`
	Version string `json:"version"`
}

// Queue represents the download queue
type Queue struct {
	Version string      `json:"version"`
	Slots   []QueueSlot `json:"slots"`
}

// QueueSlot represents a download in the queue
type QueueSlot struct {
	Status     string  `json:"status"`
	TimeLeft   string  `json:"timeleft"`
	Mb         int64   `json:"mb"`
	Filename   string  `json:"filename"`
	Priority   string  `json:"priority"`
	Cat        string  `json:"cat"`
	MBLeft     int64   `json:"mbleft"`
	Percentage float64 `json:"percentage"`
	NzoId      string  `json:"nzo_id"`
	Size       int64   `json:"size"`
}

// HistoryResponse represents the history response
type HistoryResponse struct {
	History History `json:"history"`
}

// History represents the download history
type History struct {
	Version string        `json:"version"`
	Paused  bool          `json:"paused"`
	Slots   []HistorySlot `json:"slots"`
}

// HistorySlot represents a completed download
type HistorySlot struct {
	Status      string `json:"status"`
	Name        string `json:"name"`
	NZBName     string `json:"nzb_name"`
	NzoId       string `json:"nzo_id"`
	Category    string `json:"category"`
	FailMessage string `json:"fail_message"`
	Bytes       int64  `json:"bytes"`
	Storage     string `json:"storage"`
}

// StageLog represents processing stages
type StageLog struct {
	Name    string   `json:"name"`
	Actions []string `json:"actions"`
}

// VersionResponse represents version information
type VersionResponse struct {
	Version string `json:"version"`
}

// StatusResponse represents general status
type StatusResponse struct {
	Status bool   `json:"status"`
	Error  string `json:"error,omitempty"`
}

// FullStatusResponse represents the full status response with queue and history
type FullStatusResponse struct {
	Queue   Queue   `json:"queue"`
	History History `json:"history"`
	Status  bool    `json:"status"`
	Version string  `json:"version"`
}

// AddNZBRequest represents the request to add an NZB
type AddNZBRequest struct {
	Name     string `json:"name"`
	Cat      string `json:"cat"`
	Script   string `json:"script"`
	Priority string `json:"priority"`
	PP       string `json:"pp"`
	Password string `json:"password"`
	NZBData  []byte `json:"nzb_data"`
	URL      string `json:"url"`
}

// AddNZBResponse represents the response when adding an NZB
type AddNZBResponse struct {
	Status bool     `json:"status"`
	NzoIds []string `json:"nzo_ids"`
	Error  string   `json:"error,omitempty"`
}

// API Mode constants
const (
	ModeQueue      = "queue"
	ModeHistory    = "history"
	ModeConfig     = "config"
	ModeGetConfig  = "get_config"
	ModeAddURL     = "addurl"
	ModeAddFile    = "addfile"
	ModeVersion    = "version"
	ModePause      = "pause"
	ModeResume     = "resume"
	ModeDelete     = "delete"
	ModeShutdown   = "shutdown"
	ModeRestart    = "restart"
	ModeGetCats    = "get_cats"
	ModeGetScripts = "get_scripts"
	ModeGetFiles   = "get_files"
	ModeRetry      = "retry"
	ModeStatus     = "status"
	ModeFullStatus = "fullstatus"
)

// Status constants
const (
	StatusQueued      = "Queued"
	StatusPaused      = "Paused"
	StatusDownloading = "downloading"
	StatusProcessing  = "Processing"
	StatusCompleted   = "Completed"
	StatusFailed      = "Failed"
	StatusGrabbing    = "Grabbing"
	StatusPropagating = "Propagating"
	StatusVerifying   = "Verifying"
	StatusRepairing   = "Repairing"
	StatusExtracting  = "Extracting"
	StatusMoving      = "Moving"
	StatusRunning     = "Running"
)

// Priority constants
const (
	PriorityForced = "2"
	PriorityHigh   = "1"
	PriorityNormal = "0"
	PriorityLow    = "-1"
	PriorityStop   = "-2"
)
