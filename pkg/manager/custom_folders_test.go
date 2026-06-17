package manager

import (
	"os"
	"testing"
	"time"
)

// stubFileInfo lets tests construct an os.FileInfo without touching the
// filesystem. Mirrors what entry.go's FileInfo does in production but stays
// local to keep the test self-contained.
type stubFileInfo struct {
	name string
	size int64
}

func (f *stubFileInfo) Name() string       { return f.name }
func (f *stubFileInfo) Size() int64        { return f.size }
func (f *stubFileInfo) Mode() os.FileMode  { return 0 }
func (f *stubFileInfo) ModTime() time.Time { return time.Time{} }
func (f *stubFileInfo) IsDir() bool        { return true }
func (f *stubFileInfo) Sys() any           { return nil }

// TestMatchesFilterCategory covers the new filterByCategory / filterByNotCategory
// primitives. The whole point: let a virtual folder say "show me only torrents
// added by Sonarr" via `filters: {category: "tv-sonarr"}` without inventing
// any out-of-band metadata — Category is already on every torrent in the
// IndexEntry, set when the *arr POSTs to the qBit add endpoint.
func TestMatchesFilterCategory(t *testing.T) {
	tests := []struct {
		name       string
		folderName string
		filterType string
		filterVal  string
		torrentCat string
		want       bool
	}{
		{
			name:       "category match — sonarr torrent in sonarr folder",
			folderName: "sonarr",
			filterType: filterByCategory,
			filterVal:  "tv-sonarr",
			torrentCat: "tv-sonarr",
			want:       true,
		},
		{
			name:       "category mismatch — radarr torrent excluded from sonarr folder",
			folderName: "sonarr",
			filterType: filterByCategory,
			filterVal:  "tv-sonarr",
			torrentCat: "radarr",
			want:       false,
		},
		{
			name:       "empty category never matches a non-empty filter",
			folderName: "sonarr",
			filterType: filterByCategory,
			filterVal:  "tv-sonarr",
			torrentCat: "",
			want:       false,
		},
		{
			name:       "not_category — torrent NOT in radarr does match",
			folderName: "non-radarr",
			filterType: filterByNotCategory,
			filterVal:  "radarr",
			torrentCat: "tv-sonarr",
			want:       true,
		},
		{
			name:       "not_category — torrent IS radarr does not match",
			folderName: "non-radarr",
			filterType: filterByNotCategory,
			filterVal:  "radarr",
			torrentCat: "radarr",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cf := &CustomFolders{
				filters: map[string][]directoryFilter{
					tt.folderName: {
						{filterType: tt.filterType, value: tt.filterVal},
					},
				},
				folders: []string{tt.folderName},
			}
			got := cf.matchesFilter(
				tt.folderName,
				&stubFileInfo{name: "Some.Release.Name.S01E01.mkv", size: 1024},
				time.Now(),
				tt.torrentCat,
				func() []string { return nil },
			)
			if got != tt.want {
				t.Errorf("matchesFilter(folder=%q, %s=%q, torrentCat=%q) = %v, want %v",
					tt.folderName, tt.filterType, tt.filterVal, tt.torrentCat, got, tt.want)
			}
		})
	}
}

// TestMatchesFilterCategoryInteractsWithOtherFilters verifies category as part
// of an AND-stack with other filter primitives. A "sonarr 4K only" virtual
// folder should match Sonarr+4K and reject Sonarr+1080p / Radarr+4K.
func TestMatchesFilterCategoryInteractsWithOtherFilters(t *testing.T) {
	cf := &CustomFolders{
		filters: map[string][]directoryFilter{
			"sonarr-4k": {
				{filterType: filterByCategory, value: "tv-sonarr"},
				{filterType: filterByInclude, value: "2160p"},
			},
		},
		folders: []string{"sonarr-4k"},
	}

	tests := []struct {
		name       string
		releaseName string
		category   string
		want       bool
	}{
		{"Sonarr + 2160p — match", "Show.S01E01.2160p.WEB.x265.mkv", "tv-sonarr", true},
		{"Sonarr + 1080p — reject (size filter fails)", "Show.S01E01.1080p.WEB.x264.mkv", "tv-sonarr", false},
		{"Radarr + 2160p — reject (category filter fails)", "Movie.2024.2160p.BluRay.x265.mkv", "radarr", false},
		{"Empty category + 2160p — reject", "Anon.Release.2160p.mkv", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cf.matchesFilter(
				"sonarr-4k",
				&stubFileInfo{name: tt.releaseName, size: 4_000_000_000},
				time.Now(),
				tt.category,
				func() []string { return nil },
			)
			if got != tt.want {
				t.Errorf("matchesFilter(release=%q, cat=%q) = %v, want %v", tt.releaseName, tt.category, got, tt.want)
			}
		})
	}
}
