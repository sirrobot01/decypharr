package reader

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appconfig "github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
)

func TestStreamingReaderPrefetchesOutOfOrderAndReadsInOrder(t *testing.T) {
	const (
		segmentCount   = 8
		segmentSize    = 32 * 1024
		readSize       = 4 * 1024
		maxConnections = 4
	)

	// The shared buffer pool resolves its memory budget through config.Get on
	// first use. Isolate that lazy initialization from the repository and the
	// caller's real configuration.
	appconfig.SetConfigPath(t.TempDir())

	payloads := make([][]byte, segmentCount)
	segments := make([]SegmentMeta, segmentCount)
	var expected bytes.Buffer
	for i := range segmentCount {
		payload := make([]byte, segmentSize)
		for j := range payload {
			payload[j] = byte((i*37 + j) % 251)
		}
		payloads[i] = payload
		expected.Write(payload)

		start := int64(i * segmentSize)
		segments[i] = SegmentMeta{
			MessageID:   fmt.Sprintf("<segment-%02d@test>", i),
			Number:      i + 1,
			Bytes:       segmentSize,
			StartOffset: start,
			EndOffset:   start + segmentSize - 1,
		}
	}

	// Keep the protocol portion deterministic across CGO and non-CGO builds.
	oldPureGo := nntpyenc.UsePureGo
	nntpyenc.UsePureGo = true
	t.Cleanup(func() { nntpyenc.UsePureGo = oldPureGo })

	server := newPipelineNNTPServer(t, payloads)
	t.Cleanup(server.Close)

	host, port := server.address(t)
	client, err := nntp.NewClient(&appconfig.Config{
		Retries: 1,
		Usenet: appconfig.Usenet{Providers: []appconfig.UsenetProvider{
			{
				Host:           host,
				Port:           port,
				MaxConnections: maxConnections,
			},
		}},
	})
	if err != nil {
		t.Fatalf("create NNTP client: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)

	stream, err := NewStreamingReader(
		ctx,
		client,
		segments,
		WithMaxConnections(maxConnections),
		WithPrefetchAhead(segmentCount-1),
		WithDownloadTimeout(10*time.Second),
		WithDiskPath(t.TempDir()),
	)
	if err != nil {
		t.Fatalf("create streaming reader: %v", err)
	}
	t.Cleanup(func() { _ = stream.Close() })
	// This runs before reader/client cleanup and prevents a failed assertion
	// from leaving a fake BODY response gated during teardown.
	t.Cleanup(server.releaseAll)

	type readResult struct {
		data []byte
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data, readErr := readSequentially(ctx, stream, readSize)
		readDone <- readResult{data: data, err: readErr}
	}()

	// Segment zero is the blocking foreground read. The three prefetch
	// workers must concurrently request segments 1-3 before any response is
	// released. Gates make this an event assertion rather than a timing test.
	started := make(map[int]bool, maxConnections)
	for len(started) < maxConnections {
		select {
		case idx := <-server.started:
			started[idx] = true
		case <-ctx.Done():
			t.Fatalf("waiting for concurrent BODY requests: %v (started=%v)", ctx.Err(), started)
		}
	}
	for i := range maxConnections {
		if !started[i] {
			t.Fatalf("initial concurrent requests = %v, want segments 0-%d", started, maxConnections-1)
		}
	}
	if peak := server.peakActive(); peak != maxConnections {
		t.Fatalf("peak concurrent BODY requests = %d, want %d", peak, maxConnections)
	}

	// Let future segments complete while the true head remains blocked.
	server.releaseFuture()
	for i := 1; i < maxConnections; i++ {
		if err := stream.cache.WaitForSegment(ctx, i); err != nil {
			t.Fatalf("future segment %d did not complete ahead of head: %v", i, err)
		}
	}
	if state := stream.cache.GetState(0); state != StateFetching {
		t.Fatalf("head state = %s, want %s while future segments are cached", state, StateFetching)
	}
	if completed := server.completionOrder(); containsInt(completed, 0) {
		t.Fatalf("head completed before its gate was released: completion order %v", completed)
	}
	select {
	case result := <-readDone:
		t.Fatalf("ordered read completed while head was blocked: bytes=%d err=%v", len(result.data), result.err)
	default:
	}

	server.releaseHead()

	var result readResult
	select {
	case result = <-readDone:
	case <-ctx.Done():
		t.Fatalf("waiting for ordered read: %v", ctx.Err())
	}
	if result.err != nil {
		t.Fatalf("ordered read failed: %v", result.err)
	}
	if !bytes.Equal(result.data, expected.Bytes()) {
		t.Fatalf("ordered output mismatch: got %d bytes, want %d", len(result.data), expected.Len())
	}

	order := server.completionOrder()
	if len(order) != segmentCount {
		t.Fatalf("completion order has %d segments, want %d: %v", len(order), segmentCount, order)
	}
	if order[0] == 0 {
		t.Fatalf("segments did not complete out of order: %v", order)
	}
	for i := range segmentCount {
		if count := server.requestCount(i); count != 1 {
			t.Fatalf("segment %d BODY request count = %d, want 1", i, count)
		}
	}
}

func readSequentially(ctx context.Context, stream *StreamingReader, readSize int) ([]byte, error) {
	var out bytes.Buffer
	buf := make([]byte, readSize)
	var off int64
	for {
		n, err := stream.ReadAtContext(ctx, buf, off)
		if n > 0 {
			_, _ = out.Write(buf[:n])
			off += int64(n)
		}
		if errors.Is(err, io.EOF) {
			return out.Bytes(), nil
		}
		if err != nil {
			return out.Bytes(), err
		}
		if n == 0 {
			return out.Bytes(), io.ErrNoProgress
		}
	}
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

type pipelineNNTPServer struct {
	listener net.Listener
	payloads [][]byte

	started chan int
	stop    chan struct{}

	headGate   chan struct{}
	futureGate chan struct{}
	headOnce   sync.Once
	futureOnce sync.Once
	closeOnce  sync.Once

	mu          sync.Mutex
	connections map[net.Conn]struct{}
	active      int
	peak        int
	requests    map[int]int
	completed   []int

	wg sync.WaitGroup
}

func newPipelineNNTPServer(t *testing.T, payloads [][]byte) *pipelineNNTPServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for fake NNTP server: %v", err)
	}

	server := &pipelineNNTPServer{
		listener:    listener,
		payloads:    payloads,
		started:     make(chan int, len(payloads)*2),
		stop:        make(chan struct{}),
		headGate:    make(chan struct{}),
		futureGate:  make(chan struct{}),
		connections: make(map[net.Conn]struct{}),
		requests:    make(map[int]int),
		completed:   make([]int, 0, len(payloads)),
	}

	server.wg.Add(1)
	go server.acceptLoop()
	return server
}

func (s *pipelineNNTPServer) address(t *testing.T) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(s.listener.Addr().String())
	if err != nil {
		t.Fatalf("parse fake NNTP address: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse fake NNTP port: %v", err)
	}
	return host, port
}

