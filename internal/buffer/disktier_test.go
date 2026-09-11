package buffer

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestImmutableDiskBypassesRAMBlocks(t *testing.T) {
	p := newTestPool(t, PoolConfig{MemoryBudget: 4 << 20})
	b := newTestBuffer(t, p, Config{
		DiskPath:      tempDisk(t),
		TotalSize:     2 << 20,
		ImmutableDisk: true,
	})
	want := make([]byte, 750<<10)
	fillPattern(want, 128<<10)
	if _, err := b.WriteAt(want, 128<<10); err != nil {
		t.Fatal(err)
	}
	stats := b.Stats()
	if stats.BlocksInRAM != 0 || stats.BytesInRAM != 0 {
		t.Fatalf("immutable write allocated RAM blocks: %+v", stats)
	}
	got := make([]byte, len(want))
	if _, err := b.ReadAt(got, 128<<10); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("immutable disk read mismatch")
	}
}

func TestDiskLimitSerializesConcurrentWrites(t *testing.T) {
	const limit = 4 * blockSize
	p := newTestPool(t, PoolConfig{DiskLimit: limit})

	buffers := make([]*Buffer, 12)
	for i := range buffers {
		buffers[i] = newTestBuffer(t, p, Config{
			DiskPath:      tempDisk(t),
			TotalSize:     blockSize,
			ImmutableDisk: true,
		})
	}

	results := make(chan error, len(buffers))
	var wg sync.WaitGroup
	for _, b := range buffers {
		wg.Go(func() {
			_, err := b.WriteAt(make([]byte, blockSize), 0)
			results <- err
		})
	}
	wg.Wait()
	close(results)

	succeeded := 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrDiskLimit):
		default:
			t.Fatalf("unexpected write error: %v", err)
		}
	}
	if succeeded != int(limit/blockSize) {
		t.Fatalf("successful writes = %d, want %d", succeeded, limit/blockSize)
	}
	if got := p.Stats().DiskInUse; got > limit {
		t.Fatalf("disk usage crossed hard limit: got %d, limit %d", got, limit)
	}
}

func TestPersistentDiskAccountingSurvivesCloseAndReopen(t *testing.T) {
	const persisted = 512 << 10
	p := newTestPool(t, PoolConfig{
		DiskLimit:        blockSize,
		InitialDiskUsage: persisted,
	})
	path := tempDisk(t)
	seed := []Range{{Off: 0, Size: persisted}}

	b := newTestBuffer(t, p, Config{
		DiskPath:                  path,
		TotalSize:                 blockSize,
		InitialRanges:             seed,
		InitialRangesPreaccounted: true,
		PersistentDisk:            true,
	})
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats().DiskInUse; got != persisted {
		t.Fatalf("usage after persistent close = %d, want %d", got, persisted)
	}

	reopened := newTestBuffer(t, p, Config{
		DiskPath:                  path,
		TotalSize:                 blockSize,
		InitialRanges:             seed,
		InitialRangesPreaccounted: true,
		PersistentDisk:            true,
	})
	if got := p.Stats().DiskInUse; got != persisted {
		t.Fatalf("usage after reopen = %d, want %d", got, persisted)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	p.ReleaseDisk(persisted)
	if got := p.Stats().DiskInUse; got != 0 {
		t.Fatalf("usage after physical removal = %d, want 0", got)
	}
}

// TestDiskTierSurvivesEviction writes far more than the RAM window holds, so
// most blocks are flushed out to the file, and checks every byte still reads
// back. This is the contract the whole tier exists for.
func TestDiskTierSurvivesEviction(t *testing.T) {
	const total = 16 << 20
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: total, MemorySize: 4 << 20})

	data := make([]byte, total)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}

	st := b.Stats()
	if st.Evictions == 0 || st.DiskWrites == 0 {
		t.Fatalf("expected blocks to be flushed out of the window: %+v", st)
	}
	if st.BytesInRAM > 4<<20 {
		t.Fatalf("RAM window exceeded: %d", st.BytesInRAM)
	}

	got := make([]byte, total)
	if _, err := b.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 0)
	if b.Stats().Misses == 0 {
		t.Fatal("expected at least one read to come from the disk tier")
	}
}

// TestDiskTierFaultsInOnReadmit covers the read-modify-write case: a block is
// flushed and dropped, then a *different* part of the same block is written,
// which makes it resident again. Without faulting the flushed bytes back in,
// a read of them would find the block resident and copy out zeros.
func TestDiskTierFaultsInOnReadmit(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: 4 << 20, MemorySize: blockSize})

	head := make([]byte, 256<<10)
	fillPattern(head, 0)
	if _, err := b.WriteAt(head, 0); err != nil {
		t.Fatal(err)
	}

	// Touch the next block to push block 0 out to the file.
	other := make([]byte, 256<<10)
	fillPattern(other, 1<<20)
	if _, err := b.WriteAt(other, blockSize); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.blocks[0]; ok {
		t.Fatal("block 0 should have been evicted")
	}

	// Re-admit block 0 by writing a disjoint region of it.
	tail := make([]byte, 256<<10)
	fillPattern(tail, 1<<30)
	if _, err := b.WriteAt(tail, 512<<10); err != nil {
		t.Fatal(err)
	}

	gotHead := make([]byte, 256<<10)
	if _, err := b.ReadAt(gotHead, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, gotHead, 0)

	gotTail := make([]byte, 256<<10)
	if _, err := b.ReadAt(gotTail, 512<<10); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, gotTail, 1<<30)
}

