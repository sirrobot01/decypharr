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
			b.ResetTimer()
			for range b.N {
				err := client.ExecuteWithFailover(ctx, func(conn *Connection) error {
					_, err := conn.StreamBody("<bench@nntpd>", w)
					return err
				})
				if err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			if w.n != int64(b.N)*benchSegmentSize {
				b.Fatalf("streamed %d bytes, want %d", w.n, int64(b.N)*benchSegmentSize)
			}
		})
	}
}
