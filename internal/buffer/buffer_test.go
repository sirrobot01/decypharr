package buffer

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
	"testing"
)

// newTestBuffer builds a Buffer with a small RAM budget so eviction tests
// don't need to allocate huge amounts of memory.
func newTestBuffer(t *testing.T, memorySize, totalSize int64) *Buffer {
	t.Helper()
	dir := t.TempDir()
	b, err := New(Config{
		MemorySize: memorySize,
		DiskPath:   filepath.Join(dir, "buf.bin"),
		TotalSize:  totalSize,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestBuffer_WriteReadRoundTrip(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	want := bytes.Repeat([]byte("Aa1"), 4096)
	if _, err := b.WriteAt(want, 100); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := b.ReadAt(got, 100); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("read mismatch: got %q…, want %q…", got[:16], want[:16])
	}
}

func TestBuffer_ReadAtUnwrittenReturnsErrNotPresent(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	got := make([]byte, 16)
	_, err := b.ReadAt(got, 0)
	if !errors.Is(err, ErrNotPresent) {
		t.Fatalf("expected ErrNotPresent, got %v", err)
	}
}

func TestBuffer_ReadAtPartiallyWrittenReturnsErrNotPresent(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	if _, err := b.WriteAt([]byte("hello"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// Try to read past the written region — first 5 bytes are present,
	// the rest are not, so the whole call must fail.
	got := make([]byte, 100)
	_, err := b.ReadAt(got, 0)
	if !errors.Is(err, ErrNotPresent) {
		t.Fatalf("expected ErrNotPresent, got %v", err)
	}
}

func TestBuffer_HasRangeAndPresent(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	_, _ = b.WriteAt([]byte("hello"), 0)
	_, _ = b.WriteAt([]byte("world"), 100)

	if !b.HasRange(0, 5) {
		t.Error("HasRange(0,5) should be true")
	}
	if b.HasRange(0, 10) {
		t.Error("HasRange(0,10) should be false (gap)")
	}
	if !b.HasRange(100, 5) {
		t.Error("HasRange(100,5) should be true")
	}

	got := b.Present(0, 200)
	if len(got) != 2 {
		t.Fatalf("expected 2 present ranges, got %d: %+v", len(got), got)
	}
	if got[0] != (Range{0, 5}) || got[1] != (Range{100, 5}) {
		t.Fatalf("unexpected ranges: %+v", got)
	}
}

func TestBuffer_DiscardReleasesRange(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	if _, err := b.WriteAt([]byte("hello world"), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	if !b.HasRange(0, 11) {
		t.Fatal("range should be present before discard")
	}
	if err := b.Discard(0, 11); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if b.HasRange(0, 11) {
		t.Fatal("range should be gone after discard")
	}
	got := make([]byte, 11)
	if _, err := b.ReadAt(got, 0); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("after discard, read should return ErrNotPresent, got %v", err)
	}
}

func TestBuffer_DiscardPartialBlockKeepsSurvivor(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	payload := bytes.Repeat([]byte{0xAB}, 4096)
	if _, err := b.WriteAt(payload, 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// Discard the middle 1KB, leaving the head and tail.
	if err := b.Discard(1024, 1024); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if !b.HasRange(0, 1024) {
		t.Fatal("head should survive")
	}
	if !b.HasRange(2048, 2048) {
		t.Fatal("tail should survive")
	}
	if b.HasRange(1024, 1024) {
		t.Fatal("middle should be gone")
	}
}

func TestBuffer_WritesBeyondBudgetReachDisk(t *testing.T) {
	// MemorySize = 2 blocks. The first 2 distinct blocks cache; further
	// blocks exceed the budget and write-through straight to disk rather
	// than caching+evicting. Either mechanism must persist all data and
	// read it back correctly.
	b := newTestBuffer(t, 2*blockSize, 8*blockSize)

	type write struct {
		off  int64
		data []byte
	}
	writes := []write{
		{off: 0, data: bytes.Repeat([]byte{1}, 4096)},
		{off: blockSize, data: bytes.Repeat([]byte{2}, 4096)},
		{off: 2 * blockSize, data: bytes.Repeat([]byte{3}, 4096)},
		{off: 3 * blockSize, data: bytes.Repeat([]byte{4}, 4096)},
	}
	for _, w := range writes {
		if _, err := b.WriteAt(w.data, w.off); err != nil {
			t.Fatalf("WriteAt(%d): %v", w.off, err)
		}
	}

	// Force a Sync so anything still in RAM lands on disk for verification.
	if err := b.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Read back each. The first blocks come from RAM, the over-budget ones
	// from disk via write-through. All must round-trip correctly.
	for _, w := range writes {
		got := make([]byte, len(w.data))
		if _, err := b.ReadAt(got, w.off); err != nil {
			t.Fatalf("ReadAt(%d): %v", w.off, err)
		}
		if !bytes.Equal(got, w.data) {
			t.Fatalf("read at %d: mismatch", w.off)
		}
	}

	// Two blocks fit the budget; the remaining two exceeded it and must
	// have bypassed the cache via write-through.
	stats := b.Stats()
	if stats.WritesThrough == 0 {
		t.Errorf("expected at least one write-through past the budget, got %d", stats.WritesThrough)
	}
}

func TestBuffer_NonContiguousWriteWithinBlock(t *testing.T) {
	// Writing two non-overlapping regions inside the same block triggers
	// an early flush of the first region before the second is applied.
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)

	a := bytes.Repeat([]byte{0x11}, 256)
	c := bytes.Repeat([]byte{0x33}, 256)
	if _, err := b.WriteAt(a, 0); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if _, err := b.WriteAt(c, 4096); err != nil {
		t.Fatalf("write c: %v", err)
	}

	gotA := make([]byte, 256)
	if _, err := b.ReadAt(gotA, 0); err != nil || !bytes.Equal(gotA, a) {
		t.Fatalf("read a: err=%v match=%v", err, bytes.Equal(gotA, a))
	}
	gotC := make([]byte, 256)
	if _, err := b.ReadAt(gotC, 4096); err != nil || !bytes.Equal(gotC, c) {
		t.Fatalf("read c: err=%v match=%v", err, bytes.Equal(gotC, c))
	}

	// Bytes between the two writes must NOT be present — we didn't write
	// them, and ReadAt-on-unwritten must return ErrNotPresent.
	gotGap := make([]byte, 100)
	if _, err := b.ReadAt(gotGap, 256); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("gap read: expected ErrNotPresent, got %v", err)
	}
}

func TestBuffer_WriteSpansMultipleBlocks(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	// Write that straddles three block boundaries.
	want := bytes.Repeat([]byte("XY"), blockSize+blockSize/2)
	off := int64(blockSize / 2)
	if _, err := b.WriteAt(want, off); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	got := make([]byte, len(want))
	if _, err := b.ReadAt(got, off); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("read mismatch across blocks")
	}
}

// TestBuffer_NoCorruptionUnderHeavyEviction is a regression test for a
// data race in flushBlockLocked / ReadAt: when the lock was released
// across the disk I/O syscall, concurrent writes to a block being flushed
// could corrupt the on-disk bytes (and the dirty-clear logic then forgot
// the new writes). Symptom downstream was video corruption while audio
// played fine (video tolerates lost bytes terribly; audio packets are
// independent so a lost packet is a click, not a hang).
//
// To trigger the race we need many small blocks with eviction pressure
// AND concurrent writers/readers hitting the same offsets.
func TestBuffer_NoCorruptionUnderHeavyEviction(t *testing.T) {
	// MemorySize = 2 blocks; 50 blocks of data → constant eviction.
	b := newTestBuffer(t, 2*blockSize, 64*blockSize)

	const numBlocks = 50
	payloads := make([][]byte, numBlocks)
	for i := range payloads {
		// Distinct, recognizable payloads: byte value = block index + 1.
		payloads[i] = bytes.Repeat([]byte{byte(i + 1)}, blockSize)
	}

	// Concurrent writers race to fill all blocks.
	var wg sync.WaitGroup
	for i := 0; i < numBlocks; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := b.WriteAt(payloads[i], int64(i)*blockSize); err != nil {
				t.Errorf("WriteAt %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	if err := b.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Every block must round-trip exactly. Even one wrong byte is the
	// bug — report the first divergence per failing block.
	for i := 0; i < numBlocks; i++ {
		got := make([]byte, blockSize)
		if _, err := b.ReadAt(got, int64(i)*blockSize); err != nil {
			t.Errorf("ReadAt %d: %v", i, err)
			continue
		}
		if !bytes.Equal(got, payloads[i]) {
			for j := 0; j < blockSize; j++ {
				if got[j] != payloads[i][j] {
					t.Errorf("block %d corrupted at byte %d: got %#x want %#x", i, j, got[j], payloads[i][j])
					break
				}
			}
		}
	}
}

func TestBuffer_ConcurrentWritersAndReaders(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	const segs = 8
	const segSize = 4096

	// Producers: write distinct payloads at well-spaced offsets.
	var wg sync.WaitGroup
	for i := 0; i < segs; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := bytes.Repeat([]byte{byte(i + 1)}, segSize)
			if _, err := b.WriteAt(data, int64(i)*segSize); err != nil {
				t.Errorf("WriteAt seg %d: %v", i, err)
			}
		}()
	}
	wg.Wait()

	// Consumers: each segment must round-trip its own payload.
	for i := 0; i < segs; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			want := bytes.Repeat([]byte{byte(i + 1)}, segSize)
			got := make([]byte, segSize)
			if _, err := b.ReadAt(got, int64(i)*segSize); err != nil {
				t.Errorf("ReadAt seg %d: %v", i, err)
				return
			}
			if !bytes.Equal(got, want) {
				t.Errorf("seg %d mismatch", i)
			}
		}()
	}
	wg.Wait()
}

