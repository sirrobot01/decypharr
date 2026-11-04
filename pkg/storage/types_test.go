package storage

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
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

func TestMarkAsErrorSanitizesMessage(t *testing.T) {
	entry := &Entry{}
	entry.MarkAsError(errors.New("bad header \x00\x1a\xff\xfeRar!\x00 end"))
	if strings.ContainsRune(entry.LastError, 0) {
		t.Fatalf("LastError contains NUL: %q", entry.LastError)
	}
	if !utf8.ValidString(entry.LastError) {
		t.Fatalf("LastError is not valid UTF-8: %q", entry.LastError)
	}
	if !strings.HasPrefix(entry.LastError, "bad header ") || !strings.HasSuffix(entry.LastError, " end") {
		t.Fatalf("readable text lost: %q", entry.LastError)
	}
}

func TestSanitizeErrorMessageTruncates(t *testing.T) {
	got := sanitizeErrorMessage(strings.Repeat("é", maxErrorMessageRunes+50))
	if n := utf8.RuneCountInString(got); n != maxErrorMessageRunes+1 {
		t.Fatalf("got %d runes, want %d", n, maxErrorMessageRunes+1)
	}
	if plain := "failed to process nzb: missing articles"; sanitizeErrorMessage(plain) != plain {
		t.Fatalf("plain message changed")
	}
}
