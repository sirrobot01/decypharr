package manager

import (
	"github.com/sirrobot01/decypharr/internal/rclone"
)

// MountStats is the unified stats struct returned by all MountManager implementations.
// Each mount type populates only its relevant field.
type MountStats struct {
	Enabled  bool          `json:"enabled"`
	Ready    bool          `json:"ready"`
	Type     string        `json:"type,omitempty"`
	Error    string        `json:"error,omitempty"`
	DFS      *DFSDetail    `json:"dfs,omitempty"`
	Rclone   *RcloneDetail `json:"rclone,omitempty"`
	External *RcloneDetail `json:"external,omitempty"`
}

// DFSDetail holds DFS-specific mount statistics.
type DFSDetail struct {
	Backend   string    `json:"backend"`
	Ready     bool      `json:"ready"`
	MountPath string    `json:"mount_path"`
	VFS       VFSDetail `json:"vfs"`
}

// VFSDetail holds VFS manager statistics.
type VFSDetail struct {
	TotalFiles     int32       `json:"total_files"`
	ActiveFiles    int32       `json:"active_files"`
	Cache          CacheDetail `json:"cache"`
	TotalBytesRead int64       `json:"total_bytes_read"`
	TotalErrors    int64       `json:"total_errors"`
}

// CacheDetail holds VFS cache statistics.
type CacheDetail struct {
	TotalSize   int64   `json:"total_size"`
	MaxSize     int64   `json:"max_size"`
	ItemCount   int64   `json:"item_count"`
	Utilization float64 `json:"utilization"`
}

// RcloneDetail holds rclone/external-specific mount statistics.
type RcloneDetail struct {
	Core      rclone.CoreStatsResponse `json:"core"`
	Memory    rclone.MemoryStats       `json:"memory"`
	Bandwidth rclone.BandwidthStats    `json:"bandwidth"`
	Version   rclone.VersionResponse   `json:"version"`
	Mount     *RcloneMountInfo         `json:"mount,omitempty"`
}

// RcloneMountInfo holds rclone mount point information.
type RcloneMountInfo struct {
	LocalPath  string `json:"local_path"`
	WebDAVURL  string `json:"webdav_url"`
	Mounted    bool   `json:"mounted"`
	MountedAt  string `json:"mounted_at,omitempty"`
	ConfigName string `json:"config_name"`
	Error      string `json:"error,omitempty"`
}
