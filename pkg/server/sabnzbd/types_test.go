package sabnzbd

import (
	"testing"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Arrs act on the SABnzbd status, so each phase must keep its wire value.
func TestSabStatus(t *testing.T) {
	tests := map[storage.EntryPhase]string{
		storage.EntryPhaseQueued:      StatusQueued,
		storage.EntryPhaseDownloading: StatusDownloading,
		storage.EntryPhaseProcessing:  StatusDownloading,
		storage.EntryPhasePaused:      StatusPaused,
		storage.EntryPhaseCompleted:   StatusCompleted,
		storage.EntryPhaseFailed:      StatusFailed,
	}
	for phase, want := range tests {
		if got := sabStatus(phase); got != want {
			t.Errorf("%s: got %q, want %q", phase, got, want)
		}
	}
}
