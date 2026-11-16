package storage

import (
	"testing"

	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestNeedsUpdateSummaryRows(t *testing.T) {
	withFiles := &ProviderEntry{
		ID:     "7",
		Status: debridTypes.TorrentStatusDownloaded,
		Files:  map[string]*ProviderFile{"a.mkv": {Id: "1", Link: "torbox://7/1"}},
	}
	noFiles := &ProviderEntry{ID: "7", Status: debridTypes.TorrentStatusDownloaded}

	tests := []struct {
		name      string
		placement *ProviderEntry
		remote    *debridTypes.Torrent
		want      bool
	}{
		{
			name:      "summary with a different status is not authoritative",
			placement: withFiles,
			remote:    &debridTypes.Torrent{Id: "7", Status: debridTypes.TorrentStatusDownloading, SummaryOnly: true},
			want:      false,
		},
		{
			name:      "summary with a changed provider ID still updates",
			placement: withFiles,
			remote:    &debridTypes.Torrent{Id: "8", Status: debridTypes.TorrentStatusDownloaded, SummaryOnly: true},
			want:      true,
		},
		{
			name:      "summary without files does not re-hydrate a file-less placement",
			placement: noFiles,
			remote:    &debridTypes.Torrent{Id: "7", Status: debridTypes.TorrentStatusDownloaded, SummaryOnly: true},
			want:      false,
		},
		{
			name:      "full row with a different status updates",
			placement: withFiles,
			remote:    &debridTypes.Torrent{Id: "7", Status: debridTypes.TorrentStatusError},
			want:      true,
		},
		{
			name:      "full row with files hydrates a file-less placement",
			placement: noFiles,
			remote: &debridTypes.Torrent{Id: "7", Status: debridTypes.TorrentStatusDownloaded,
				Files: map[string]debridTypes.File{"a.mkv": {Id: "1"}}},
			want: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.placement.NeedsUpdate(tt.remote); got != tt.want {
				t.Fatalf("NeedsUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}
