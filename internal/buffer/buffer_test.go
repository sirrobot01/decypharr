package buffer

import (
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fillPattern writes a deterministic, offset-addressed byte pattern into p.
// Any range read back from the buffer can be verified against the absolute
// offset it was written at, regardless of which path (RAM, write-through,
// eviction-flush, reopen) served it.
func fillPattern(p []byte, off int64) {
	for i := range p {
		v := off + int64(i)
		p[i] = byte(v ^ (v >> 8) ^ (v >> 17))
	}
}

// verifyPattern returns an error on the first byte that doesn't match the
// pattern fillPattern wrote at off. Safe to call from any goroutine.
func verifyPattern(p []byte, off int64) error {
	for i := range p {
		v := off + int64(i)
		want := byte(v ^ (v >> 8) ^ (v >> 17))
		if p[i] != want {
			return fmt.Errorf("byte mismatch at off %d (+%d): got %#x want %#x", off, i, p[i], want)
		}
	}
	return nil
}

func checkPattern(t *testing.T, p []byte, off int64) {
	t.Helper()
	if err := verifyPattern(p, off); err != nil {
		t.Fatal(err)
	}
}

func newTestPool(t testing.TB, cfg PoolConfig) *Pool {
	t.Helper()
	if cfg.Name == "" {
		cfg.Name = "test"
	}
	p := NewPool(cfg)
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func newTestBuffer(t testing.TB, p *Pool, cfg Config) *Buffer {
	t.Helper()
	if cfg.DiskPath == "" {
		cfg.DiskPath = filepath.Join(t.TempDir(), "buf.bin")
	}
	b, err := p.NewBuffer(cfg)
	if err != nil {
		t.Fatalf("NewBuffer: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestWriteReadRoundtrip(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 8 << 20})

	// Unaligned writes straddling block boundaries.
	writes := []struct {
		off  int64
		size int
	}{
		{0, 128 << 10},
		{128 << 10, 1 << 20},               // crosses the 1MB boundary
		{(1 << 20) + (128 << 10), 3 << 20}, // multiple blocks
		{5<<20 - 7, 4096},                  // odd offset
	}
	for _, w := range writes {
		buf := make([]byte, w.size)
		fillPattern(buf, w.off)
		if n, err := b.WriteAt(buf, w.off); err != nil || n != w.size {
			t.Fatalf("WriteAt(%d, %d): n=%d err=%v", w.off, w.size, n, err)
		}
	}
	for _, w := range writes {
		buf := make([]byte, w.size)
		if n, err := b.ReadAt(buf, w.off); err != nil || n != w.size {
			t.Fatalf("ReadAt(%d, %d): n=%d err=%v", w.off, w.size, n, err)
		}
		checkPattern(t, buf, w.off)
	}
	// A read spanning two separate writes that happen to be contiguous.
	span := make([]byte, 2<<20)
	if _, err := b.ReadAt(span, 0); err != nil {
		t.Fatalf("spanning read: %v", err)
	}
	checkPattern(t, span, 0)
}

func TestReadNotPresent(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 4 << 20})

	buf := make([]byte, 4096)
	if _, err := b.ReadAt(buf, 0); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("read of unwritten range: err=%v, want ErrNotPresent", err)
	}

	data := make([]byte, 64<<10)
	fillPattern(data, 1<<20)
	if _, err := b.WriteAt(data, 1<<20); err != nil {
		t.Fatal(err)
	}
	// Read overlapping written and unwritten bytes must fail wholesale.
	if _, err := b.ReadAt(make([]byte, 128<<10), 1<<20); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("read past written range: err=%v, want ErrNotPresent", err)
	}
	// Exact range still fine.
	got := make([]byte, 64<<10)
	if _, err := b.ReadAt(got, 1<<20); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 1<<20)
}

