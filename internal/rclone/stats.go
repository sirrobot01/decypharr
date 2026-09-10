package rclone

import "context"

// Stats collects RC statistics. Failed sections retain their zero values.
func (r *Client) Stats(ctx context.Context) map[string]any {
	stats := map[string]any{
		"core":      CoreStatsResponse{},
		"memory":    MemoryStats{},
		"bandwidth": BandwidthStats{},
		"version":   VersionResponse{},
	}
	if core, err := r.GetCoreStats(ctx); err == nil {
		stats["core"] = *core
	}
	if memory, err := r.GetMemoryUsage(ctx); err == nil {
		stats["memory"] = *memory
	}
	if bandwidth, err := r.GetBandwidthStats(ctx); err == nil {
		stats["bandwidth"] = *bandwidth
	}
	if version, err := r.GetVersion(ctx); err == nil {
		stats["version"] = *version
	}
	return stats
}
