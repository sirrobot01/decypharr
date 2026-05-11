package rclone

import (
	"context"

	json "github.com/bytedance/sonic"

	"github.com/sirrobot01/decypharr/internal/rclone"
)

// Stats represents rclone statistics
type Stats struct {
	Type      string                   `json:"type"`
	Enabled   bool                     `json:"enabled"`
	Ready     bool                     `json:"ready"`
	Core      rclone.CoreStatsResponse `json:"core"`
	Memory    rclone.MemoryStats       `json:"memory"`
	Mount     *MountInfo               `json:"mounts"`
	Bandwidth rclone.BandwidthStats    `json:"bandwidth"`
	Version   rclone.VersionResponse   `json:"version"`
}

// Stats retrieves statistics from the rclone RC server
func (m *Manager) Stats() map[string]interface{} {
	empty := make(map[string]interface{})
	stats := &Stats{}
	stats.Ready = m.IsReady()
	stats.Enabled = true
	stats.Type = m.Type()

	ctx := context.Background()

	coreStats, err := m.client.GetCoreStats(ctx)
	if err == nil {
		stats.Core = *coreStats
	}

	// GetReader memory usage
	memStats, err := m.client.GetMemoryUsage(ctx)
	if err == nil {
		stats.Memory = *memStats
	}
	// GetReader bandwidth stats
	bwStats, err := m.client.GetBandwidthStats(ctx)
	if err == nil && bwStats != nil {
		stats.Bandwidth = *bwStats
	}

	// Add mount infos
	mountInfo := m.getMountInfo()
	stats.Mount = mountInfo

	// GetReader version info
	versionResp, err := m.client.GetVersion(ctx)
	if err == nil {
		stats.Version = *versionResp
	}

	// Convert to map[string]interface{}
	statsMap := make(map[string]interface{})
	data, err := json.Marshal(stats)
	if err != nil {
		return empty
	}
	if err := json.Unmarshal(data, &statsMap); err != nil {
		return empty
	}

	return statsMap
}