// TestWriteThroughVisibility drives the buffer past its RAM ceiling so later
// writes take the write-through path, and asserts the WriteAt→ReadAt
// visibility contract holds on both paths: as soon as WriteAt returns, the
// exact bytes are readable.
func TestWriteThroughVisibility(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{MemorySize: blockSize, TotalSize: 16 << 20})

	const chunk = 256 << 10
	for off := int64(0); off < 8<<20; off += chunk {
		buf := make([]byte, chunk)
		fillPattern(buf, off)
		if _, err := b.WriteAt(buf, off); err != nil {
			t.Fatalf("WriteAt(%d): %v", off, err)
		}
		got := make([]byte, chunk)
		if _, err := b.ReadAt(got, off); err != nil {
			t.Fatalf("ReadAt(%d) right after write: %v", off, err)
		}
		checkPattern(t, got, off)
	}
	if b.Stats().WritesThrough == 0 {
		t.Fatalf("expected write-through path to fire with MemorySize=1 block; stats=%+v", b.Stats())
	}
	// Whole-file verification after the fact (mix of RAM and disk serves).
	whole := make([]byte, 8<<20)
	if _, err := b.ReadAt(whole, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, whole, 0)
}

// TestSyncFlushesDirtyBlocks exercises flushBlockLocked via Sync and verifies
// dirty RAM-resident bytes survive on disk (read back after dropping to a
// fresh buffer over the same file).
func TestSyncFlushesDirtyBlocks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.bin")
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: path, TotalSize: 8 << 20})

	data := make([]byte, 3<<20)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	if b.Stats().Flushes == 0 {
		t.Fatal("expected Sync to flush dirty blocks")
	}
	present := b.Present(0, 8<<20)
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	seed := make([]Range, 0, len(present))
	seed = append(seed, present...)
	b2 := newTestBuffer(t, p, Config{DiskPath: path, TotalSize: 8 << 20, InitialRanges: seed})
	got := make([]byte, 3<<20)
	if _, err := b2.ReadAt(got, 0); err != nil {
		t.Fatalf("reopen read: %v", err)
	}
	checkPattern(t, got, 0)
}

func TestDiscardMakesRangeNotPresent(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 8 << 20})

	data := make([]byte, 6<<20)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	// Discard an unaligned middle range.
	dOff, dLen := int64(1<<20+4096), int64(2<<20)
	if err := b.Discard(dOff, dLen); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadAt(make([]byte, 4096), dOff); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("read of discarded range: err=%v, want ErrNotPresent", err)
	}
	// Edges survive.
	head := make([]byte, int(dOff))
	if _, err := b.ReadAt(head, 0); err != nil {
		t.Fatalf("read before hole: %v", err)
	}
	checkPattern(t, head, 0)
	tailOff := dOff + dLen
	tail := make([]byte, int(6<<20-tailOff))
	if _, err := b.ReadAt(tail, tailOff); err != nil {
		t.Fatalf("read after hole: %v", err)
	}
	checkPattern(t, tail, tailOff)
	// Rewrite the hole and read it back.
	re := make([]byte, dLen)
	fillPattern(re, dOff)
	if _, err := b.WriteAt(re, dOff); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, dLen)
	if _, err := b.ReadAt(got, dOff); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, dOff)
}

func TestClosedErrors(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 1 << 20})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteAt([]byte("x"), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteAt after close: %v", err)
	}
	if _, err := b.ReadAt(make([]byte, 1), 0); !errors.Is(err, ErrClosed) {
		t.Fatalf("ReadAt after close: %v", err)
	}
	if err := b.Discard(0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Discard after close: %v", err)
	}
	if err := b.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("second Close: %v", err)
	}
}

