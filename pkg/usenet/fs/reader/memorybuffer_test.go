package reader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

func TestMemoryWindowSize(t *testing.T) {
	seg750 := []SegmentMeta{{Bytes: 750 * 1024}}
	base := DefaultConfig()
	cases := []struct {
		name       string
		prefetch   int
		segs       []SegmentMeta
		wantBudget int64
	}{
		{"probe-no-readahead", 0, seg750, 8 << 20},
		{"small-readahead-floors", 8, seg750, 32 << 20}, // 3*8*750KB = 17.6MB < floor
		{"default-16MB-readahead", 22, seg750, 3 * 22 * 750 * 1024},
		{"no-segment-size-fallback", 22, nil, 3 * 22 * 750 * 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := base
			cfg.PrefetchAhead = tc.prefetch
			if budget := memoryWindowSize(cfg, tc.segs); budget != tc.wantBudget {
				t.Fatalf("memoryWindowSize = %d, want %d", budget, tc.wantBudget)
			}
		})
	}
}

func TestWindowRetentionDropsWhenOverBudget(t *testing.T) {
	const segSize = 750 * 1024
	const segCount = 16 // 12MB total against an 8MB window
	segs := make([]SegmentMeta, segCount)
	for i := range segs {
		segs[i] = SegmentMeta{
			MessageID:   fmt.Sprintf("<seg%d@test>", i),
			Number:      i + 1,
			Bytes:       segSize,
			StartOffset: int64(i) * segSize,
			EndOffset:   int64(i+1)*segSize - 1,
		}
	}
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.PrefetchAhead = 0 // probe profile: 8MB window, so drops must fire

	cache, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	data := make([]byte, segSize)
	for i := 0; i < segCount; i++ {
		for j := range data {
			data[j] = byte(i + j)
		}
		putSegment(t, cache, i, data)
		// A real stream publishes its consumed floor; without one the victim
		// picker has no read head to steer away from.
		cache.SetConsumedFloor(cache.segOffsets[i])
	}

	// The segment at the read head survives in RAM; the oldest was shed.
	got := make([]byte, segSize)
	if n, ok := cache.ReadRangeInto(segCount-1, 0, segSize, got); !ok || n != segSize {
		t.Fatalf("newest segment unreadable: n=%d ok=%v", n, ok)
	}
	dropped := 0
	for i := 0; i < segCount; i++ {
		if cache.GetState(i) == StateEmpty {
			dropped++
		}
	}
	if dropped == 0 {
		t.Fatal("expected over-budget stream to shed segments back to StateEmpty")
	}

	// And through it all, no cache directory was ever created — memory mode
	// owns no file.
	entries, err := os.ReadDir(cfg.DiskPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("memory mode created cache files: %v", entries)
	}
}

func TestMemoryEvictionFollowsPlayback(t *testing.T) {
	const segSize = 750 * 1024
	const segCount = 20
	segs := make([]SegmentMeta, segCount)
	for i := range segs {
		segs[i] = SegmentMeta{
			MessageID:   fmt.Sprintf("<seg%d@test>", i),
			Number:      i + 1,
			Bytes:       segSize,
			StartOffset: int64(i) * segSize,
			EndOffset:   int64(i+1)*segSize - 1,
		}
	}
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.PrefetchAhead = 0 // probe profile: 8MB window against ~14MB of segments

	cache, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	data := make([]byte, segSize)
	for i := 0; i < segCount; i++ {
		for j := range data {
			data[j] = byte(i + j)
		}
		putSegment(t, cache, i, data)
		cache.SetConsumedFloor(cache.segOffsets[i+1])
	}

	if drops := cache.stats.Evictions.Load(); drops == 0 {
		t.Fatal("expected the window to shed blocks once it was outgrown")
	}
	got := make([]byte, segSize)
	if n, ok := cache.ReadRangeInto(segCount-1, 0, segSize, got); !ok || n != segSize {
		t.Fatalf("segment at the read head was evicted: n=%d ok=%v", n, ok)
	}
	if cache.GetState(0) != StateEmpty {
		t.Fatalf("oldest segment survived: state=%v", cache.GetState(0))
	}
}

// putSegment stores a segment through the production write path
// (StreamWriter + Finalize).
func putSegment(t *testing.T, sc *SegmentCache, segIdx int, data []byte) {
	t.Helper()
	w := sc.StreamWriter(segIdx)
	if w == nil {
		t.Fatalf("StreamWriter(%d) returned nil", segIdx)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write segment %d: %v", segIdx, err)
	}
	w.Finalize()
}

