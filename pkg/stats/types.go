package stats

import (
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"github.com/sirrobot01/decypharr/pkg/manager"
)

// Snapshot holds a point-in-time stats snapshot.
// Using typed structs avoids map[string]any allocations on every JSON encode.
type Snapshot struct {
	System        SystemStats       `json:"system"`
	Debrids       []types.Stats     `json:"debrids"`
	Mount         MountStats        `json:"mount"`
	Usenet        map[string]any    `json:"usenet,omitempty"`
	ActiveStreams ActiveStreamStats `json:"active_streams"`
	Storage       StorageStats      `json:"storage"`
	Queue         QueueStats        `json:"queue"`
	Arrs          ArrStats          `json:"arrs"`
	Repair        RepairStats       `json:"repair"`
}

type SystemStats struct {
	HeapAllocMB   string `json:"heap_alloc_mb"`
	MemoryUsed    string `json:"memory_used"`
	GCCycles      uint32 `json:"gc_cycles"`
	Goroutines    int    `json:"goroutines"`
	NumCPU        int    `json:"num_cpu"`
	OS            string `json:"os"`
	Arch          string `json:"arch"`
	GoVersion     string `json:"go_version"`
	UptimeSeconds int64  `json:"uptime_seconds"`
	Uptime        string `json:"uptime"`
	StartTime     string `json:"start_time"`
}

type MountStats struct {
	Ready   bool   `json:"ready"`
	Enabled bool   `json:"enabled"`
	Type    string `json:"type,omitempty"`
	Error   string `json:"error,omitempty"`
	// Detail holds the subsystem-specific stats (e.g. VFS counters).
	// nil when mount is not ready.
	Detail map[string]any `json:"detail,omitempty"`
}

type ActiveStreamStats struct {
	Count   int                     `json:"count"`
	Streams []*manager.ActiveStream `json:"streams"`
}

type StorageStats struct {
	DBSize       int64 `json:"db_size"`
	TotalEntries int   `json:"total_entries"`
}

type QueueStats struct {
	Pending int `json:"pending"`
}

type ArrStats struct {
	Count int      `json:"count"`
	Names []string `json:"names"`
}

// RepairStats is the dashboard view of the repair system's state.
type RepairStats struct {
	Enabled bool           `json:"enabled"`
	Active  bool           `json:"active"`
	Health  map[string]int `json:"health,omitempty"`
}
