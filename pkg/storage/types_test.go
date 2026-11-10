package storage

import (
	"errors"
	"testing"

	debridTypes "github.com/sirrobot01/decypharr/pkg/debrid/types"
)

func TestGetFirstFileReturnsOnlyActiveFiles(t *testing.T) {
	active := &File{Name: "active"}
	item := &EntryItem{Files: map[string]*File{"deleted": {Deleted: true}, "nil": nil}}
	if file, err := item.GetFirstFile(); file != nil || err == nil {
		t.Fatalf("no active files: got %v, %v", file, err)
	}
	item.Files["active"] = active
	if file, err := item.GetFirstFile(); file != active || err != nil {
		t.Fatalf("active file: got %v, %v", file, err)
	}
}

func TestEntryPhase(t *testing.T) {
	tests := []struct {
		name  string
		entry Entry
		want  EntryPhase
	}{
		{"waiting for a slot", Entry{State: EntryStateDownloading, Status: debridTypes.TorrentStatusQueued}, EntryPhaseQueued},
		{"on the provider", Entry{State: EntryStateDownloading, Status: debridTypes.TorrentStatusDownloading}, EntryPhaseDownloading},
		{"provider done", Entry{State: EntryStateDownloading, Status: debridTypes.TorrentStatusDownloaded}, EntryPhaseProcessing},
		{"local action running", Entry{State: EntryStateDownloading, Status: debridTypes.TorrentStatusDownloading, IsDownloading: true}, EntryPhaseProcessing},
		{"paused", Entry{State: EntryStatePausedDL}, EntryPhasePaused},
		{"completed", Entry{State: EntryStatePausedUP, Status: debridTypes.TorrentStatusDownloaded}, EntryPhaseCompleted},
		{"failed", Entry{State: EntryStateError, Status: debridTypes.TorrentStatusError}, EntryPhaseFailed},
	}
	for _, tt := range tests {
		if got := tt.entry.Phase(); got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestEntryPhaseFollowsTransitions(t *testing.T) {
	e := &Entry{State: EntryStateDownloading, Status: debridTypes.TorrentStatusQueued}
	e.MarkAsCompleted("/done")
	if got := e.Phase(); got != EntryPhaseCompleted {
		t.Fatalf("after MarkAsCompleted: got %q", got)
	}

	e = &Entry{State: EntryStateDownloading, IsDownloading: true}
	e.MarkAsError(errors.New("boom"))
	if got := e.Phase(); got != EntryPhaseFailed {
		t.Fatalf("after MarkAsError: got %q", got)
	}
}