// TestBuffer_ConcurrentReadDuringWriteThrough stresses the rmu/mu split:
// a writer fills many segments sequentially through a tiny RAM budget (so
// most go down the write-through path), while readers concurrently read
// already-completed segments. This exercises ranges.insert (writer, rmu)
// racing ranges.present (reader, rmu) and concurrent block-map RLocks —
// the paths the split decoupled. Run with -race. Respects the caller
// contract: a reader only touches a segment after its single write has
// fully completed, so no read overlaps a write of the same range.
func TestBuffer_ConcurrentReadDuringWriteThrough(t *testing.T) {
	// 2-block RAM budget, 48 distinct blocks → ~46 write-throughs.
	const segs = 48
	b := newTestBuffer(t, 2*blockSize, segs*blockSize)

	payload := func(i int) []byte { return bytes.Repeat([]byte{byte(i*7 + 1)}, 64*1024) }

	var done atomic.Int64 // count of fully-written segments (publish barrier)
	done.Store(0)

	var wg sync.WaitGroup

	// Single writer fills segments sequentially.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < segs; i++ {
			if _, err := b.WriteAt(payload(i), int64(i)*blockSize); err != nil {
				t.Errorf("WriteAt seg %d: %v", i, err)
				return
			}
			done.Add(1)
		}
	}()

	// Readers verify random already-completed segments while writes continue.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			rng := mrandNew(int64(seed))
			for iters := 0; iters < 2000; iters++ {
				n := done.Load()
				if n == 0 {
					continue
				}
				i := int(rng() % n)
				got := make([]byte, 64*1024)
				if _, err := b.ReadAt(got, int64(i)*blockSize); err != nil {
					t.Errorf("ReadAt seg %d: %v", i, err)
					return
				}
				if !bytes.Equal(got, payload(i)) {
					t.Errorf("seg %d mismatch during concurrent write-through", i)
					return
				}
			}
		}(r + 1)
	}
	wg.Wait()
}

