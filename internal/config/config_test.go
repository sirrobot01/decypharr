package config

import (
	"os"
	"path/filepath"
	"testing"
)

func withConfigPath(t *testing.T, path string) {
	t.Helper()
	previous := GetMainPath()
	SetConfigPath(path)
	t.Cleanup(func() {
		SetConfigPath(previous)
	})
}

func TestSetDefaultsMigratesLegacyDebridAddSamples(t *testing.T) {
	cfg := &Config{
		Debrids: []Debrid{{
			Provider:   "realdebrid",
			Name:       "realdebrid",
			APIKey:     "token",
			AddSamples: true,
		}},
	}

	cfg.setDefaults()

	if !cfg.AllowSamples {
		t.Fatal("expected legacy debrid add_samples to enable root allow_samples")
	}
	if cfg.Debrids[0].AddSamples {
		t.Fatal("expected legacy debrid add_samples to be cleared after migration")
	}
}

func TestSaveWithBlankConfigPathWritesConfigInWorkingDir(t *testing.T) {
	withConfigPath(t, "")
	tmp := t.TempDir()
	previousWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousWD); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	cfg := &Config{}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() with blank config path returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "config.json")); err != nil {
		t.Fatalf("expected config.json in working directory: %v", err)
	}
}

func TestSaveCreatesConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "config")
	withConfigPath(t, dir)

	cfg := &Config{}
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		t.Fatalf("expected config.json in config directory: %v", err)
	}
}

func TestSaveAuthCreatesConfigDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "config")
	withConfigPath(t, dir)

	cfg := &Config{}
	if err := cfg.SaveAuth(&Auth{Username: "admin", Password: "hash", APIToken: "token"}); err != nil {
		t.Fatalf("SaveAuth() returned error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "auth.json")); err != nil {
		t.Fatalf("expected auth.json in config directory: %v", err)
	}
}

func TestRequiresRestartAllowsRuntimeFields(t *testing.T) {
	current := &Config{
		AllowedExt:   []string{"mkv"},
		AllowSamples: false,
		MinFileSize:  "10MB",
		MaxFileSize:  "50GB",
		NZBUserAgent: "old-agent",
		Notifications: Notifications{
			Enabled:    true,
			WebhookURL: "https://old.example/webhook",
		},
		Repair: RepairConfig{
			Workers: 5,
		},
		Port: "8282",
	}
	next := *current
	next.AllowedExt = []string{"mkv", "mp4"}
	next.AllowSamples = true
	next.MinFileSize = "1MB"
	next.MaxFileSize = "100GB"
	next.NZBUserAgent = "new-agent"
	next.Notifications.WebhookURL = "https://new.example/webhook"
	next.Repair.Workers = 10

	if current.RequiresRestart(&next) {
		t.Fatal("expected runtime-only field changes to avoid restart")
	}
}

func TestRequiresRestartDetectsColdFields(t *testing.T) {
	current := &Config{Port: "8282"}
	next := &Config{Port: "9090"}

	if !current.RequiresRestart(next) {
		t.Fatal("expected port change to require restart")
	}
}
