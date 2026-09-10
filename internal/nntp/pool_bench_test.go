package nntp

import (
	"bufio"
	"context"
	"net"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
)

// newBenchClient builds a Client around the given pools (index-aligned with
// providers) without any real network config. staleThreshold is huge so
// pooled checkouts never pay a verify ping; idleTimeout is huge so entries
// never idle-expire mid-bench.
func newBenchClient(providers []config.UsenetProvider, ordered []*ProviderPool) *Client {
	pools := make(map[string]*ProviderPool, len(ordered))
	for _, pp := range ordered {
		pools[pp.config.ID()] = pp
	}
	return &Client{
		pools:          pools,
		orderedPools:   ordered,
		providers:      providers,
		logger:         zerolog.Nop(),
		idleTimeout:    24 * time.Hour,
		staleThreshold: 24 * time.Hour,
		pingInterval:   24 * time.Hour,
	}
}

// newBenchPool creates a pool pre-filled with `max` pipe-backed connections so
// checkout never dials. The server halves are parked; no pings fire because
// the bench client's staleThreshold is huge.
func newBenchPool(b *testing.B, host string, max int) (*ProviderPool, config.UsenetProvider) {
	provider := config.UsenetProvider{Host: host, Port: 119, MaxConnections: max}
	pp := &ProviderPool{
		conns:  make([]*connectionEntry, 0, max),
		slots:  make(chan struct{}, max),
		max:    max,
		config: provider,
	}
	for range max {
		clientSide, serverSide := net.Pipe()
		conn := &Connection{
			conn:   clientSide,
			reader: bufio.NewReader(clientSide),
			writer: bufio.NewWriter(clientSide),
		}
		b.Cleanup(func() { _ = conn.Close(); _ = serverSide.Close() })
		pp.conns = append(pp.conns, acquireConnectionEntry(conn, provider, time.Now()))
	}
	return pp, provider
}

// reportWaitQuantiles reports acquisition-wait quantiles in milliseconds.
func reportWaitQuantiles(b *testing.B, waits []time.Duration) {
	if len(waits) == 0 {
		return
	}
	slices.Sort(waits)
	q := func(p float64) float64 {
		idx := int(p * float64(len(waits)-1))
		return float64(waits[idx]) / float64(time.Millisecond)
	}
	b.ReportMetric(q(0.50), "p50-wait-ms")
	b.ReportMetric(q(0.99), "p99-wait-ms")
	b.ReportMetric(float64(waits[len(waits)-1])/float64(time.Millisecond), "max-wait-ms")
}

// BenchmarkPoolCheckoutUncontended measures the raw cost of one
// checkout/return cycle with a free slot and a warm pooled connection: the
// per-segment overhead the pool adds when capacity is available.
func BenchmarkPoolCheckoutUncontended(b *testing.B) {
	pp, provider := newBenchPool(b, "bench-a", 8)
	c := newBenchClient([]config.UsenetProvider{provider}, []*ProviderPool{pp})
	ctx := context.Background()

	for b.Loop() {
		conn, prov, err := c.getAnyAvailableConnection(ctx, WorkloadStreamDemand, providerExclusions{})
		if err != nil {
			b.Fatal(err)
		}
		c.put(conn, prov)
	}
}

// BenchmarkPriorityAdmission measures every adjacent priority boundary. The
// higher-class request should wait for one article boundary; the same-class
// control waits behind the saturated workload's FIFO peers.
func BenchmarkPriorityAdmission(b *testing.B) {
	scenarios := []struct {
		name   string
		higher Workload
		load   Workload
	}{
		{"demand_over_prefetch", WorkloadStreamDemand, WorkloadStreamPrefetch},
		{"prefetch_over_download", WorkloadStreamPrefetch, WorkloadDownload},
		{"download_over_background", WorkloadDownload, WorkloadBackground},
	}
	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			for _, contender := range []struct {
				name     string
				workload Workload
			}{
				{"priority", scenario.higher},
				{"same_class", scenario.load},
			} {
				b.Run(contender.name, func(b *testing.B) {
					benchmarkPriorityAdmission(b, scenario.load, contender.workload)
				})
			}
		})
	}
}

func benchmarkPriorityAdmission(b *testing.B, load, contender Workload) {
	const (
		slots       = 8
		workers     = 32
		articleTime = 2 * time.Millisecond
	)
	pp, provider := newBenchPool(b, "bench-priority", slots)
	client := newBenchClient([]config.UsenetProvider{provider}, []*ProviderPool{pp})
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for ctx.Err() == nil {
				conn, acquiredProvider, err := client.getAnyAvailableConnection(ctx, load, providerExclusions{})
				if err != nil {
					return
				}
				time.Sleep(articleTime)
				client.put(conn, acquiredProvider)
			}
		})
	}
	waitForBenchSaturation(b, client, pp, load, workers-slots)

	var totalWait, maxWait time.Duration
	var iterations int64
	for b.Loop() {
		start := time.Now()
		conn, acquiredProvider, err := client.getAnyAvailableConnection(context.Background(), contender, providerExclusions{})
		if err != nil {
			b.Fatal(err)
		}
		wait := time.Since(start)
		totalWait += wait
		maxWait = max(maxWait, wait)
		iterations++
		client.put(conn, acquiredProvider)
	}
	cancel()
	wg.Wait()
	b.ReportMetric(float64(totalWait)/float64(iterations)/float64(time.Millisecond), "mean-wait-ms")
	b.ReportMetric(float64(maxWait)/float64(time.Millisecond), "max-wait-ms")
}