func (s *pipelineNNTPServer) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.connections[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.serveConnection(conn)
	}
}

func (s *pipelineNNTPServer) serveConnection(conn net.Conn) {
	defer s.wg.Done()
	defer func() {
		s.mu.Lock()
		delete(s.connections, conn)
		s.mu.Unlock()
		_ = conn.Close()
	}()

	writer := bufio.NewWriter(conn)
	if _, err := writer.WriteString("200 pipeline test server ready\r\n"); err != nil {
		return
	}
	if err := writer.Flush(); err != nil {
		return
	}

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 0 {
			continue
		}

		switch strings.ToUpper(fields[0]) {
		case "BODY":
			if len(fields) != 2 {
				_, _ = writer.WriteString("501 message-id required\r\n")
				_ = writer.Flush()
				continue
			}
			idx, err := pipelineSegmentIndex(fields[1])
			if err != nil || idx < 0 || idx >= len(s.payloads) {
				_, _ = writer.WriteString("430 no such article\r\n")
				_ = writer.Flush()
				continue
			}
			if !s.serveBody(writer, idx) {
				return
			}
		case "DATE":
			_, _ = writer.WriteString("111 20260718000000\r\n")
			if err := writer.Flush(); err != nil {
				return
			}
		case "QUIT":
			_, _ = writer.WriteString("205 closing connection\r\n")
			_ = writer.Flush()
			return
		default:
			_, _ = writer.WriteString("500 unsupported command\r\n")
			if err := writer.Flush(); err != nil {
				return
			}
		}
	}
}

