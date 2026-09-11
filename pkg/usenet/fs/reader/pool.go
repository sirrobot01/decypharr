package reader

import (
	"sync"

	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/config"
)

// usenet owns its streaming-buffer pool here rather than the buffer package
// owning a "usenet" singleton — the buffer package stays generic. The pool is
// created once with the configured usenet RAM budget and shared across every
// SegmentCache. Each disk-backed cache enforces its own disk cap.
var (
	bufPoolOnce sync.Once
	bufPool     *buffer.Pool
	extentOnce  sync.Once
	extents     *extentPool
)

func usenetBufferPool() *buffer.Pool {
	bufPoolOnce.Do(func() {
		bufPool = buffer.NewPool(buffer.PoolConfig{
			Name:         "usenet",
			MemoryBudget: config.Get().Usenet.BufferMemoryBytes(),
		})
	})
	return bufPool
}

func usenetExtentPool() *extentPool {
	extentOnce.Do(func() {
		extents = newExtentPool(config.Get().Usenet.BufferMemoryBytes())
	})
	return extents
}
