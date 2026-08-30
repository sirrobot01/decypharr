package buffer

import (
	"errors"
	"sync"
	"testing"
)

// TestWriteThroughPolicyBypassesBlockCache: under WriteThrough no new RAM
// blocks are admitted; every write pwrites and publishes, and completed
// blocks flip to the lock-free fast-read state.
func TestWriteThroughPolicyBypassesBlockCache(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 8 << 20, WritePolicy: WriteThrough})

	data := make([]byte, 4<<20)
	fillPattern(data, 0)
	if _, err := b.WriteAt(data, 0); err != nil {
		t.Fatal(err)
	}

	st := b.Stats()
	if st.BlocksInRAM != 0 || st.BytesInRAM != 0 {
		t.Fatalf("WriteThrough admitted RAM blocks: %+v", st)
	}
	if st.WritesThrough == 0 {
		t.Fatalf("expected write-through stats, got %+v", st)
	}

	// Fully-covered blocks must be on the lock-free fast path.
	for blkOff := int64(0); blkOff < 4<<20; blkOff += blockSize {
		if slot := b.stateSlot(blkOff); slot == nil || slot.Load() != stateFastDisk {
			t.Fatalf("block %d not stateFastDisk after full write-through", blkOff)
		}
	}

	// And the bytes are exact.
	got := make([]byte, 4<<20)
	if _, err := b.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 0)
	if b.Stats().Misses == 0 {
		t.Fatal("expected the read to be served via the disk path")
	}
}

// TestWriteThroughRewriteVisibility: overwrites through the write-through
// path are immediately visible and don't disturb neighbouring bytes. (Under
// pure WriteThrough no block is ever admitted, so the resident-block branch
// in writeRegion is defensive; the observable contract is rewrite-through-
// disk correctness.)
func TestWriteThroughRewriteVisibility(t *testing.T) {
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: 4 << 20, WritePolicy: WriteThrough})

	first := make([]byte, 1<<20)
	fillPattern(first, 0)
	if _, err := b.WriteAt(first, 0); err != nil {
		t.Fatal(err)
	}
	// Overwrite a sub-range and verify the rewrite is what reads back.
	over := make([]byte, 128<<10)
	fillPattern(over, 1<<30) // different pattern seed
	if _, err := b.WriteAt(over, 256<<10); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 128<<10)
	if _, err := b.ReadAt(got, 256<<10); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, got, 1<<30)
	// Neighbouring bytes untouched.
	head := make([]byte, 256<<10)
	if _, err := b.ReadAt(head, 0); err != nil {
		t.Fatal(err)
	}
	checkPattern(t, head, 0)
}

// TestWriteThroughConcurrent is the WriteThrough twin of the streaming
// workload race test: many writers, readers on completed regions, discards
// behind — all through the write-through path.
func TestWriteThroughConcurrent(t *testing.T) {
	const (
		regions    = 32
		regionSize = 1 << 20
	)
	p := newTestPool(t, PoolConfig{})
	b := newTestBuffer(t, p, Config{TotalSize: regions * regionSize, WritePolicy: WriteThrough})

	work := make(chan int64, regions)
	for i := 0; i < regions; i++ {
		work <- int64(i) * regionSize
	}
	close(work)
	written := make(chan int64, regions)

	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			buf := make([]byte, regionSize)
			for off := range work {
				fillPattern(buf, off)
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
				if err := b.Discard(off, regionSize); err != nil && !errors.Is(err, ErrClosed) {
					t.Errorf("Discard(%d): %v", off, err)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(written)
	rg.Wait()

	if b.Stats().BlocksInRAM != 0 {
		t.Fatalf("WriteThrough left RAM blocks resident: %+v", b.Stats())
	}
}
