package vfs

import (
	"errors"
	"io/fs"
	"testing"
)

func TestStreamingFileCloseIsIdempotent(t *testing.T) {
	item := &CacheItem{info: ItemInfo{Size: 1 << 20}}
	file := NewStreamingFile(item)
	if file == nil {
		t.Fatal("NewStreamingFile returned nil")
	}
	if item.opens.Load() != 1 {
		t.Fatalf("open references=%d, want 1", item.opens.Load())
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if item.opens.Load() != 0 {
		t.Fatalf("open references=%d after close, want 0", item.opens.Load())
	}
	if _, err := file.ReadAt(make([]byte, 1), 0); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("read after close error=%v, want fs.ErrClosed", err)
	}
}

func TestStreamingFileRejectsNegativeOffset(t *testing.T) {
	item := &CacheItem{info: ItemInfo{Size: 1 << 20}}
	file := NewStreamingFile(item)
	if file == nil {
		t.Fatal("NewStreamingFile returned nil")
	}
	t.Cleanup(func() { _ = file.Close() })

	if _, err := file.ReadAt(make([]byte, 1), -1); !errors.Is(err, fs.ErrInvalid) {
		t.Fatalf("negative read error=%v, want fs.ErrInvalid", err)
	}
}
