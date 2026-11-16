package config

import "testing"

// The download speed cap applies live after a save, without a restart.
func TestMaxDownloadSpeedAppliesWithoutRestart(t *testing.T) {
	before := &Config{}
	after := &Config{MaxDownloadSpeed: "10MB"}
	if before.RequiresRestart(after) {
		t.Fatal("a download speed change requires a restart")
	}
	if got := after.GetMaxDownloadSpeed(); got != 10<<20 {
		t.Fatalf("GetMaxDownloadSpeed = %d, want 10MB", got)
	}
}
