package nntp

import (
	"context"
	"fmt"
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
