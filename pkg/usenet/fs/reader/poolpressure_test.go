package reader

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/buffer"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

// withTestPool swaps the process-wide usenet buffer pool for one with a known
// RAM budget, so behaviour under pool pressure is testable.
func withTestPool(t *testing.T, budget int64) {
	t.Helper()
	old := bufPool
	oldExtents := extents
	bufPool = buffer.NewPool(buffer.PoolConfig{Name: "usenet-test", MemoryBudget: budget})
	extents = newExtentPool(budget)
	bufPoolOnce.Do(func() {}) // fence the singleton so usenetBufferPool keeps ours
	extentOnce.Do(func() {})
	t.Cleanup(func() {
		_ = bufPool.Close()
		if old == nil {
			// The singleton had not been built yet, and Do above fenced it
			// permanently — leave a real pool behind for later tests.
			old = buffer.NewPool(buffer.PoolConfig{
				Name:         "usenet",
				MemoryBudget: config.Get().Usenet.BufferMemoryBytes(),
			})
		}
		bufPool = old
		if oldExtents == nil {
			oldExtents = newExtentPool(config.Get().Usenet.BufferMemoryBytes())
		}
		extents = oldExtents
	})
}

const ppSegSize = int64(750 * 1024)

// ppStack builds a fake NNTP server carrying `streams` independent files —
// the multi-volume RAR set that made pool pressure the steady state — and a
// client pointed at it.
func ppStack(t *testing.T, streams, segsPer int, rtt time.Duration) (*nntpd.Server, *nntp.Client, [][]SegmentMeta) {
	t.Helper()
	srv, err := nntpd.New(nntpd.Config{RTT: rtt})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	all := make([][]SegmentMeta, streams)
	fileSize := ppSegSize * int64(segsPer)
	for s := range streams {
		segs := make([]SegmentMeta, segsPer)
		for i := range segs {
			off := int64(i) * ppSegSize
			id := fmt.Sprintf("<v%d-seg%d@nntpd>", s, i)
			srv.AddArticle(id, nntpd.Encode(nntpd.Pattern(off, int(ppSegSize)), "v.bin", i+1, fileSize, off))
			segs[i] = SegmentMeta{
				MessageID:   id,
				Number:      i + 1,
				Bytes:       ppSegSize,
				StartOffset: off,
				EndOffset:   off + ppSegSize - 1,
			}
		}
		all[s] = segs
	}

	host, port := srv.Addr()
	client, err := nntp.NewClient(&config.Config{Usenet: config.Usenet{
		Providers: []config.UsenetProvider{{Host: host, Port: port, MaxConnections: 32}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return srv, client, all
}

// TestPlaybackUnderPoolPressure is the regression guard for the memory-buffer
// playback stalls. Several streams play at once while the shared RAM pool is
// deliberately far too small to hold all their windows, which is the ordinary
// case for a multi-volume RAR set.
//
// Before the fix this deadlocked outright: a block drop flipped a segment the
// reader had already ensured from OnDisk back to Empty, WaitForSegment parked
// on a state nothing would ever change, and playback stopped for good. The
// test asserts the two properties that failure violated — playback keeps up
// with the consumer, and the pool does not force streams to re-download what
// they just fetched.
func TestPlaybackUnderPoolPressure(t *testing.T) {
	cases := []struct {
		name             string
		streams, segsPer int
		poolMB           int64
		maxAmplification float64
	}{
		// Pool holds ~2 of 6 windows.
		{"moderate", 6, 64, 96, 2.0},
		// Pool holds well under one window per stream: every stream is
		// permanently over its share.
		{"severe", 12, 48, 48, 3.5},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTestPool(t, tc.poolMB<<20)
			srv, client, all := ppStack(t, tc.streams, tc.segsPer, 2*time.Millisecond)

			fileSize := ppSegSize * int64(tc.segsPer)
			playBytes := fileSize / 2
			readers := make([]*StreamingReader, tc.streams)
			for s := range tc.streams {
				sr, err := NewStreamingReader(context.Background(), client, all[s],
					WithMaxConnections(8), WithPrefetchAhead(22), WithRetention(RetentionWindow))
				if err != nil {
					t.Fatal(err)
				}
				readers[s] = sr
				t.Cleanup(func() { _ = sr.Close() })
			}

			// Consumption far slower than download is what lets the
			// prefetchers fill the pool and keep it full.
			const playbackBytesPerSec = 5_000_000 // ~40 Mbps
			ideal := time.Duration(float64(playBytes) / playbackBytesPerSec * float64(time.Second))

			var wg sync.WaitGroup
			worst := make([]time.Duration, tc.streams)
			start := time.Now()
			for s := range tc.streams {
				wg.Add(1)
				go func() {
					defer wg.Done()
					p := make([]byte, 128*1024)
					pace := time.Duration(float64(len(p)) / playbackBytesPerSec * float64(time.Second))
					next := time.Now()
					for off := int64(0); off < playBytes; off += int64(len(p)) {
						next = next.Add(pace)
						if d := time.Until(next); d > 0 {
							time.Sleep(d)
						}
						t0 := time.Now()
						if _, err := readers[s].ReadAt(p[:min(int64(len(p)), playBytes-off)], off); err != nil {
							t.Errorf("stream %d read at %d: %v", s, off, err)
							return
						}
						if d := time.Since(t0); d > worst[s] {
							worst[s] = d
						}
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start)

			var maxRead time.Duration
			for _, w := range worst {
				if w > maxRead {
					maxRead = w
				}
			}
			unique := int64(tc.streams * tc.segsPer / 2)
			bodies := srv.Bodies.Load()
			amplification := float64(bodies) / float64(unique)
			t.Logf("elapsed=%v ideal=%v bodies=%d unique=%d amplification=%.2fx worstRead=%v",
				elapsed, ideal, bodies, unique, amplification, maxRead)

			// Playback is paced, so a healthy run finishes in about `ideal`.
			// The old deadlock never finished at all; anything past 2x means
			// reads are blocking on re-fetches the player should not see.
			if elapsed > 2*ideal {
				t.Errorf("playback fell behind: %v for a %v workload", elapsed, ideal)
			}
			if amplification > tc.maxAmplification {
				t.Errorf("re-download amplification %.2fx exceeds %.2fx: the pool is evicting data streams still need",
					amplification, tc.maxAmplification)
			}
		})
	}
}
