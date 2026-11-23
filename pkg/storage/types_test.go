package storage

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

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

func TestMarkAsErrorStoresCleanText(t *testing.T) {
	e := &Entry{}
	e.MarkAsError(errors.New("no segments built for file a\x00b\x01c\xff" + strings.Repeat("x", 2*maxErrorMessageRunes)))
	if !utf8.ValidString(e.LastError) || strings.ContainsFunc(e.LastError, unicode.IsControl) {
		t.Fatalf("LastError has invalid or control characters: %q", e.LastError[:64])
	}
	if !strings.HasPrefix(e.LastError, "no segments built for file a b c\uFFFD") {
		t.Fatalf("LastError = %q", e.LastError[:64])
	}
	if n := utf8.RuneCountInString(e.LastError); n > maxErrorMessageRunes+3 {
		t.Fatalf("LastError has %d runes, want at most %d", n, maxErrorMessageRunes+3)
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
