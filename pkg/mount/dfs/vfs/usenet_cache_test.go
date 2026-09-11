package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
	appconfig "github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/manager"
	dfsconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

type persistedNZBBackend struct {
	entry    *storage.Entry
	opens    atomic.Int64
	tracks   atomic.Int64
	untracks atomic.Int64
	active   atomic.Int64
}

func (b *persistedNZBBackend) GetEntryByName(string, string) (*storage.Entry, error) {
	return b.entry, nil
}

func (b *persistedNZBBackend) TrackStream(*storage.Entry, string, string) string {
	b.tracks.Add(1)
	b.active.Add(1)
	return "persisted-nzb"
}

func (b *persistedNZBBackend) UntrackStream(string) {
	b.untracks.Add(1)
	b.active.Add(-1)
}

func (b *persistedNZBBackend) OpenStreamUntrackedForCache(context.Context, *storage.Entry, string, int64) (manager.StreamReader, error) {
	b.opens.Add(1)
	return nil, errors.New("persisted DFS cache unexpectedly opened an upstream stream")
}

func TestPersistedNZBUsesDFSCacheAndTracksStream(t *testing.T) {
	const (
		entryName = "Cached.Movie"
		filename  = "movie.mkv"
		fileSize  = int64(512 << 10)
	)

	cacheDir := t.TempDir()
	entryDir := filepath.Join(cacheDir, entryName)
	if err := os.MkdirAll(entryDir, 0o755); err != nil {
		t.Fatal(err)
	}

	want := make([]byte, fileSize)
	for i := range want {
		want[i] = byte(i*17 + 3)
	}
	if err := os.WriteFile(filepath.Join(entryDir, filename), want, 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	metadata, err := json.Marshal(ItemInfo{
		Size:    fileSize,
		Rs:      ranges.Ranges{{Pos: 0, Size: fileSize}},
		ModTime: now,
		ATime:   now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(entryDir, filename+".json"), metadata, 0o644); err != nil {
		t.Fatal(err)
	}

	entry := &storage.Entry{
		Name:     entryName,
		InfoHash: "cached-nzb",
		Protocol: appconfig.ProtocolNZB,
		Files: map[string]*storage.File{
			filename: {Name: filename, Size: fileSize},
		},
	}
	backend := &persistedNZBBackend{entry: entry}
	cfg := dfsconfig.DefaultFuseConfig()
	cfg.CacheDir = cacheDir
	cfg.CacheCleanupInterval = time.Hour

	cache, err := NewCache(context.Background(), backend, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cache.logger = zerolog.Nop()
	t.Cleanup(func() { _ = cache.Close() })

	item, err := cache.GetItem(entryName, filename, fileSize)
	if err != nil {
		t.Fatal(err)
	}
	if item.buf == nil || item.downloaders.Load() == nil {
		t.Fatal("NZB did not construct the persistent DFS cache path")
	}

	file := NewStreamingFile(item)
	if file == nil {
		t.Fatal("NewStreamingFile returned nil")
	}
	t.Cleanup(func() { _ = file.Close() })

	const off = int64(96 << 10)
	got := make([]byte, 64<<10)
	n, err := file.ReadAtContext(context.Background(), got, off)
	if err != nil {
		t.Fatal(err)
	}
	if n != len(got) || !bytes.Equal(got, want[off:off+int64(len(got))]) {
		t.Fatal("read did not return the persisted DFS bytes")
	}
	if backend.opens.Load() != 0 {
		t.Fatalf("upstream opens=%d, want 0 for a persisted hit", backend.opens.Load())
	}
	if cache.cacheHits.Load() != 1 || cache.cacheMisses.Load() != 0 {
		t.Fatalf("cache accounting: hits=%d misses=%d, want 1/0", cache.cacheHits.Load(), cache.cacheMisses.Load())
	}
	if backend.tracks.Load() != 1 || backend.active.Load() != 1 {
		t.Fatalf("tracking after read: tracks=%d active=%d, want 1/1", backend.tracks.Load(), backend.active.Load())
	}

	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if backend.untracks.Load() != 1 || backend.active.Load() != 0 {
		t.Fatalf("tracking after close: untracks=%d active=%d, want 1/0", backend.untracks.Load(), backend.active.Load())
	}
}
