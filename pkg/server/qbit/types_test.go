package qbit

import (
	"testing"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestConvertToQBitTorrentTorrentCalculatesETA(t *testing.T) {
	tests := []struct {
		name     string
		entry    *storage.Entry
		wantLeft int64
		wantETA  int64
	}{
		{
			name:     "active download",
			entry:    &storage.Entry{Size: 2_000, Progress: 0.25, Speed: 100},
			wantLeft: 1_500,
			wantETA:  15,
		},
		{
			name:     "partial second rounds up",
			entry:    &storage.Entry{Size: 100, Progress: 0.5, Speed: 30},
			wantLeft: 50,
			wantETA:  2,
		},
		{
			name:     "unknown speed uses qBittorrent infinity sentinel",
			entry:    &storage.Entry{Size: 100, Progress: 0.5},
			wantLeft: 50,
			wantETA:  qbitInfiniteETA,
		},
		{
			name:     "completed download",
			entry:    &storage.Entry{Size: 100, Progress: 1, Speed: 30},
			wantLeft: 0,
			wantETA:  0,
		},
		{
			name:     "very long ETA is capped",
			entry:    &storage.Entry{Size: qbitInfiniteETA + 1, Speed: 1},
			wantLeft: qbitInfiniteETA + 1,
			wantETA:  qbitInfiniteETA,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertToQBitTorrentTorrent(tt.entry)
			if got.AmountLeft != tt.wantLeft {
				t.Fatalf("AmountLeft = %d, want %d", got.AmountLeft, tt.wantLeft)
			}
			if got.Eta != tt.wantETA {
				t.Fatalf("Eta = %d, want %d", got.Eta, tt.wantETA)
			}
		})
	}
}
