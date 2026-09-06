package reader

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
)

func newPipelineIntegrityReader(t *testing.T, providers []config.UsenetProvider, segments []SegmentMeta) *StreamingReader {
	t.Helper()
	client, err := nntp.NewClient(&config.Config{Usenet: config.Usenet{Providers: providers}})
	if err != nil {
		t.Fatal(err)
	}
	sr, err := NewStreamingReader(t.Context(), client, segments,
		WithRetention(RetentionDelivery), WithMaxConnections(2), WithPrefetchAhead(0),
		WithBodyPipelineDepth(2), WithDownloadTimeout(5*time.Second))
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := errors.Join(sr.Close(), client.Close()); err != nil {
			t.Error(err)
		}
		if pool := sr.cache.extentPool.stats(); pool.MemoryInUse != 0 || pool.Caches != 0 || sr.cache.residentN.Load() != 0 {
			t.Errorf("cache ownership after close: %+v, resident=%d", pool, sr.cache.residentN.Load())
		}
	})
	return sr
}

func pipelineIntegritySegments(size, dataStart int) []SegmentMeta {
	return []SegmentMeta{
		{MessageID: "<zero@integrity>", Number: 1, Bytes: int64(size), StartOffset: 0, EndOffset: int64(size - 1), SegmentDataStart: int64(dataStart)},
		{MessageID: "<one@integrity>", Number: 2, Bytes: int64(size), StartOffset: int64(size), EndOffset: int64(2*size - 1)},
	}
}

func corruptPipelineIntegrityBody(t *testing.T, payload []byte, part int, total, offset int64) []byte {
	t.Helper()
	corrupt := bytes.Clone(payload)
	for i := range corrupt {
		corrupt[i] ^= 0x5a
	}
	body := nntpd.Encode(corrupt, "retry.bin", part, total, offset)
	trailer := []byte(fmt.Sprintf("pcrc32=%08x", crc32.ChecksumIEEE(corrupt)))
	if bytes.Count(body, trailer) != 1 {
		t.Fatal("missing unique CRC trailer")
	}
	return bytes.Replace(body, trailer, fmt.Appendf(nil, "pcrc32=%08x", crc32.ChecksumIEEE(payload)), 1)
}