// TestConcurrentStreamWorkload is the race-detector workhorse: several
// writers stream disjoint regions while readers verify completed regions and
// a discarder reclaims regions readers are done with. It honors the caller
// contract (no concurrent read/write/discard of the same byte range) the way
// real callers do — a range is handed to readers only after its write
// completed, and discarded only after its read completed.
func TestConcurrentStreamWorkload(t *testing.T) {
	const (
		regions    = 48
		regionSize = 1 << 20
		writers    = 6
	)
	p := newTestPool(t, PoolConfig{MemoryBudget: 8 << 20})
	b := newTestBuffer(t, p, Config{MemorySize: 4 << 20, TotalSize: regions * regionSize})

	work := make(chan int64, regions)
	for i := 0; i < regions; i++ {
		work <- int64(i) * regionSize
	}
	close(work)
	written := make(chan int64, regions)
	done := make(chan int64, regions)

	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, regionSize)
			for off := range work {
				fillPattern(buf, off)
				// Write in uneven slabs to hit partial-block paths.
				for cur := 0; cur < regionSize; {
					n := 200<<10 + (cur % 4096)
					if cur+n > regionSize {
						n = regionSize - cur
					}
					if _, err := b.WriteAt(buf[cur:cur+n], off+int64(cur)); err != nil {
						t.Errorf("WriteAt(%d): %v", off+int64(cur), err)
						return
					}
					cur += n
				}
				written <- off
			}
		}()
	}
	// Readers verify each completed region exactly once.
	var rg sync.WaitGroup
	for r := 0; r < 4; r++ {
		rg.Add(1)
		go func() {
			defer rg.Done()
			buf := make([]byte, regionSize)
			for off := range written {
				if _, err := b.ReadAt(buf, off); err != nil {
					t.Errorf("ReadAt(%d): %v", off, err)
					return
				}
				if err := verifyPattern(buf, off); err != nil {
					t.Error(err)
					return
				}
				done <- off
			}
		}()
	}
	// Discarder reclaims fully-consumed regions concurrently with the rest.
	var dg sync.WaitGroup
	dg.Add(1)
	go func() {
		defer dg.Done()
		for off := range done {
			if err := b.Discard(off, regionSize); err != nil && !errors.Is(err, ErrClosed) {
				t.Errorf("Discard(%d): %v", off, err)
				return
			}
		}
	}()

	wg.Wait()
	close(written)
	rg.Wait()
	close(done)
	dg.Wait()
}

// TestPoolDiskBackstopPunchesBehindHead verifies the pool's DiskLimit
// backstop: a stream that advances its read head past the limit gets its
// tail punched (OnEvict fired), keeping the pool near its disk budget.
func TestPoolDiskBackstopPunchesBehindHead(t *testing.T) {
	var (
		evictMu sync.Mutex
		evicted []Range
	)
	p := newTestPool(t, PoolConfig{DiskLimit: 8 << 20, BackWindow: 2 << 20})
	b := newTestBuffer(t, p, Config{
		MemorySize: blockSize,
		TotalSize:  64 << 20,
		OnEvict: func(off, length int64) {
			evictMu.Lock()
			evicted = append(evicted, Range{Off: off, Size: length})
			evictMu.Unlock()
		},
	})

	chunk := make([]byte, 1<<20)
	for off := int64(0); off < 32<<20; off += 1 << 20 {
		fillPattern(chunk, off)
		if _, err := b.WriteAt(chunk, off); err != nil {
			t.Fatal(err)
		}
		b.SetReadHead(off + 1<<20)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if p.Stats().DiskInUse <= 9<<20 { // limit + 1MB slack for in-flight accounting
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("disk backstop never reclaimed: pool=%+v", p.Stats())
		}
		time.Sleep(10 * time.Millisecond)
	}
	evictMu.Lock()
	n := len(evicted)
	evictMu.Unlock()
	if n == 0 {
		t.Fatal("expected OnEvict callbacks from the disk backstop")
	}
	// Data inside the protected window (read head - BackWindow .. head) survives.
	head := int64(32 << 20)
	got := make([]byte, 1<<20)
	if _, err := b.ReadAt(got, head-1<<20); err != nil {
		t.Fatalf("read inside back window: %v", err)
	}
	checkPattern(t, got, head-1<<20)
	// Far behind the head the bytes are gone.
	if _, err := b.ReadAt(got, 0); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("read far behind head: err=%v, want ErrNotPresent", err)
	}
}

// TestInitialRangesFastPath seeds a reopened buffer and confirms reads are
// served (the reopen path also seeds the lock-free fast-read states).
func TestInitialRangesFastPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "buf.bin")
	p := newTestPool(t, PoolConfig{})

	b := newTestBuffer(t, p, Config{DiskPath: path, TotalSize: 4 << 20})
	data := make([]byte, 4<<20)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}

	b2 := newTestBuffer(t, p, Config{
		DiskPath:      path,
		TotalSize:     4 << 20,
		InitialRanges: []Range{{Off: 0, Size: 4 << 20}},
	})
	missesBefore := b2.Stats().Misses
	got := make([]byte, 1<<20)
	if _, err := b2.ReadAt(got, 1<<20); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 1<<20)
	if b2.Stats().Misses == missesBefore {
		t.Fatal("expected a disk-path read on the reopened buffer")
	}
}