// mrandNew returns a tiny deterministic LCG so the stress test doesn't pull
// in math/rand global state or extra imports.
func mrandNew(seed int64) func() int64 {
	state := uint64(seed)*2862933555777941757 + 3037000493
	return func() int64 {
		state = state*6364136223846793005 + 1442695040888963407
		return int64(state >> 1)
	}
}

func TestBuffer_CloseRemovesTempFile(t *testing.T) {
	b, err := New(Config{MemorySize: blockSize, TotalSize: blockSize}) // no DiskPath → temp
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	path := b.file.Name()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("temp file should exist before Close: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("temp file should be removed after Close, stat err=%v", err)
	}
}

func TestBuffer_ClosedRejectsOps(t *testing.T) {
	b := newTestBuffer(t, blockSize, blockSize)
	_ = b.Close()

	if _, err := b.WriteAt([]byte("x"), 0); !errors.Is(err, ErrClosed) {
		t.Errorf("WriteAt after Close: got %v, want ErrClosed", err)
	}
	if _, err := b.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrClosed) {
		t.Errorf("ReadAt after Close: got %v, want ErrClosed", err)
	}
	if err := b.Discard(0, 1); !errors.Is(err, ErrClosed) {
		t.Errorf("Discard after Close: got %v, want ErrClosed", err)
	}
	if err := b.Sync(); !errors.Is(err, ErrClosed) {
		t.Errorf("Sync after Close: got %v, want ErrClosed", err)
	}
}

