package storage

import "testing"

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