func waitForBenchSaturation(b *testing.B, client *Client, pp *ProviderPool, workload Workload, queuedTarget int) {
	b.Helper()
	deadline := time.Now().Add(5 * time.Second)
	queued := 0
	for time.Now().Before(deadline) {
		client.waitMu.Lock()
		queued = client.waiters[workload].len
		client.waitMu.Unlock()
		if len(pp.slots) == pp.max && queued >= queuedTarget {
			return
		}
		time.Sleep(time.Millisecond)
	}
	b.Fatalf("background workload did not saturate: active=%d queued=%d, want at least %d", len(pp.slots), queued, queuedTarget)
}

// benchContended runs `workers` goroutines that each loop: acquire a
// connection, hold it for `hold` (simulating a segment download), return it.
// With demand at workers/slots oversubscription, ideal ns/op = hold/slots.
// The gap between measured and ideal is time freed slots sat idle because no
// waiter was woken to claim them.
func benchContended(b *testing.B, slots, workers int, hold time.Duration) {
	pp, provider := newBenchPool(b, "bench-a", slots)
	c := newBenchClient([]config.UsenetProvider{provider}, []*ProviderPool{pp})
	ctx := context.Background()

	waitsPerWorker := make([][]time.Duration, workers)
	var wg sync.WaitGroup
	jobs := make(chan struct{}, workers*2)

	start := time.Now()
	for w := range workers {
		wg.Go(func() {
			for range jobs {
				t0 := time.Now()
				conn, prov, err := c.getAnyAvailableConnection(ctx, WorkloadStreamDemand, providerExclusions{})
				if err != nil {
					b.Error(err)
					return
				}
				waitsPerWorker[w] = append(waitsPerWorker[w], time.Since(t0))
				time.Sleep(hold)
				c.put(conn, prov)
			}
		})
	}
	var iterations int64
	for b.Loop() {
		jobs <- struct{}{}
		iterations++
	}
	close(jobs)
	wg.Wait()
	elapsed := time.Since(start)

	var waits []time.Duration
	for _, ws := range waitsPerWorker {
		waits = append(waits, ws...)
	}
	reportWaitQuantiles(b, waits)
	ideal := float64(hold) / float64(slots) * float64(iterations)
	b.ReportMetric(ideal/float64(elapsed)*100, "slot-utilization-%")
}

func BenchmarkPoolContended3x(b *testing.B) {
	// 20 provider slots, 60 demanders (e.g. 6 open files x 10 per-file
	// conns), 20ms simulated segment fetch.
	benchContended(b, 20, 60, 20*time.Millisecond)
}

func BenchmarkPoolContended8x(b *testing.B) {
	// Heavier oversubscription with a faster op: stresses wakeup delivery.
	benchContended(b, 8, 64, 5*time.Millisecond)
}

// startSilentServer listens on loopback and accepts connections without ever
// sending an NNTP greeting — a provider that is up at the TCP level but
// unresponsive (overloaded, blackholed by a middlebox after SYN, etc).
func startSilentServer(b *testing.B) (addr *net.TCPAddr) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	b.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
	})
	return ln.Addr().(*net.TCPAddr)
}

// BenchmarkAcquireDeadPrimary measures per-acquisition latency when the
// highest-priority provider accepts TCP but never answers, while a healthy
// lower-priority provider has warm pooled connections available.
func BenchmarkAcquireDeadPrimary(b *testing.B) {
	addr := startSilentServer(b)

	saved := timeouts
	timeouts.HandshakeTimeout = 1 * time.Second
	b.Cleanup(func() { timeouts = saved })

	dead := config.UsenetProvider{Host: "127.0.0.1", Port: addr.Port, MaxConnections: 4, Priority: 1}
	deadPool := &ProviderPool{
		conns:  make([]*connectionEntry, 0, dead.MaxConnections),
		slots:  make(chan struct{}, dead.MaxConnections),
		max:    dead.MaxConnections,
		config: dead,
	}
	healthyPool, healthy := newBenchPool(b, "localhost", 8)
	healthy.Priority = 2

	c := newBenchClient(
		[]config.UsenetProvider{dead, healthy},
		[]*ProviderPool{deadPool, healthyPool},
	)
	ctx := context.Background()

	for b.Loop() {
		conn, prov, err := c.getAnyAvailableConnection(ctx, WorkloadStreamDemand, providerExclusions{})
		if err != nil {
			b.Fatal(err)
		}
		c.put(conn, prov)
	}
}
