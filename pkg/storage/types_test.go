package storage

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
)

func TestGetTorrentFolderArrSubmittedNameFromMagnet(t *testing.T) {
	entry := &Entry{
		InfoHash: "8a19577fb5f690970ca43a57ff1011ae202244b8",
		Name:     "provider-name",
		Magnet:   "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=Example+Show+Season+01+S01+1080p+WEB-DL+x265",
	}

	got := GetTorrentFolder(config.WebDavUseArrSubmittedName, entry)
	want := "Example Show Season 01 S01 1080p WEB-DL x265"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGetTorrentFolderArrSubmittedNameSanitizesPathUnsafeNames(t *testing.T) {
	entry := &Entry{
		InfoHash: "8a19577fb5f690970ca43a57ff1011ae202244b8",
		Name:     "provider-name",
		Magnet:   "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=..%2Fbad%3Aname%3F",
	}

	got := GetTorrentFolder(config.WebDavUseArrSubmittedName, entry)
	want := "badname"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestGetTorrentFolderArrSubmittedNameFallsBackToInfoHash(t *testing.T) {
	entry := &Entry{
		InfoHash: "8a19577fb5f690970ca43a57ff1011ae202244b8",
		Name:     "provider-name",
		Magnet:   "magnet:?xt=urn:btih:8a19577fb5f690970ca43a57ff1011ae202244b8&dn=..%2F%3F",
	}

	got := GetTorrentFolder(config.WebDavUseArrSubmittedName, entry)
	want := "8a19577fb5f690970ca43a57ff1011ae202244b8"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
