package nntp

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

const benchSegmentSize = 750 * 1024

func newBenchServerClient(b *testing.B, cfg nntpd.Config, maxConns int) (*nntpd.Server, *Client) {
	b.Helper()
	srv, err := nntpd.New(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(srv.Close)

	host, port := srv.Addr()
	client, err := NewClient(&config.Config{
		Usenet: config.Usenet{
			Providers: []config.UsenetProvider{{
				Host:           host,
				Port:           port,
				MaxConnections: maxConns,
			}},
		},
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = client.Close() })
	return srv, client
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// BenchmarkStreamBodyE2E measures one full BODY round trip — command, status
// line, yEnc decode, single write — through a real dialed connection, at
// several simulated RTTs. Per-article cost is 1 RTT + transfer + decode.
func BenchmarkStreamBodyE2E(b *testing.B) {
	payload := nntpd.Pattern(0, benchSegmentSize)
	body := nntpd.Encode(payload, "bench.bin", 1, benchSegmentSize, 0)

	for _, rtt := range []time.Duration{0, 10 * time.Millisecond, 30 * time.Millisecond} {
		b.Run(fmt.Sprintf("rtt%dms", rtt/time.Millisecond), func(b *testing.B) {
			srv, client := newBenchServerClient(b, nntpd.Config{RTT: rtt}, 2)
			srv.AddArticle("<bench@nntpd>", body)

			ctx := context.Background()
			w := &countingWriter{}
			b.SetBytes(benchSegmentSize)
			var iterations int64
			for b.Loop() {
				err := client.ExecuteWithFailover(ctx, WorkloadStreamDemand, func(conn *Connection) error {
					_, err := conn.StreamBody("<bench@nntpd>", w)
					return err
				})
				if err != nil {
					b.Fatal(err)
				}
				iterations++
			}
			if w.n != iterations*benchSegmentSize {
				b.Fatalf("streamed %d bytes, want %d", w.n, iterations*benchSegmentSize)
			}
		})
	}
}

// BenchmarkStatBatchE2E compares the former request/response loop with a
// sixteen-command pipeline on one warm connection. The fake server charges
// one configured RTT per wire burst, so both paths still exercise real TCP
// framing and response parsing.
func BenchmarkStatBatchE2E(b *testing.B) {
	const pipelineDepth = 16
	for _, rtt := range []time.Duration{0, 10 * time.Millisecond, 30 * time.Millisecond} {
		for _, pipelined := range []bool{false, true} {
			name := "sequential"
			if pipelined {
				name = "pipelined"
			}
			b.Run(fmt.Sprintf("rtt%dms/%s", rtt/time.Millisecond, name), func(b *testing.B) {
				srv, client := newBenchServerClient(b, nntpd.Config{RTT: rtt}, 1)
				messageIDs := make([]string, pipelineDepth)
				for i := range pipelineDepth {
					messageIDs[i] = fmt.Sprintf("<stat-%d@nntpd>", i)
					srv.AddArticle(messageIDs[i], []byte{1})
				}
				conn, provider, err := client.getConnectionFromProvider(context.Background(), WorkloadBackground, client.providers[0])
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(func() { client.returnOrReleaseConn(conn, provider) })
				b.ReportMetric(pipelineDepth, "stats/op")

				for b.Loop() {
					if pipelined {
						if _, err := conn.StatBatch(messageIDs); err != nil {
							b.Fatal(err)
						}
						continue
					}
					for _, messageID := range messageIDs {
						if _, _, err := conn.Stat(messageID); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

// BenchmarkStreamBodyPriorityUnderDownloadPressure exercises the complete
// BODY path through real TCP connections while bulk downloads saturate every
// provider slot. It measures the time until a decoded segment reaches the
// cache boundary, not merely the scheduler handoff.
func BenchmarkStreamBodyPriorityUnderDownloadPressure(b *testing.B) {
	const (
		slots             = 4
		downloadWorkers   = 12
		providerBandwidth = 32 << 20
	)
	for _, workload := range []Workload{WorkloadStreamDemand, WorkloadStreamPrefetch, WorkloadDownload} {
		b.Run(workload.String(), func(b *testing.B) {
			payload := nntpd.Pattern(0, benchSegmentSize)
			body := nntpd.Encode(payload, "priority.bin", 1, benchSegmentSize, 0)
			srv, client := newBenchServerClient(b, nntpd.Config{
				RTT:       10 * time.Millisecond,
				Bandwidth: providerBandwidth,
			}, slots)
			srv.AddArticle("<priority@nntpd>", body)
			pp := client.orderedPools[0]

			loadCtx, cancelLoad := context.WithCancel(context.Background())
			var loadWG sync.WaitGroup
			errors := make(chan error, downloadWorkers)
			for range downloadWorkers {
				loadWG.Go(func() {
					for loadCtx.Err() == nil {
						err := client.ExecuteWithFailover(loadCtx, WorkloadDownload, func(conn *Connection) error {
							_, err := conn.StreamBody("<priority@nntpd>", io.Discard)
							return err
						})
						if err != nil && loadCtx.Err() == nil {
							errors <- err
							return
						}
					}
				})
			}
			waitForBenchSaturation(b, client, pp, WorkloadDownload, downloadWorkers-slots)

			var totalWait, maxWait, totalSegment time.Duration
			var iterations int64
			b.SetBytes(benchSegmentSize)
			for b.Loop() {
				started := time.Now()
				conn, provider, err := client.getAnyAvailableConnection(context.Background(), workload, providerExclusions{})
				if err != nil {
					b.Fatal(err)
				}
				wait := time.Since(started)
				totalWait += wait
				maxWait = max(maxWait, wait)
				if _, err := conn.StreamBody("<priority@nntpd>", io.Discard); err != nil {
					client.release(conn)
					b.Fatal(err)
				}
				client.put(conn, provider)
				totalSegment += time.Since(started)
				iterations++
			}

			cancelLoad()
			loadWG.Wait()
			close(errors)
			for err := range errors {
				b.Error(err)
			}
			b.ReportMetric(float64(totalWait)/float64(iterations)/1e6, "mean-admission-ms")
			b.ReportMetric(float64(maxWait)/1e6, "max-admission-ms")
			b.ReportMetric(float64(totalSegment)/float64(iterations)/1e6, "mean-segment-ms")
		})
	}
}
