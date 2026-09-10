package reader

import (
	"errors"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/buffer"
)

func TestPoolBudgetsBelongToEachRun(t *testing.T) {
	var previous *Pools
	for _, budget := range []int64{64 << 20, 8 << 20} {
		pools := NewPools(budget)
		t.Cleanup(func() { _ = pools.Close() })
		if pools == previous || pools.buffers.Stats().MemoryBudget != budget || pools.extents.stats().MemoryBudget != budget {
			t.Fatalf("new run retained old budget: buffers=%#v extents=%#v", pools.buffers.Stats(), pools.extents.stats())
		}
		cfg := DefaultConfig()
		cfg.Pools = pools
		for _, retention := range []Retention{RetentionWindow, RetentionRewind} {
			cfg.Retention = retention
			cache, err := NewSegmentCache(t.Context(), mkSegs(4, 1024), cfg, &ReaderStats{}, zerolog.Nop())
			if err != nil {
				t.Fatal(err)
			}
			if cache.pools != pools || cache.ownsPools {
				t.Fatal("cache did not use shared service pools")
			}
			if err := cache.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := pools.Close(); err != nil {
			t.Fatal(err)
		}
		if pools.extents.stats().Caches != 0 || pools.buffers.Stats().Buffers != 0 {
			t.Fatal("closed run retains caches")
		}
		if _, err := NewSegmentCache(t.Context(), mkSegs(4, 1024), cfg, &ReaderStats{}, zerolog.Nop()); !errors.Is(err, buffer.ErrClosed) {
			t.Fatalf("old run accepted a cache: %v", err)
		}
		previous = pools
	}
}

func TestStandaloneCacheClosesItsPrivatePools(t *testing.T) {
	cache, err := NewSegmentCache(t.Context(), mkSegs(4, 1024), DefaultConfig(), &ReaderStats{}, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !cache.ownsPools {
		t.Fatal("standalone cache has no pool owner")
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	if !cache.pools.closed {
		t.Fatal("private pools were not closed")
	}
}
