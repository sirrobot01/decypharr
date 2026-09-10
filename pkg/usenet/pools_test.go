package usenet

import (
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/usenet/fs/reader"
)

func TestRestartAppliesUsenetMemoryBudget(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	t.Cleanup(config.Reset)
	var firstDepth int
	var firstPools *reader.Pools
	for _, budget := range []string{"64MB", "8MB"} {
		if _, err := config.Update(func(cfg *config.Config) error {
			cfg.Usenet.Providers = []config.UsenetProvider{{Host: "127.0.0.1", Port: 1, MaxConnections: 1}}
			cfg.Usenet.BufferMemory = budget
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		service, err := New()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = service.Close() })
		cfg := reader.DefaultConfig()
		cfg.Pools = service.bufferPools
		segments := make([]reader.SegmentMeta, 64)
		for i := range segments {
			segments[i].Bytes = 1 << 20
		}
		cache, err := reader.NewSegmentCache(t.Context(), segments, cfg, &reader.ReaderStats{}, zerolog.Nop())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cache.Close() })
		depth := cache.MaxPrefetchSegments()
		if firstPools == nil {
			firstPools, firstDepth = service.bufferPools, depth
		} else if firstPools == service.bufferPools || depth >= firstDepth {
			t.Fatalf("restart retained its old pool budget: prefetch %d -> %d", firstDepth, depth)
		}
		if err := cache.Close(); err != nil {
			t.Fatal(err)
		}
		if err := service.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