func TestPipelineRetryPreservesAcceptedBuffers(t *testing.T) {
	const size = 64 << 10
	for _, mode := range []string{"corrupt-redundant-copy", "adopt-rejected-primary", "pending-crc-error"} {
		t.Run(mode, func(t *testing.T) {
			primary, err := nntpd.New(nntpd.Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(primary.Close)
			backup, err := nntpd.New(nntpd.Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(backup.Close)
			dataStart := 0
			if mode == "adopt-rejected-primary" {
				dataStart = size / 2
			}
			segments := pipelineIntegritySegments(size, dataStart)
			first := nntpd.Pattern(0, size+dataStart)
			second := nntpd.Pattern(int64(len(first)), size)
			total := int64(len(first) + len(second))
			primaryFirst := first
			if dataStart > 0 {
				primaryFirst = first[:dataStart/2]
			}
			primary.AddArticle(segments[0].MessageID, nntpd.Encode(primaryFirst, "retry.bin", 1, total, 0))
			backupFirst := nntpd.Encode(first, "retry.bin", 1, total, 0)
			if dataStart == 0 {
				backupFirst = corruptPipelineIntegrityBody(t, first, 1, total, 0)
			}
			backup.AddArticle(segments[0].MessageID, backupFirst)
			backupSecond := nntpd.Encode(second, "retry.bin", 2, total, int64(len(first)))
			if mode == "pending-crc-error" {
				backupSecond = corruptPipelineIntegrityBody(t, second, 2, total, int64(len(first)))
			}
			backup.AddArticle(segments[1].MessageID, backupSecond)
			primaryHost, primaryPort := primary.Addr()
			backupHost, backupPort := backup.Addr()
			sr := newPipelineIntegrityReader(t, []config.UsenetProvider{
				{Host: primaryHost, Port: primaryPort, Backbone: "integrity-primary", Priority: 1, MaxConnections: 1},
				{Host: backupHost, Port: backupPort, Backbone: "integrity-backup", Priority: 2, Backup: true, MaxConnections: 1},
			}, segments)
			ctx, cancel := context.WithTimeout(sr.ctx, 5*time.Second)
			defer cancel()
			fetchErr := sr.fetcher.fetchPrefetchBatch(ctx, []int{0, 1})
			if mode == "pending-crc-error" {
				if !errors.Is(fetchErr, nntpyenc.ErrCrcMismatch) || !errors.Is(fetchErr, nntp.ErrAllProvidersFailed) || !strings.Contains(fetchErr.Error(), "article 2/2:") {
					t.Fatalf("pending error lost identity or index: %v", fetchErr)
				}
			} else if fetchErr != nil {
				t.Fatalf("recovery: %v", fetchErr)
			}
			for i, want := range [][]byte{first[dataStart:], second} {
				dst := make([]byte, size)
				n, present := sr.cache.ReadRangeInto(i, 0, size, dst)
				if i == 1 && mode == "pending-crc-error" {
					if present || n != 0 || sr.cache.GetState(i) != StateFailed {
						t.Errorf("failed slot published: state=%s, n=%d, present=%t", sr.cache.GetState(i), n, present)
					}
				} else if !present || n != size || !bytes.Equal(dst, want) {
					t.Errorf("slot %d: state=%s, n=%d, present=%t, exact=%t", i, sr.cache.GetState(i), n, present, bytes.Equal(dst, want))
				}
			}
			if err := sr.Close(); err != nil {
				t.Fatal(err)
			}
			primary.Close()
			backup.Close()
			wantBackup := int64(1)
			if dataStart > 0 {
				wantBackup = 2
			}
			if primary.CompletedBodies.Load() != 1 || backup.CompletedBodies.Load() != wantBackup {
				t.Errorf("completed BODYs: primary=%d, backup=%d, want 1/%d", primary.CompletedBodies.Load(), backup.CompletedBodies.Load(), wantBackup)
			}
		})
	}
}

func TestPipelineAcceptedBufferRemainsPrivateDuringRecovery(t *testing.T) {
	const size = 64 << 10
	for _, action := range []string{"disconnect", "cancel", "reader-close", "idle"} {
		t.Run(action, func(t *testing.T) {
			primary, err := nntpd.New(nntpd.Config{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(primary.Close)
			segments := pipelineIntegritySegments(size, 0)
			first, second := nntpd.Pattern(0, size), nntpd.Pattern(size, size)
			primary.AddArticle(segments[0].MessageID, nntpd.Encode(first, "retry.bin", 1, 2*size, 0))
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			gate, arrived := make(chan struct{}), make(chan string, 1)
			release := sync.OnceFunc(func() { close(gate) })
			defer release()
			serverDone := make(chan error, 1)
			var servers sync.WaitGroup
			servers.Go(func() {
				serverDone <- func() error {
					conn, err := listener.Accept()
					if err != nil {
						return err
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					if _, err := io.WriteString(conn, "200 pipeline recovery test\r\n"); err != nil {
						return err
					}
					reader := bufio.NewReader(conn)
					for {
						line, err := reader.ReadString('\n')
						if err != nil {
							return err
						}
						if line == "DATE\r\n" {
							if _, err := io.WriteString(conn, "111 20260905220000\r\n"); err != nil {
								return err
							}
							continue
						}
						arrived <- line
						<-gate
						if action == "disconnect" {
							return nil
						}
						if action == "idle" {
							_, err := fmt.Fprintf(conn, "222 0 %s body\r\n%s.\r\n", segments[1].MessageID, nntpd.Encode(second, "retry.bin", 2, 2*size, size))
							return err
						}
						_, err = reader.ReadByte()
						if err == nil {
							return errors.New("canceled client sent an unexpected byte")
						}
						return nil
					}
				}()
			})
			t.Cleanup(func() { release(); _ = listener.Close(); servers.Wait() })
			primaryHost, primaryPort := primary.Addr()
			sr := newPipelineIntegrityReader(t, []config.UsenetProvider{
				{Host: primaryHost, Port: primaryPort, Backbone: "staging-primary", Priority: 1, MaxConnections: 1},
				{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, Backbone: "staging-backup", Priority: 2, Backup: true, MaxConnections: 1},
			}, segments)
			ctx, cancel := context.WithTimeout(sr.ctx, 5*time.Second)
			defer cancel()
			finished := make(chan error, 1)
			if !sr.fetcher.submit(ctx, priorityPrefetch, func() { finished <- sr.fetcher.fetchPrefetchBatch(ctx, []int{0, 1}) }, nil) {
				t.Fatal("submission failed")
			}
			select {
			case line := <-arrived:
				if line != "BODY "+segments[1].MessageID+"\r\n" {
					t.Fatalf("backup command = %q", line)
				}
			case err := <-finished:
				t.Fatalf("fetch ended before recovery gate: %v", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			dst := make([]byte, size)
			if n, present := sr.cache.ReadRangeInto(0, 0, size, dst); n != 0 || present || sr.cache.GetState(0) != StateFetching || sr.cache.residentN.Load() != 0 || sr.cache.extentPool.inUse.Load() != 0 {
				t.Fatalf("accepted bytes published before batch end: n=%d, present=%t, state=%s", n, present, sr.cache.GetState(0))
			}
			switch action {
			case "cancel":
				cancel()
			case "reader-close":
				if err := sr.Close(); err != nil {
					t.Fatal(err)
				}
			case "idle":
				sr.cache.ReleaseIdleDelivery()
			}
			release()
			var fetchErr error
			select {
			case fetchErr = <-finished:
			case <-time.After(5 * time.Second):
				t.Fatal("fetch did not join")
			}
			if action == "idle" {
				if fetchErr != nil || sr.cache.residentN.Load() != 0 || sr.cache.extentPool.inUse.Load() != 0 {
					t.Fatalf("idle staging retained ownership: error=%v, resident=%d", fetchErr, sr.cache.residentN.Load())
				}
			} else if action == "disconnect" {
				if typed, ok := errors.AsType[*nntp.Error](fetchErr); !ok || typed.Type != nntp.ErrorTypeConnection || !errors.Is(fetchErr, nntp.ErrAllProvidersFailed) || !strings.Contains(fetchErr.Error(), "article 2/2:") {
					t.Fatalf("pending connection failure lost identity or index: %v", fetchErr)
				}
			} else if !errors.Is(fetchErr, context.Canceled) {
				t.Fatalf("cancellation identity = %v", fetchErr)
			}
			if action == "cancel" || action == "disconnect" {
				if n, present := sr.cache.ReadRangeInto(0, 0, size, dst); !present || n != size || !bytes.Equal(dst, first) {
					t.Fatalf("accepted peer lost after cancellation: n=%d, present=%t", n, present)
				}
			}
			if err := <-serverDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}