// TestDiskTierRewriteVisibility: overwrites are immediately visible and don't
// disturb neighbouring bytes.
func TestDiskTierRewriteVisibility(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: 4 << 20})

	first := make([]byte, 1<<20)
	fillPattern(first, 0)
	if _, err := b.WriteAt(first, 0); err != nil {
		t.Fatal(err)
	}
	over := make([]byte, 128<<10)
	fillPattern(over, 1<<30)
	if _, err := b.WriteAt(over, 256<<10); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128<<10)
	if _, err := b.ReadAt(got, 256<<10); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 1<<30)

	head := make([]byte, 256<<10)
	if _, err := b.ReadAt(head, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, head, 0)
}

func TestDiskTierRewriteSurvivesEviction(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: 4 << 20, MemorySize: blockSize})

	first := make([]byte, blockSize)
	fillPattern(first, 0)
	if _, err := b.WriteAt(first, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.WriteAt(make([]byte, blockSize), blockSize); err != nil {
		t.Fatal(err)
	}

	const rewriteOff = 256 << 10
	rewrite := make([]byte, 128<<10)
	fillPattern(rewrite, 1<<30)
	if _, err := b.WriteAt(rewrite, rewriteOff); err != nil {
		t.Fatal(err)
	}
	if got := b.PersistedRanges(rewriteOff, int64(len(rewrite))); len(got) != 0 {
		t.Fatalf("dirty overwrite reported persisted: %+v", got)
	}

	// Admitting another block flushes and evicts the rewritten block.
	if _, err := b.WriteAt(make([]byte, blockSize), 2*blockSize); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(rewrite))
	if _, err := b.ReadAt(got, rewriteOff); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 1<<30)
}

func TestFlushPublishesOnlyCleanRanges(t *testing.T) {
	path := tempDisk(t)
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: path, TotalSize: 2 << 20})

	data := make([]byte, 256<<10)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if got := b.PersistedRanges(0, int64(len(data))); len(got) != 0 {
		t.Fatalf("resident write reported persisted: %+v", got)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	seed := b.PersistedRanges(0, int64(len(data)))
	if len(seed) != 1 || seed[0] != (Range{Off: 0, Size: int64(len(data))}) {
		t.Fatalf("unexpected persisted ranges: %+v", seed)
	}
}

func TestPersistedRangesRemainAvailableAfterClose(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: blockSize})
	data := make([]byte, 256<<10)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	want := Range{Off: 0, Size: int64(len(data))}
	if got := b.PersistedRanges(0, blockSize); len(got) != 1 || got[0] != want {
		t.Fatalf("persisted ranges after close = %+v, want %v", got, want)
	}
}

func TestDiskBackstopPreservesDirtyOverwrite(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: blockSize})
	if _, err := b.WriteAt(make([]byte, blockSize), 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}

	const off = 256 << 10
	rewrite := make([]byte, 128<<10)
	fillPattern(rewrite, 1<<30)
	if _, err := b.WriteAt(rewrite, off); err != nil {
		t.Fatal(err)
	}
	b.SetReadHead(blockSize)
	if reclaimed := b.punchBehindWindow(0); reclaimed == 0 {
		t.Fatal("disk backstop did not reclaim clean bytes")
	}

	got := make([]byte, len(rewrite))
	if _, err := b.ReadAt(got, off); err != nil {
		t.Fatalf("read dirty overwrite after reclaim: %v", err)
	}
	checkPattern(t, got, 1<<30)
}

func TestEvictionReturnsDiskWriteFailure(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: 2 << 20, MemorySize: blockSize})
	if _, err := b.WriteAt(make([]byte, blockSize), 0); err != nil {
		t.Fatal(err)
	}
	if err := b.file.Close(); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := b.WriteAt(make([]byte, blockSize), blockSize)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write succeeded after backing file was closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write hung retrying a failed eviction")
	}
}

func TestDiscardStaysLogicalWhenPunchingIsUnavailable(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{DiskPath: tempDisk(t), TotalSize: blockSize})
	data := make([]byte, 128<<10)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Flush(); err != nil {
		t.Fatal(err)
	}
	b.punchable.Store(false)
	if err := b.Discard(0, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReadAt(make([]byte, len(data)), 0); !errors.Is(err, ErrNotPresent) {
		t.Fatalf("discarded read: %v", err)
	}
	if got := b.PersistedRanges(0, int64(len(data))); len(got) != 0 {
		t.Fatalf("discarded bytes reported persisted: %+v", got)
	}
}

// TestNoDiskTierLosesEvictedBytes is the other half of the contract: with no
// file behind it, an evicted block's bytes are gone and reported, not silently
// returned as zeros.
func TestNoDiskTierLosesEvictedBytes(t *testing.T) {
	var evicted []Range
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{
		MemorySize: 2 * blockSize,
		OnEvict:    func(off, length int64) { evicted = append(evicted, Range{off, length}) },
	})

	data := make([]byte, blockSize)
	for i := range 4 {
		fillPattern(data, int64(i)*blockSize)
		if _, err := b.WriteAt(data, int64(i)*blockSize); err != nil {
			t.Fatal(err)
		}
	}
	if len(evicted) == 0 {
		t.Fatal("expected OnEvict for bytes dropped with no disk tier")
	}
	for _, r := range evicted {
		if b.HasRange(r.Off, r.Size) {
			t.Fatalf("range %d+%d reported evicted but still present", r.Off, r.Size)
		}
	}
}