func TestBuffer_InitialRangesSeedsTracker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buf.bin")
	// First buffer: write some data, close.
	{
		b, err := New(Config{MemorySize: blockSize, DiskPath: path, TotalSize: 8 * blockSize})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		want := bytes.Repeat([]byte{0x77}, 4096)
		if _, err := b.WriteAt(want, 100); err != nil {
			t.Fatalf("WriteAt: %v", err)
		}
		if err := b.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		_ = b.Close()
	}
	// Second buffer: reopen with InitialRanges describing the existing
	// on-disk extent. Reads of seeded bytes must succeed; reads outside
	// the seed must still return ErrNotPresent.
	b2, err := New(Config{
		MemorySize:    blockSize,
		DiskPath:      path,
		TotalSize:     8 * blockSize,
		InitialRanges: []Range{{Off: 100, Size: 4096}},
	})
	if err != nil {
		t.Fatalf("reopen New: %v", err)
	}
	t.Cleanup(func() { _ = b2.Close() })

	got := make([]byte, 4096)
	if _, err := b2.ReadAt(got, 100); err != nil {
		t.Fatalf("ReadAt seeded range: %v", err)
	}
	if got[0] != 0x77 || got[4095] != 0x77 {
		t.Fatalf("seeded data mismatch: first=%x last=%x", got[0], got[4095])
	}
	// Outside the seed must still be ErrNotPresent.
	if _, err := b2.ReadAt(make([]byte, 100), 5000); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("unseeded read: want ErrNotPresent, got %v", err)
	}
}

func TestBuffer_DiscardClearsBlockDirtyRange(t *testing.T) {
	b := newTestBuffer(t, 4*blockSize, 8*blockSize)
	if _, err := b.WriteAt(bytes.Repeat([]byte{0xCD}, 4096), 0); err != nil {
		t.Fatalf("WriteAt: %v", err)
	}
	// Discard the whole written range. The block's dirty state should
	// be cleared so a subsequent Sync doesn't try to write to a hole.
	if err := b.Discard(0, 4096); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	if err := b.Sync(); err != nil {
		t.Fatalf("Sync after discard: %v", err)
	}
}

// -------- range set tests (internal) --------

