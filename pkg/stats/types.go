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
	Access        AccessStats       `json:"access"`
	Usenet        map[string]any    `json:"usenet,omitempty"`
	ActiveStreams ActiveStreamStats `json:"active_streams"`
	Storage       StorageStats      `json:"storage"`
	Queue         QueueStats        `json:"queue"`
	Arrs          ArrStats          `json:"arrs"`
	Repair        RepairStats       `json:"repair"`
}

// AccessStats describes the network file servers that expose the library to
// external clients (as opposed to the local Mount). The host is not included:
// the dashboard fills it in from the browser's current location, which is the
// address the user actually reaches Decypharr on.
type AccessStats struct {
	WebDAV WebDAVAccess `json:"webdav"`
	NFS    NFSAccess    `json:"nfs"`
	SMB    SMBAccess    `json:"smb"`
}

// NFSAccess describes the NFSv4 listener. NFSv4 is single-port with no
// portmapper, so the dashboard's mount commands pass the port explicitly.
type NFSAccess struct {
	Enabled bool   `json:"enabled"`
	Port    uint16 `json:"port"`
}

type SMBAccess struct {
	Enabled   bool   `json:"enabled"`
	Port      uint16 `json:"port"`
	ShareName string `json:"share_name"`
	Username  string `json:"username"`
}

type WebDAVAccess struct {
	Enabled      bool   `json:"enabled"`
	Path         string `json:"path"`          // URL path, e.g. "/webdav"
	Port         string `json:"port"`          // main web server port
	AuthRequired bool   `json:"auth_required"` // basic auth enforced on WebDAV
}

type SystemStats struct {
	// MemoryUsed is the process's real heap footprint: memory committed from
	// the OS that has not been released back (Sys - HeapReleased). This is the
	// number that tracks RSS, not Sys — most of Sys is reserved-but-released
	// address space.
	MemoryUsed string `json:"memory_used"`
	// HeapAllocMB is the live heap (HeapAlloc): bytes of in-use, reachable
	// objects. Fluctuates between GC cycles up to NextGC.
	HeapAllocMB string `json:"heap_alloc_mb"`
	// HeapInuseMB is the bytes in heap spans currently in use (HeapInuse).
	HeapInuseMB string `json:"heap_inuse_mb"`
	// HeapReleasedMB is heap memory already returned to the OS (HeapReleased).
	HeapReleasedMB string `json:"heap_released_mb"`
	// SysMB is the total address space reserved from the OS (Sys). Includes
	// released memory, so it is NOT a measure of real usage.
	SysMB         string `json:"sys_mb"`
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
	Active  int `json:"active"`
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
