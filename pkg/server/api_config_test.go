package server

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestMergeConfigUpdatePreservesOmittedFields(t *testing.T) {
	current := config.Config{
		Port:     "9000",
		LogLevel: "info",
		Debrids: []config.Debrid{{
			Name:   "realdebrid",
			APIKey: "secret",
		}},
		Mount: config.Mount{
			Type:      config.MountTypeDFS,
			MountPath: "/mnt/decypharr",
		},
		Notifications: config.Notifications{
			Enabled:    true,
			WebhookURL: "https://example.com/webhook",
		},
	}

	merged, err := mergeConfigUpdate(&current, strings.NewReader(`{"log_level":"debug"}`))
	if err != nil {
		t.Fatalf("merge config update: %v", err)
	}

	if merged.LogLevel != "debug" {
		t.Fatalf("expected updated log level, got %q", merged.LogLevel)
	}
	if merged.Port != current.Port {
		t.Fatalf("expected port %q to be preserved, got %q", current.Port, merged.Port)
	}
	if !reflect.DeepEqual(merged.Debrids, current.Debrids) {
		t.Fatalf("expected debrid config to be preserved, got %#v", merged.Debrids)
	}
	if !reflect.DeepEqual(merged.Mount, current.Mount) {
		t.Fatalf("expected mount config to be preserved, got %#v", merged.Mount)
	}
	if !reflect.DeepEqual(merged.Notifications, current.Notifications) {
		t.Fatalf("expected notification config to be preserved, got %#v", merged.Notifications)
	}
}

func TestMergeConfigUpdateMergesNestedObjects(t *testing.T) {
	current := config.Config{
		Mount: config.Mount{
			Type:      config.MountTypeRclone,
			MountPath: "/mnt/decypharr",
		},
	}

	merged, err := mergeConfigUpdate(&current, strings.NewReader(`{"mount":{"type":"dfs"}}`))
	if err != nil {
		t.Fatalf("merge config update: %v", err)
	}

	if merged.Mount.Type != config.MountTypeDFS {
		t.Fatalf("expected mount type %q, got %q", config.MountTypeDFS, merged.Mount.Type)
	}
	if merged.Mount.MountPath != current.Mount.MountPath {
		t.Fatalf("expected mount path %q to be preserved, got %q", current.Mount.MountPath, merged.Mount.MountPath)
	}
}

func TestMergeConfigUpdateAllowsExplicitClear(t *testing.T) {
	current := config.Config{Debrids: []config.Debrid{{Name: "realdebrid", APIKey: "secret"}}}

	merged, err := mergeConfigUpdate(&current, strings.NewReader(`{"debrids":[]}`))
	if err != nil {
		t.Fatalf("merge config update: %v", err)
	}

	if len(merged.Debrids) != 0 {
		t.Fatalf("expected debrid config to be cleared, got %#v", merged.Debrids)
	}
}
