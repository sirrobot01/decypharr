package vfs

import (
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/buffer"
	fuseconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

// newTestBuffer returns a disk-backed buffer over a fresh temp file.
func newTestBuffer(tb testing.TB, fileSize int64) *buffer.Buffer {
	tb.Helper()
	pool := buffer.NewPool(buffer.PoolConfig{Name: "test"})
	buf, err := pool.NewBuffer(buffer.Config{
		DiskPath:  filepath.Join(tb.TempDir(), "data.bin"),
		TotalSize: fileSize,
	})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = pool.Close() })
	return buf
}

// newTestItem builds a CacheItem over a real buffer with cached already
// written, so range queries answer from the same tracker production uses.
func newTestItem(tb testing.TB, fileSize int64, cached ...ranges.Range) *CacheItem {
	tb.Helper()
	buf := newTestBuffer(tb, fileSize)
	for _, r := range cached {
		markCached(tb, buf, r)
	}
	return &CacheItem{buf: buf, info: ItemInfo{Size: fileSize}}
}

// markCached writes r so the buffer reports it present.
func markCached(tb testing.TB, buf *buffer.Buffer, r ranges.Range) {
	tb.Helper()
	const chunk = 1 << 20
	block := make([]byte, min(r.Size, chunk))
	for off := r.Pos; off < r.End(); {
		n := min(int64(len(block)), r.End()-off)
		if _, err := buf.WriteAt(block[:n], off); err != nil {
			tb.Fatal(err)
		}
		off += n
	}
}

// TestSelfCachingItemLifecycle covers the item shape used for backends that
// cache for themselves: no buffer, no downloaders, no metadata writer. Every
// lifecycle call must tolerate that rather than dereference a nil buffer.
func TestSelfCachingItemLifecycle(t *testing.T) {
	item := &CacheItem{
		cache: &Cache{config: &fuseconfig.FuseConfig{}, logger: zerolog.Nop()},
		key:   "entry/file.mkv",
		info:  ItemInfo{Size: 1 << 20},
	}

	item.touch()
	item.markMetadataDirty()
	item.flushMetadata(true)
	item.StopDownloaders()

	if !item.Open() {
		t.Fatal("Open on a fresh item must succeed")
	}
	item.Release()

	if err := item.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := item.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
