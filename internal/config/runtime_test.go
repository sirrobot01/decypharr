package config

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
)

func TestUpdatePublishesIndependentSnapshots(t *testing.T) {
	Reset()
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)
	before := Get()
	beforeCategories := slices.Clone(before.Categories)
	beforeToken := before.GetAuth().APIToken
	var edited *Config
	after, err := Update(func(next *Config) error {
		edited = next
		next.Categories = append(next.Categories, "added")
		next.Auth.APIToken = "updated"
		return next.SaveAuth(next.Auth)
	})
	if err != nil {
		t.Fatal(err)
	}
	edited.Categories[len(edited.Categories)-1] = "mutated after publication"
	edited.Auth.APIToken = "mutated after publication"
	if !slices.Equal(before.Categories, beforeCategories) || before.GetAuth().APIToken != beforeToken {
		t.Fatal("previous snapshot changed")
	}
	if after.Categories[len(after.Categories)-1] != "added" || after.GetAuth().APIToken != "updated" {
		t.Fatal("published snapshot shares mutable data with its editor")
	}
	auth := after.GetAuth()
	auth.APIToken = "mutated auth copy"
	if after.GetAuth().APIToken != "updated" {
		t.Fatal("GetAuth exposes mutable credentials")
	}
}

func TestConcurrentConfigUpdatesKeepAllChanges(t *testing.T) {
	Reset()
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)
	before := Get()
	initialCount := len(before.Categories)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Go(func() {
			_, err := Update(func(next *Config) error { next.Categories = append(next.Categories, fmt.Sprint(i)); return nil })
			if err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			for range 100 {
				current := Get()
				_ = slices.Clone(current.Categories)
				_ = current.GetAuth()
			}
		})
	}
	wg.Wait()
	if got := len(Get().Categories); got != initialCount+12 {
		t.Fatalf("categories=%d, want %d", got, initialCount+12)
	}
	if len(before.Categories) != initialCount {
		t.Fatal("old snapshot changed")
	}
}

func TestFailedSaveDoesNotPublishConfig(t *testing.T) {
	Reset()
	SetConfigPath(t.TempDir())
	t.Cleanup(Reset)
	before := Get()
	if err := os.Remove(before.JsonFile()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(before.JsonFile(), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := Update(func(next *Config) error { next.AppURL = "https://changed.test"; return nil }); err == nil {
		t.Fatal("save to a directory succeeded")
	}
	if Get() != before {
		t.Fatal("failed save published a new snapshot")
	}
}

func TestStartupSettingsRequireRestart(t *testing.T) {
	for _, field := range []string{"workers", "retries", "schedule", "notifications"} {
		t.Run(field, func(t *testing.T) {
			before := &Config{}
			after := *before
			switch field {
			case "workers":
				after.MaxActiveDownloads = 2
			case "retries":
				after.Retries = 2
			case "schedule":
				after.RefreshInterval = "5m"
			case "notifications":
				after.Notifications.Enabled = true
			}
			if !before.RequiresRestart(&after) {
				t.Fatal("startup setting did not require restart")
			}
		})
	}
}
