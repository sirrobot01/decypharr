package rclone

import "context"

// Stats returns mount metadata and RC statistics.
func (m *Manager) Stats() map[string]any {
	stats := m.client.Stats(context.Background())
	stats["ready"] = m.IsReady()
	stats["enabled"] = true
	stats["type"] = m.Type()
	stats["mounts"] = m.getMountInfo()
	return stats
}
