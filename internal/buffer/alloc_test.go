package buffer

import "testing"

func TestPoolCountsReusableAllocations(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{MemorySize: 4 * blockSize})
	data := make([]byte, 4*blockSize)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Discard(0, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	stats := p.Stats()
	if stats.MemoryInUse != 0 || stats.MemoryAllocated != int64(len(data)) {
		t.Fatalf("discarded blocks must remain charged for reuse: %+v", stats)
	}
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	stats = p.Stats()
	if stats.MemoryInUse != int64(len(data)) || stats.MemoryAllocated != int64(len(data)) {
		t.Fatalf("reuse must not charge an allocation twice: %+v", stats)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.MemoryInUse != 0 || stats.MemoryAllocated != 0 {
		t.Fatalf("close left charged blocks: %+v", stats)
	}
}

func TestPoolPressureReleasesReuseBeforeActiveData(t *testing.T) {
	p := newTestPool(t, PoolConfig{MemoryBudget: 4 * blockSize})
	idle := newTestBuffer(t, p, Config{MemorySize: 4 * blockSize})
	active := newTestBuffer(t, p, Config{MemorySize: 4 * blockSize})
	data := make([]byte, 4*blockSize)
	if _, err := idle.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := idle.Discard(0, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if got := p.Stats().MemoryAllocated; got != int64(len(data)) {
		t.Fatalf("idle buffer allocations = %d, want %d", got, len(data))
	}
	if _, err := active.WriteAt([]byte{42}, 0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "reuse released under pool pressure", func() bool {
		return p.Stats().MemoryAllocated == blockSize
	})
	var got [1]byte
	if _, err := active.ReadAt(got[:], 0); err != nil || got[0] != 42 {
		t.Fatalf("active data was removed: data=%v err=%v", got, err)
	}
	if got := active.Stats().Evictions; got != 0 {
		t.Fatalf("evicted active blocks while reuse was available: %d", got)
	}
}

func TestAdmissionPreservesActiveDataAtAllocationLimit(t *testing.T) {
	for _, reuseOwner := range []string{"idle", "active"} {
		t.Run(reuseOwner, func(t *testing.T) {
			p := newTestPool(t, PoolConfig{MemoryBudget: 4 * blockSize})
			active := newTestBuffer(t, p, Config{MemorySize: 4 * blockSize})
			idle := newTestBuffer(t, p, Config{MemorySize: 4 * blockSize})
			for i := range 3 {
				if _, err := active.WriteAt([]byte{42}, int64(i*blockSize)); err != nil {
					t.Fatal(err)
				}
			}
			owner := idle
			if reuseOwner == "active" {
				owner = active
			}
			if _, err := owner.WriteAt([]byte{42}, 3*blockSize); err != nil {
				t.Fatal(err)
			}
			if err := owner.Discard(3*blockSize, blockSize); err != nil {
				t.Fatal(err)
			}
			if stats := p.Stats(); stats.MemoryInUse != 3*blockSize || stats.MemoryAllocated != 4*blockSize {
				t.Fatalf("expected three active blocks and one reusable block: %+v", stats)
			}
			if _, err := active.WriteAt([]byte{42}, 3*blockSize); err != nil {
				t.Fatal(err)
			}
			if got := active.Stats().Evictions; got != 0 {
				t.Fatalf("evicted %d active blocks despite available reuse", got)
			}
			for i := range 4 {
				var got [1]byte
				if _, err := active.ReadAt(got[:], int64(i*blockSize)); err != nil || got[0] != 42 {
					t.Fatalf("active block %d was removed: data=%v err=%v", i, got, err)
				}
			}
			if stats := p.Stats(); stats.MemoryInUse != 4*blockSize || stats.MemoryAllocated != 4*blockSize {
				t.Fatalf("allocation limit changed during admission: %+v", stats)
			}
		})
	}
}

func TestPendingBlockRemainsChargedUntilRelease(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	a := blockAllocator{pool: p}
	data := a.get()
	// Hold a release request before the worker processes it.
	pending := make(chan blockRelease, 1)
	pending <- blockRelease{data: data, pool: p}
	if got := p.Stats().MemoryAllocated; got != blockSize {
		t.Fatalf("pending allocation = %d, want %d", got, blockSize)
	}
	(<-pending).release()
	if got := p.Stats().MemoryAllocated; got != 0 {
		t.Fatalf("released allocation remains charged: %d", got)
	}
}

func TestCloseReleasesRetainedAndDeferredAllocations(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{MemorySize: 16 * blockSize})
	data := make([]byte, 16*blockSize)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Discard(0, int64(len(data))); err != nil {
		t.Fatal(err)
	}
	if stats := p.Stats(); stats.MemoryInUse != 0 || stats.MemoryAllocated < maxReuseBlocks*blockSize {
		t.Fatalf("reuse or deferred blocks are not charged: %+v", stats)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "all allocations released after close", func() bool {
		return p.Stats().MemoryAllocated == 0
	})
}