func (s *pipelineNNTPServer) serveBody(writer *bufio.Writer, idx int) bool {
	s.mu.Lock()
	s.active++
	if s.active > s.peak {
		s.peak = s.active
	}
	s.requests[idx]++
	s.mu.Unlock()

	select {
	case s.started <- idx:
	case <-s.stop:
		s.finishBody(idx, false)
		return false
	}

	gate := s.futureGate
	if idx == 0 {
		gate = s.headGate
	}
	select {
	case <-gate:
	case <-s.stop:
		s.finishBody(idx, false)
		return false
	}

	if _, err := fmt.Fprintf(writer, "222 0 <%s>\r\n", pipelineMessageID(idx)); err != nil {
		s.finishBody(idx, false)
		return false
	}
	if _, err := writer.WriteString(pipelineYEnc(s.payloads[idx], fmt.Sprintf("segment-%02d.bin", idx))); err != nil {
		s.finishBody(idx, false)
		return false
	}
	if _, err := writer.WriteString(".\r\n"); err != nil {
		s.finishBody(idx, false)
		return false
	}
	if err := writer.Flush(); err != nil {
		s.finishBody(idx, false)
		return false
	}

	s.finishBody(idx, true)
	return true
}

func (s *pipelineNNTPServer) finishBody(idx int, completed bool) {
	s.mu.Lock()
	s.active--
	if completed {
		s.completed = append(s.completed, idx)
	}
	s.mu.Unlock()
}

func (s *pipelineNNTPServer) peakActive() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peak
}

func (s *pipelineNNTPServer) completionOrder() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.completed...)
}

func (s *pipelineNNTPServer) requestCount(idx int) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[idx]
}

func (s *pipelineNNTPServer) releaseHead() {
	s.headOnce.Do(func() { close(s.headGate) })
}

func (s *pipelineNNTPServer) releaseFuture() {
	s.futureOnce.Do(func() { close(s.futureGate) })
}

func (s *pipelineNNTPServer) releaseAll() {
	s.releaseHead()
	s.releaseFuture()
}

func (s *pipelineNNTPServer) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.releaseAll()
		_ = s.listener.Close()

		s.mu.Lock()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.mu.Unlock()
		s.wg.Wait()
	})
}

func pipelineSegmentIndex(messageID string) (int, error) {
	messageID = strings.Trim(messageID, "<>")
	const (
		prefix = "segment-"
		suffix = "@test"
	)
	if !strings.HasPrefix(messageID, prefix) || !strings.HasSuffix(messageID, suffix) {
		return 0, fmt.Errorf("unexpected message-id %q", messageID)
	}
	return strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(messageID, prefix), suffix))
}

func pipelineMessageID(idx int) string {
	return fmt.Sprintf("segment-%02d@test", idx)
}

func pipelineYEnc(data []byte, name string) string {
	var encoded bytes.Buffer
	_, _ = fmt.Fprintf(&encoded, "=ybegin line=128 size=%d name=%s\r\n", len(data), name)

	column := 0
	for _, value := range data {
		value += 42
		if value == 0 || value == '\n' || value == '\r' || value == '=' || value == '\t' || value == ' ' || value == '.' {
			encoded.WriteByte('=')
			encoded.WriteByte(value + 64)
			column += 2
		} else {
			encoded.WriteByte(value)
			column++
		}
		if column >= 128 {
			encoded.WriteString("\r\n")
			column = 0
		}
	}
	if column > 0 {
		encoded.WriteString("\r\n")
	}
	_, _ = fmt.Fprintf(&encoded, "=yend size=%d\r\n", len(data))
	return encoded.String()
}
