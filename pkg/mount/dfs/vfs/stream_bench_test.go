package vfs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/logger"
	fuseconfig "github.com/sirrobot01/decypharr/pkg/mount/dfs/config"
	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

// newBenchItem builds a CacheItem wired the way newItem does, but without a
// Cache/Manager behind it: the buffer is real, the metadata writer loop is
// real, and the Downloaders coordinator is real but pre-tracked and never
// asked to download (callers pre-fill the item or write directly).
func newBenchItem(tb testing.TB, fileSize int64) (*CacheItem, *Downloaders) {
	tb.Helper()
	dir := tb.TempDir()

	pool := buffer.NewPool(buffer.PoolConfig{Name: "bench"})
	tb.Cleanup(func() { _ = pool.Close() })

	buf, err := pool.NewBuffer(buffer.Config{
		MemorySize: 32 << 20,
		DiskPath:   filepath.Join(dir, "data.bin"),
		TotalSize:  fileSize,
	})
	if err != nil {
		tb.Fatal(err)
	}

	cache := &Cache{
		config: &fuseconfig.FuseConfig{},
		logger: zerolog.Nop(),
	}
	log := logger.NewRateLimitedLogger(logger.WithLogger(zerolog.Nop()))
	item := &CacheItem{
		cache:    cache,
		key:      "bench",
		filename: "bench.bin",
		buf:      buf,
		metaPath: filepath.Join(dir, "data.json"),
		info:     ItemInfo{Size: fileSize},
		logger:   log.Rate("bench"),
	}

	dls := NewDownloaders(context.Background(), nil, item, &fuseconfig.FuseConfig{})
	// Pre-mark the stream as tracked so the read path never dereferences the
	// nil manager.
	dls.mu.Lock()
	dls.streamID = "bench"
	dls.streamTracked.Store(true)
	dls.mu.Unlock()
	item.downloaders.Store(dls)

	item.startMetaWriter()
	tb.Cleanup(func() {
		// Drop the fake stream ID first — Close would otherwise try to
		// unregister it with the nil manager.
		dls.mu.Lock()
		dls.streamID = ""
		dls.streamTracked.Store(false)
		dls.mu.Unlock()
		dls.Close(nil)
		item.stopMetaWriter()
		_ = buf.Close()
	})
	return item, dls
}

// prefillItem writes the whole file through WriteAtNoOverwrite so info.Rs and
// the buffer agree that everything is present.
func prefillItem(tb testing.TB, item *CacheItem, fileSize int64) {
	tb.Helper()
	chunk := make([]byte, 1<<20)
	for off := int64(0); off < fileSize; off += int64(len(chunk)) {
		n := int64(len(chunk))
		if off+n > fileSize {
			n = fileSize - off
		}
		if _, _, err := item.WriteAtNoOverwrite(chunk[:n], off); err != nil {
			tb.Fatal(err)
		}
	}
}

// BenchmarkReadAtContextWarm measures the full per-read overhead of the FUSE
// read path (stats, coordinator locks, range walks, buffer read) when all
// data is cached — the dominant case during playback with healthy readahead.
func BenchmarkReadAtContextWarm(b *testing.B) {
	const fileSize = int64(64 << 20)
	item, _ := newBenchItem(b, fileSize)
	prefillItem(b, item, fileSize)

	const readSize = 128 << 10
	p := make([]byte, readSize)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := (int64(i) * readSize) % (fileSize - readSize)
		if _, err := item.ReadAtContext(ctx, p, off); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReadAtContextWarmParallel is the same with concurrent readers —
// what go-fuse actually does with kernel readahead in flight.
func BenchmarkReadAtContextWarmParallel(b *testing.B) {
	const fileSize = int64(64 << 20)
	item, _ := newBenchItem(b, fileSize)
	prefillItem(b, item, fileSize)

	const readSize = 128 << 10
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		p := make([]byte, readSize)
		var i int64
		for pb.Next() {
			off := (i * readSize) % (fileSize - readSize)
			i++
			if _, err := item.ReadAtContext(ctx, p, off); err != nil {
				b.Error(err)
				return
			}
		}
	})
}

// BenchmarkCacheWriterWrite measures the per-write cost of the download path
// at the session's delivery granularity (32KB today): WriteAtNoOverwrite
// (range walk + buffer write + metadata dirty signal) plus the downloader
// bookkeeping in cacheWriter.Write. The metadata writer loop runs for real,
// so its per-signal flush cost shows up as syscall pressure.
func BenchmarkCacheWriterWrite(b *testing.B) {
	const writeSize = 32 << 10
	fileSize := int64(b.N)*writeSize + writeSize
	item, dls := newBenchItem(b, fileSize)

	dl := &downloader{
		dls:              dls,
		baseChunkSize:    4 << 20,
		currentChunkSize: 4 << 20,
	}
	w := &cacheWriter{dl: dl, item: item}
	chunk := make([]byte, writeSize)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := w.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkKickWaiters measures the waiter-wakeup scan with a typical number
// of parked readahead goroutines (12) whose ranges are not yet satisfied.
func BenchmarkKickWaiters(b *testing.B) {
	const fileSize = int64(64 << 20)
	item, dls := newBenchItem(b, fileSize)
	// Some data present so HasRange does real tree work.
	prefill := make([]byte, 8<<20)
	if _, _, err := item.WriteAtNoOverwrite(prefill, 0); err != nil {
		b.Fatal(err)
	}

	errCh := make(chan error, 16)
	dls.mu.Lock()
	for i := 0; i < 12; i++ {
		r := ranges.Range{Pos: 16<<20 + int64(i)*(128<<10), Size: 128 << 10}
		dls.waiters = append(dls.waiters, waiter{r: r, errChan: errCh})
	}
	dls.waiterCount.Store(12)
	// A live downloader already covers the missing region, so kickWaiters
	// scans and re-parks the waiters (the steady-state shape while a download
	// is in flight) instead of spawning a real downloader against the nil
	// manager.
	fakeCtx, fakeCancel := context.WithCancel(context.Background())
	b.Cleanup(fakeCancel)
	dls.dls = append(dls.dls, &downloader{
		dls:       dls,
		quit:      make(chan struct{}),
		kick:      make(chan struct{}, 1),
		ctx:       fakeCtx,
		cancel:    fakeCancel,
		start:     0,
		offset:    16 << 20,
		maxOffset: fileSize,
	})
	dls.mu.Unlock()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dls.kickWaiters()
	}
}