func TestRangeSet_InsertMergesAdjacent(t *testing.T) {
	rs := newRangeSet()
	rs.insert(0, 100)
	rs.insert(100, 100)
	if len(rs.rs) != 1 {
		t.Fatalf("expected merged into 1 range, got %+v", rs.rs)
	}
	if rs.rs[0] != (extent{0, 200}) {
		t.Fatalf("unexpected: %+v", rs.rs)
	}
}

func TestRangeSet_InsertMergesOverlapping(t *testing.T) {
	rs := newRangeSet()
	rs.insert(0, 100)
	rs.insert(50, 100)
	if len(rs.rs) != 1 {
		t.Fatalf("expected 1 range, got %+v", rs.rs)
	}
	if rs.rs[0] != (extent{0, 150}) {
		t.Fatalf("unexpected: %+v", rs.rs)
	}
}

func TestRangeSet_RemoveSplits(t *testing.T) {
	rs := newRangeSet()
	rs.insert(0, 1000)
	rs.remove(300, 200)
	if len(rs.rs) != 2 {
		t.Fatalf("expected 2 ranges, got %+v", rs.rs)
	}
	if rs.rs[0] != (extent{0, 300}) || rs.rs[1] != (extent{500, 1000}) {
		t.Fatalf("unexpected: %+v", rs.rs)
	}
}

func TestRangeSet_RemoveSpansMultiple(t *testing.T) {
	rs := newRangeSet()
	rs.insert(0, 100)
	rs.insert(200, 100)
	rs.insert(400, 100)
	// Remove a big swath covering middle range entirely and partials of
	// the bracketing ones.
	rs.remove(50, 400)
	// Expect: [0,50) and [450,500)
	if len(rs.rs) != 2 {
		t.Fatalf("expected 2 ranges, got %+v", rs.rs)
	}
	if rs.rs[0] != (extent{0, 50}) || rs.rs[1] != (extent{450, 500}) {
		t.Fatalf("unexpected: %+v", rs.rs)
	}
}

// TestBuffer_PromotionFires verifies the counter-gated promote path
// actually installs a block in RAM when a single block is read repeatedly
// from disk — the workload pattern that "smart Phase 2" exists to serve.
func TestBuffer_PromotionFires(t *testing.T) {
	const fileSize = 4 * blockSize
	b := newTestBuffer(t, 2*blockSize, fileSize)

	// Fill 4 blocks. The 2-block budget means blocks 0 and 1 cache and
	// blocks 2 and 3 go straight to disk via write-through (never RAM-
	// resident). So reads of block 2 hit disk and exercise promotion.
	payload := bytes.Repeat([]byte{0xAB}, blockSize)
	for i := 0; i < 4; i++ {
		if _, err := b.WriteAt(payload, int64(i)*blockSize); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}

	// First read of block 2: counter goes 1, no enqueue.
	// Second read: counter goes 2, enqueue → worker installs.
	got := make([]byte, 4096)
	for i := 0; i < 6; i++ {
		if _, err := b.ReadAt(got, 2*blockSize); err != nil {
			t.Fatalf("ReadAt iter %d: %v", i, err)
		}
		if got[0] != 0xAB {
			t.Fatalf("iter %d: wrong payload", i)
		}
	}
	// Wait briefly for the worker to drain (it's async).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.Stats().Promotions > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got := b.Stats().Promotions; got == 0 {
		t.Fatalf("expected at least 1 promotion after repeated disk reads, got 0")
	}
}

func TestRangeSet_PresentAndAnyPresent(t *testing.T) {
	rs := newRangeSet()
	rs.insert(100, 100)
	if !rs.present(100, 100) {
		t.Error("present(100,100) should be true")
	}
	if rs.present(50, 100) {
		t.Error("present(50,100) should be false")
	}
	if !rs.anyPresent(50, 100) {
		t.Error("anyPresent(50,100) should be true (overlap)")
	}
	if rs.anyPresent(0, 50) {
		t.Error("anyPresent(0,50) should be false (no overlap)")
	}
}
