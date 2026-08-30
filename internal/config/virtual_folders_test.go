package config

import (
	"strings"
	"testing"
)

func TestMigrateVirtualFolders(t *testing.T) {
	t.Parallel()
	cfg := Config{CustomFolders: map[string]CustomFolders{
		"Recently Added": {Filters: map[string]string{"last_added": "7d"}},
		"4K":             {Filters: map[string]string{"exclude": "sample", "include": "2160p"}},
	}}

	cfg.MigrateVirtualFolders()

	if cfg.CustomFolders != nil {
		t.Fatal("legacy custom folders were not cleared")
	}
	if len(cfg.VirtualFolders) != 2 {
		t.Fatalf("got %d virtual folders, want 2", len(cfg.VirtualFolders))
	}
	if cfg.VirtualFolders[0].Name != "4K" {
		t.Fatalf("migration order is not deterministic: first folder is %q", cfg.VirtualFolders[0].Name)
	}
	conditions := cfg.VirtualFolders[0].Conditions
	if len(conditions) != 2 || conditions[0].Operator != VirtualFolderOperatorContains || conditions[1].Operator != VirtualFolderOperatorNotContains {
		t.Fatalf("unexpected migrated conditions: %#v", conditions)
	}
}

func TestMigrateAdvertisedLegacyNameAndCategoryFilters(t *testing.T) {
	t.Parallel()
	cfg := Config{CustomFolders: map[string]CustomFolders{
		"Movies": {Filters: map[string]string{"name": "*movie?*", "category": "radarr"}},
	}}
	cfg.MigrateVirtualFolders()
	conditions := cfg.VirtualFolders[0].Conditions
	if len(conditions) != 2 {
		t.Fatalf("got %d conditions, want 2", len(conditions))
	}
	if conditions[0].Field != VirtualFolderFieldEntryName || conditions[0].Operator != VirtualFolderOperatorMatchesRegex {
		t.Fatalf("legacy name wildcard was not migrated to a name regex: %#v", conditions[0])
	}
	if conditions[1].Field != VirtualFolderFieldCategory || conditions[1].Operator != VirtualFolderOperatorContains {
		t.Fatalf("legacy category was not migrated: %#v", conditions[1])
	}
}

func TestValidateVirtualFoldersRejectsCollisions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		folders []VirtualFolder
		debrids []Debrid
		want    string
	}{
		{name: "built in", folders: []VirtualFolder{{Name: "__ALL__"}}, want: "conflicts"},
		{name: "provider", folders: []VirtualFolder{{Name: "RealDebrid"}}, debrids: []Debrid{{Name: "realdebrid"}}, want: "conflicts"},
		{name: "duplicate", folders: []VirtualFolder{{Name: "Movies"}, {Name: "movies"}}, want: "more than once"},
		{name: "path separator", folders: []VirtualFolder{{Name: "TV/Shows"}}, want: "not portable"},
		{name: "windows reserved", folders: []VirtualFolder{{Name: "CON"}}, want: "reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Config{VirtualFolders: tt.folders, Debrids: tt.debrids}
			err := cfg.ValidateVirtualFolders()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateVirtualFolders() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestValidateVirtualFolderConditions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		condition VirtualFolderCondition
		wantError string
	}{
		{name: "regex", condition: VirtualFolderCondition{Field: VirtualFolderFieldEntryName, Operator: VirtualFolderOperatorMatchesRegex, Value: "["}, wantError: "regular expression"},
		{name: "size", condition: VirtualFolderCondition{Field: VirtualFolderFieldSize, Operator: VirtualFolderOperatorGreaterThan, Value: "huge"}, wantError: "invalid size"},
		{name: "duration", condition: VirtualFolderCondition{Field: VirtualFolderFieldAdded, Operator: VirtualFolderOperatorWithinLast, Value: "soon"}, wantError: "invalid duration"},
		{name: "count", condition: VirtualFolderCondition{Field: VirtualFolderFieldFileCount, Operator: VirtualFolderOperatorGreaterThan, Value: "1.5"}, wantError: "whole number"},
		{name: "field operator", condition: VirtualFolderCondition{Field: VirtualFolderFieldSize, Operator: VirtualFolderOperatorContains, Value: "1GB"}, wantError: "not valid for size"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			folder := VirtualFolder{Name: "Test", Match: VirtualFolderMatchAll, Conditions: []VirtualFolderCondition{tt.condition}}
			err := ValidateVirtualFolder(folder, nil)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("ValidateVirtualFolder() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestValidateVirtualFolderAllowsEmptyConditions(t *testing.T) {
	t.Parallel()
	err := ValidateVirtualFolder(VirtualFolder{Name: "Everything", Match: VirtualFolderMatchAll}, nil)
	if err != nil {
		t.Fatalf("empty conditions should be a valid all-items view: %v", err)
	}
}

func TestVirtualFoldersAreRuntimeApplicable(t *testing.T) {
	t.Parallel()
	current := Config{VirtualFolders: []VirtualFolder{{Name: "Old"}}}
	next := Config{VirtualFolders: []VirtualFolder{{Name: "New"}}}
	if current.RequiresRestart(&next) {
		t.Fatal("changing only virtual folders should not require a service restart")
	}
}