func TestMemoryWriterAdoptsDecodedExtent(t *testing.T) {
	cache, err := NewSegmentCache(context.Background(), mkSegs(1, 64<<10), DefaultConfig(), &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	w := cache.StreamWriter(0)
	decoded := w.DecodeBuffer()[:64<<10]
	for i := range decoded {
		decoded[i] = byte(i)
	}
	want := &decoded[0]
	if _, err := w.Adopt(decoded); err != nil {
		t.Fatal(err)
	}
	w.Finalize()
	resident := cache.resident[0].Load()
	if resident == nil || &resident.data[0] != want {
		t.Fatal("decoded storage was copied instead of adopted")
	}
	if cache.buf != nil {
		t.Fatal("window retention allocated a block buffer")
	}
}

func TestDeliveryAcknowledgementReleasesCompleteSegments(t *testing.T) {
	const segSize = int64(64 << 10)
	stats := &ReaderStats{}
	cfg := DefaultConfig()
	cfg.Retention = RetentionDelivery
	cache, err := NewSegmentCache(context.Background(), mkSegs(4, segSize), cfg, stats, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	for i := range 4 {
		putSegment(t, cache, i, make([]byte, segSize))
	}
	before := cache.residentN.Load()
	// The acknowledged range starts halfway through segment 0 and ends at
	// segment 3, so only complete segments 1 and 2 may be released.
	released := cache.ReleaseCachedRange(segSize/2, 5*segSize/2)
	if released <= 0 || cache.residentN.Load() != before-released {
		t.Fatalf("resident bytes before=%d released=%d after=%d", before, released, cache.residentN.Load())
	}
	for _, idx := range []int{1, 2} {
		if state := cache.GetState(idx); state != StateEmpty {
			t.Fatalf("complete segment %d state=%v, want Empty", idx, state)
		}
	}
	for _, idx := range []int{0, 3} {
		if state := cache.GetState(idx); state != StateOnDisk {
			t.Fatalf("boundary segment %d state=%v, want OnDisk", idx, state)
		}
	}
	if stats.DeliveryReleases.Load() != 2 || stats.DeliveryBytes.Load() != released {
		t.Fatalf("delivery stats: releases=%d bytes=%d, want 2/%d",
			stats.DeliveryReleases.Load(), stats.DeliveryBytes.Load(), released)
	}
}

func TestIdleDeliveryDropsResidentsAndLatePublishes(t *testing.T) {
	const segSize = int64(64 << 10)
	cfg := DefaultConfig()
	cfg.Retention = RetentionDelivery
	cache, err := NewSegmentCache(context.Background(), mkSegs(3, segSize), cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	putSegment(t, cache, 0, make([]byte, segSize))
	cache.PinRange(0, 0)
	putSegment(t, cache, 1, make([]byte, segSize))
	cache.ReleaseIdleDelivery()
	if cache.GetState(1) != StateEmpty {
		t.Fatalf("unpinned idle segment state=%v, want Empty", cache.GetState(1))
	}
	if cache.GetState(0) != StateOnDisk {
		t.Fatalf("pinned idle segment state=%v, want OnDisk", cache.GetState(0))
	}
	cache.UnpinRange(0, 0)
	if cache.GetState(0) != StateEmpty {
		t.Fatalf("segment survived final unpin while idle: %v", cache.GetState(0))
	}

	// A speculative fetch that completes after the last handle closed must
	// not silently rebuild the idle delivery cache.
	putSegment(t, cache, 2, make([]byte, segSize))
	if cache.GetState(2) != StateEmpty || cache.resident[2].Load() != nil {
		t.Fatal("late speculative publish repopulated an idle delivery cache")
	}

	cache.ActivateDelivery()
	putSegment(t, cache, 2, make([]byte, segSize))
	if cache.GetState(2) != StateOnDisk || cache.resident[2].Load() == nil {
		t.Fatal("reactivated delivery cache rejected a useful segment")
	}
}

func TestRetentionStorageTiers(t *testing.T) {
	if DefaultConfig().Retention != RetentionWindow {
		t.Fatal("window retention must be the default")
	}
	for _, memory := range []bool{true, false} {
		name := "disk"
		if memory {
			name = "memory"
		}
		t.Run(name, func(t *testing.T) {
			const segSize = 64 << 10
			segCount := 8
			if !memory {
				segCount = 640
			}
			segs := make([]SegmentMeta, segCount)
			for i := range segs {
				segs[i] = SegmentMeta{
					MessageID:   fmt.Sprintf("<seg%d@test>", i),
					Number:      i + 1,
					Bytes:       segSize,
					StartOffset: int64(i) * segSize,
					EndOffset:   int64(i+1)*segSize - 1,
				}
			}
			cfg := DefaultConfig()
			cfg.DiskPath = t.TempDir()
			if memory {
				cfg.Retention = RetentionWindow
			} else {
				cfg.Retention = RetentionRewind
			}

			cache, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
			if err != nil {
				t.Fatalf("NewSegmentCache: %v", err)
			}
			t.Cleanup(func() { _ = cache.Close() })

			data := make([]byte, segSize)
			for i := 0; i < segCount; i++ {
				for j := range data {
					data[j] = byte(i + j)
				}
				putSegment(t, cache, i, data)
			}
			got := make([]byte, segSize)
			for i := 0; i < segCount; i++ {
				n, ok := cache.ReadRangeInto(i, 0, segSize, got)
				if !ok || n != segSize {
					t.Fatalf("ReadRangeInto(%d) = %d, %v", i, n, ok)
				}
				if got[0] != byte(i) || got[segSize-1] != byte(i+segSize-1) {
					t.Fatalf("segment %d data mismatch", i)
				}
			}

			if memory {
				// Memory mode owns no file: the DiskPath dir stays empty.
				entries, err := os.ReadDir(cfg.DiskPath)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("memory mode created cache files: %v", entries)
				}
				return
			}
			matches, err := filepath.Glob(filepath.Join(cfg.DiskPath, "cache-*", "segments.bin"))
			if err != nil || len(matches) != 1 {
				t.Fatalf("locate segments.bin: %v (%d matches)", err, len(matches))
			}
			onDisk, err := os.ReadFile(matches[0])
			if err != nil {
				t.Fatal(err)
			}
			if len(bytes.Trim(onDisk, "\x00")) == 0 {
				t.Fatal("disk mode wrote nothing to segments.bin")
			}
		})
	}
}

// mkSegs builds a contiguous segment table of segCount equal-sized segments.
func mkSegs(segCount int, segSize int64) []SegmentMeta {
	segs := make([]SegmentMeta, segCount)
	for i := range segs {
		segs[i] = SegmentMeta{
			MessageID:   fmt.Sprintf("<seg%d@test>", i),
			Number:      i + 1,
			Bytes:       segSize,
			StartOffset: int64(i) * segSize,
			EndOffset:   int64(i+1)*segSize - 1,
		}
	}
	return segs
}

// TestWaitForSegmentReportsEvicted is the unit-level guard for the memory-mode
// playback deadlock. A segment that a block drop took back to StateEmpty has
// no fetch behind it, so waiting on it waits forever. WaitForSegment must say
// so instead of parking, leaving the caller to re-fetch.
func TestWaitForSegmentReportsEvicted(t *testing.T) {
	const segSize = 750 * 1024
	segs := mkSegs(4, segSize)
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()

	cache, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	putSegment(t, cache, 1, make([]byte, segSize))
	if err := cache.WaitForSegment(context.Background(), 1); err != nil {
		t.Fatalf("cached segment should be ready: %v", err)
	}

	cache.trimResidentTo(1)

	done := make(chan error, 1)
	go func() { done <- cache.WaitForSegment(context.Background(), 1) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrSegmentEvicted) {
			t.Fatalf("want ErrSegmentEvicted, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("WaitForSegment parked on an evicted segment: the playback deadlock is back")
	}
}

// TestPinnedSegmentsSurviveRAMPressure: a segment pinned by an in-progress
// read must not be destroyed to make room for new data. Before the CanDrop
// veto the pin was advisory, so a drop could pull the bytes out from under
// the reader that had just ensured them.
func TestPinnedSegmentsSurviveRAMPressure(t *testing.T) {
	const segSize = 750 * 1024
	const segCount = 16 // 12MB against an 8MB window: drops must fire
	segs := mkSegs(segCount, segSize)
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()
	cfg.PrefetchAhead = 0 // probe profile: 8MB window

	cache, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	data := make([]byte, segSize)
	for j := range data {
		data[j] = byte(j)
	}
	putSegment(t, cache, 0, data)

	// Pin segment 0 the way a read in flight does, and publish a read head
	// past it so it is the block furthest *behind* the consumer — the first
	// victim the eviction scan would otherwise take. Then push the stream
	// well past its budget.
	cache.PinRange(0, 0)
	defer cache.UnpinRange(0, 0)
	cache.SetConsumedFloor(int64(segCount) * segSize)
	for i := 1; i < segCount; i++ {
		putSegment(t, cache, i, data)
	}

	got := make([]byte, segSize)
	if n, ok := cache.ReadRangeInto(0, 0, segSize, got); !ok || n != segSize {
		t.Fatalf("pinned segment was evicted under RAM pressure: n=%d ok=%v state=%v",
			n, ok, cache.GetState(0))
	}
	if !bytes.Equal(got, data) {
		t.Fatal("pinned segment data corrupted")
	}
}

// TestMaxPrefetchSegmentsTracksFairShare: the prefetch depth a stream is
// allowed must shrink as the shared pool is split across more streams,
// otherwise every stream downloads more than it can hold and re-fetches what
// it just dropped.
func TestMaxPrefetchSegmentsTracksFairShare(t *testing.T) {
	const segSize = 750 * 1024
	segs := mkSegs(64, segSize)
	cfg := DefaultConfig()
	cfg.DiskPath = t.TempDir()

	withTestPool(t, 64<<20)

	first, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewSegmentCache: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	alone := first.MaxPrefetchSegments()

	// Seven more streams open against the same pool.
	for range 7 {
		sc, err := NewSegmentCache(context.Background(), segs, cfg, &ReaderStats{}, zerolog.Nop())
		if err != nil {
			t.Fatalf("NewSegmentCache: %v", err)
		}
		t.Cleanup(func() { _ = sc.Close() })
	}
	shared := first.MaxPrefetchSegments()

	if shared >= alone {
		t.Fatalf("prefetch depth ignored the shrinking fair share: alone=%d shared=%d", alone, shared)
	}
	if shared < 1 {
		t.Fatalf("prefetch depth collapsed to %d: the stream can never make progress", shared)
	}
}
