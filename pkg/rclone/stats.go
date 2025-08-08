package rclone

import (
	"encoding/json"
	"fmt"
)

// Stats represents rclone statistics
type Stats struct {
	CoreStats     map[string]interface{} `json:"coreStats"`
	TransferStats map[string]interface{} `json:"transferStats"`
	MountStats    map[string]*MountInfo  `json:"mountStats"`
}

// GetStats retrieves statistics from the rclone RC server
func (m *Manager) GetStats() (*Stats, error) {
	if !m.IsReady() {
		return nil, fmt.Errorf("rclone RC server not ready")
	}

	// Get core stats
	req := RCRequest{
		Command: "core/stats",
	}

	resp, err := m.makeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get rclone stats: %w", err)
	}

	// Parse the response
	var coreStatsResp CoreStatsResponse
	if respBytes, err := json.Marshal(resp.Result); err == nil {
		json.Unmarshal(respBytes, &coreStatsResp)
	}

	// Get mount stats
	mountStats := m.GetAllMounts()

	stats := &Stats{
		CoreStats:     coreStatsResp.CoreStats,
		TransferStats: coreStatsResp.TransferStats,
		MountStats:    mountStats,
	}

	return stats, nil
}

// GetMemoryUsage returns memory usage statistics
func (m *Manager) GetMemoryUsage() (map[string]interface{}, error) {
	if !m.IsReady() {
		return nil, fmt.Errorf("rclone RC server not ready")
	}

	req := RCRequest{
		Command: "core/memstats",
	}

	resp, err := m.makeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get memory stats: %w", err)
	}

	if memStats, ok := resp.Result.(map[string]interface{}); ok {
		return memStats, nil
	}

	return nil, fmt.Errorf("invalid memory stats response")
}

// GetBandwidthStats returns bandwidth usage for all transfers
func (m *Manager) GetBandwidthStats() (map[string]interface{}, error) {
	if !m.IsReady() {
		return nil, fmt.Errorf("rclone RC server not ready")
	}

	req := RCRequest{
		Command: "core/bwlimit",
	}

	resp, err := m.makeRequest(req)
	if err != nil {
		// Bandwidth stats might not be available, return empty
		return map[string]interface{}{}, nil
	}

	if bwStats, ok := resp.Result.(map[string]interface{}); ok {
		return bwStats, nil
	}

	return map[string]interface{}{}, nil
}

// GetVersion returns rclone version information
func (m *Manager) GetVersion() (map[string]interface{}, error) {
	if !m.IsReady() {
		return nil, fmt.Errorf("rclone RC server not ready")
	}

	req := RCRequest{
		Command: "core/version",
	}

	resp, err := m.makeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get version: %w", err)
	}

	if version, ok := resp.Result.(map[string]interface{}); ok {
		return version, nil
	}

	return nil, fmt.Errorf("invalid version response")
}

// GetConfigDump returns the current rclone configuration
func (m *Manager) GetConfigDump() (map[string]interface{}, error) {
	if !m.IsReady() {
		return nil, fmt.Errorf("rclone RC server not ready")
	}

	req := RCRequest{
		Command: "config/dump",
	}

	resp, err := m.makeRequest(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get config dump: %w", err)
	}

	if config, ok := resp.Result.(map[string]interface{}); ok {
		return config, nil
	}

	return nil, fmt.Errorf("invalid config dump response")
}
