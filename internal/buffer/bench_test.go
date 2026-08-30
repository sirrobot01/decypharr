package buffer

import (
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The benchmarks model the streaming workload: a downloader appending
// chunk-sized writes at the frontier, readers following behind, RAM budget
// small enough that block admission/eviction/write-through are all exercised.
// They are the A/B gate for buffer-engine changes (lock scope, write policy).

const (
	benchChunk  = 128 << 10
	benchWindow = int64(64 << 20) // sliding window kept on disk during streams
)

func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(float64(len(sorted)-1) * q)
	return sorted[i]
}

// BenchmarkStreamSequential: single writer streams 128KB appends with a
// reader following 4MB behind; disk bounded by discarding a lagging window.
func BenchmarkStreamSequential(b *testing.B) {
	b.Run("auto", func(b *testing.B) { benchStreamSequential(b, WriteAuto) })
	b.Run("writethrough", func(b *testing.B) { benchStreamSequential(b, WriteThrough) })
}

func benchStreamSequential(b *testing.B, policy WritePolicy) {
	p := NewPool(PoolConfig{Name: "bench"})
	defer p.Close()
	buf, err := p.NewBuffer(Config{
		DiskPath:    filepath.Join(b.TempDir(), "buf.bin"),
		TotalSize:   1 << 40, // sparse; real usage bounded by the discard window
		WritePolicy: policy,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer buf.Close()

	chunk := make([]byte, benchChunk)
	fillPattern(chunk, 0)
	rbuf := make([]byte, benchChunk)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := int64(i) * benchChunk
		if _, err := buf.WriteAt(chunk, off); err != nil {
			b.Fatal(err)
		}
		buf.SetReadHead(off)
		if off >= 4<<20 {
			if _, err := buf.ReadAt(rbuf, off-4<<20); err != nil {
				b.Fatal(err)
			}
		}
		if off > benchWindow {
			if err := buf.Discard(off-benchWindow, benchChunk); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// BenchmarkStreamContendedReads: the timed loop is the writer; 4 reader
// goroutines hammer random already-written offsets. Reports reader p50/p99/max
// latency — the number the under-lock flush/mmap/pread work inflates.
func BenchmarkStreamContendedReads(b *testing.B) {
	b.Run("auto", func(b *testing.B) { benchStreamContendedReads(b, WriteAuto) })
	b.Run("writethrough", func(b *testing.B) { benchStreamContendedReads(b, WriteThrough) })
}

func benchStreamContendedReads(b *testing.B, policy WritePolicy) {
	p := NewPool(PoolConfig{Name: "bench"})
	defer p.Close()
	buf, err := p.NewBuffer(Config{
		DiskPath:    filepath.Join(b.TempDir(), "buf.bin"),
		TotalSize:   1 << 40,
		WritePolicy: policy,
	})
	if err != nil {
		b.Fatal(err)
	}
	defer buf.Close()

	// Pre-fill a window so readers have something from iteration one.
	chunk := make([]byte, benchChunk)
	fillPattern(chunk, 0)
	const prefill = int64(8 << 20)
	for off := int64(0); off < prefill; off += benchChunk {
		if _, err := buf.WriteAt(chunk, off); err != nil {
			b.Fatal(err)
		}
	}

	var frontier atomic.Int64
	frontier.Store(prefill)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	const readers = 4
	lats := make([][]time.Duration, readers)
	for r := 0; r < readers; r++ {
		r := r
		wg.Add(1)
		go func() {
			defer wg.Done()
			rbuf := make([]byte, benchChunk)
			seed := uint64(r)*2654435761 + 12345
			for {
				select {
				case <-stop:
					return
				default:
				}
				f := frontier.Load()
				lo := f - 32<<20
				if lo < 0 {
					lo = 0
				}
				span := (f - benchChunk - lo) / benchChunk
				if span <= 0 {
					continue
				}
				seed = seed*6364136223846793005 + 1442695040888963407
				off := lo + int64(seed%uint64(span))*benchChunk
				start := time.Now()
				if _, err := buf.ReadAt(rbuf, off); err != nil {
					// A discard racing the offset pick is expected near the
					// window tail; skip it.
					continue
				}
				lats[r] = append(lats[r], time.Since(start))
			}
		}()
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		off := frontier.Load()
		if _, err := buf.WriteAt(chunk, off); err != nil {
			b.Fatal(err)
		}
		frontier.Store(off + benchChunk)
		buf.SetReadHead(off)
		if off > benchWindow {
			_ = buf.Discard(off-benchWindow, benchChunk)
		}
	}
	b.StopTimer()
	close(stop)
	wg.Wait()

	var all []time.Duration
	for _, l := range lats {
		all = append(all, l...)
	}
	if len(all) > 0 {
		sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
		b.ReportMetric(float64(quantile(all, 0.5).Nanoseconds()), "p50-read-ns")
		b.ReportMetric(float64(quantile(all, 0.99).Nanoseconds()), "p99-read-ns")
		b.ReportMetric(float64(all[len(all)-1].Nanoseconds()), "max-read-ns")
		b.ReportMetric(float64(len(all)), "reads")
	}
}

// BenchmarkReadWarmDisk: pure lock-free fast-path reads (reopened buffer,
// everything on disk, no resident blocks). Baseline for read-path overhead.
func BenchmarkReadWarmDisk(b *testing.B) {
	path := filepath.Join(b.TempDir(), "buf.bin")
	p := NewPool(PoolConfig{Name: "bench"})
	defer p.Close()

	const size = int64(64 << 20)
	{
		w, err := p.NewBuffer(Config{DiskPath: path, TotalSize: size})
		if err != nil {
			b.Fatal(err)
		}
		chunk := make([]byte, 1<<20)
		fillPattern(chunk, 0)
		for off := int64(0); off < size; off += 1 << 20 {
			if _, err := w.WriteAt(chunk, off); err != nil {
				b.Fatal(err)
			}
		}
		if err := w.Sync(); err != nil {
			b.Fatal(err)
		}
		if err := w.Close(); err != nil {
			b.Fatal(err)
		}
	}
	buf, err := p.NewBuffer(Config{
		DiskPath:      path,
		TotalSize:     size,
		InitialRanges: []Range{{Off: 0, Size: size}},
	})
	if err != nil {
		b.Fatal(err)
	}
	defer buf.Close()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		rbuf := make([]byte, benchChunk)
		var i int64
		for pb.Next() {
			off := (i * benchChunk) % (size - benchChunk)
			i++
			if _, err := buf.ReadAt(rbuf, off); err != nil {
				b.Error(err)
				return
			}
		}
	})
}
