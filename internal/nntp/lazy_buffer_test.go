package nntp

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"testing"
	"unsafe"

	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
)

// countingBuffer records how often the decoder demanded storage and returns
// the same backing array every time, as BodyBuffer requires.
type countingBuffer struct {
	calls int
	buf   []byte
	niled bool
}

func (b *countingBuffer) DecodeBuffer() []byte {
	b.calls++
	if b.niled {
		return nil
	}
	if b.buf == nil {
		b.buf = make([]byte, 0, DecodedBodyCapacity(1<<16))
	}
	return b.buf
}

func backing(b []byte) uintptr {
	if cap(b) == 0 {
		return 0
	}
	return uintptr(unsafe.Pointer(unsafe.SliceData(b[:cap(b)])))
}

// TestDecodeBodyWithBufferDefersAllocation is the core Round 16 claim: no
// decoded storage is demanded until the decoder actually has yEnc body input.
func TestDecodeBodyWithBufferDefersAllocation(t *testing.T) {
	payload := testPayload(48 * 1024)
	for _, tc := range []struct {
		name      string
		response  string
		wantCalls int
		wantData  bool
	}{
		{"negative status", "430 no such article\r\n", 0, false},
		{"empty body", "222 0 <a@b> body\r\n.\r\n", 0, false},
		{"headers without yenc data", "222 0 <a@b> body\r\nSubject: none\r\n\r\n.\r\n", 0, false},
		{"valid yenc body", "222 0 <a@b> body\r\n" + encodeBody(payload) + ".\r\n", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, server := newBodyTestConn(t)
			serveResponses(t, server, tc.response)
			source := &countingBuffer{}

			data, err := c.DecodeBodyWithBuffer("<a@b>", source)

			if source.calls != tc.wantCalls {
				t.Errorf("DecodeBuffer calls = %d, want %d", source.calls, tc.wantCalls)
			}
			if tc.wantData {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !bytes.Equal(data, payload) {
					t.Errorf("decoded %d bytes, want the exact payload", len(data))
				}
				if backing(data) != backing(source.buf) {
					t.Error("decoded result does not use the supplied caller storage")
				}
			} else if err == nil {
				t.Fatal("expected an error for a response that yields no body")
			}
			if c.bodySource != nil || c.bodyTargetSet {
				t.Error("connection retained body source or target state after the response")
			}
		})
	}
}

// TestDecodeBodyWithBufferMatchesExplicitBuffer pins the demand path to the
// existing explicit-buffer path on identical fixtures, including errors.
func TestDecodeBodyWithBufferMatchesExplicitBuffer(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{"valid", "222 0 <a@b> body\r\n" + encodeBody(testPayload(32*1024)) + ".\r\n"},
		{"missing", "430 no such article\r\n"},
		{"empty", "222 0 <a@b> body\r\n.\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eager, eagerServer := newBodyTestConn(t)
			serveResponses(t, eagerServer, tc.response)
			wantData, wantErr := eager.DecodeBodyInto("<a@b>", make([]byte, 0, DecodedBodyCapacity(1<<16)))

			lazy, lazyServer := newBodyTestConn(t)
			serveResponses(t, lazyServer, tc.response)
			gotData, gotErr := lazy.DecodeBodyWithBuffer("<a@b>", &countingBuffer{})

			if !bytes.Equal(gotData, wantData) {
				t.Errorf("data = %d bytes, want %d", len(gotData), len(wantData))
			}
			switch {
			case wantErr == nil && gotErr != nil:
				t.Fatalf("demand path failed where explicit buffer succeeded: %v", gotErr)
			case wantErr != nil && gotErr == nil:
				t.Fatal("demand path succeeded where explicit buffer failed")
			case wantErr != nil:
				var want, got *Error
				if errors.As(wantErr, &want) != errors.As(gotErr, &got) {
					t.Fatalf("error identity differs: %v vs %v", wantErr, gotErr)
				}
				if want != nil && got != nil && want.Type != got.Type {
					t.Errorf("error type = %v, want %v", got.Type, want.Type)
				}
			}
		})
	}
}

// TestDecodeBodyWithBufferNilSourceStaysCallerOwned checks the degenerate
// case: a source supplying nothing must not hand back pooled scratch.
func TestDecodeBodyWithBufferNilSourceStaysCallerOwned(t *testing.T) {
	payload := testPayload(8 * 1024)
	c, server := newBodyTestConn(t)
	serveResponses(t, server, "222 0 <a@b> body\r\n"+encodeBody(payload)+".\r\n")

	data, err := c.DecodeBodyWithBuffer("<a@b>", &countingBuffer{niled: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(data, payload) {
		t.Errorf("decoded %d bytes, want the exact payload", len(data))
	}
	pooled := getBodyBuf()
	defer putBodyBuf(pooled)
	if cap(data) != 0 && backing(data) == backing(pooled) {
		t.Error("decoded result aliases connection-pooled storage")
	}
}

// TestConnectionReusableAfterDemandRead checks the source is cleared so an
// ordinary pooled read on the same connection is unaffected.
func TestConnectionReusableAfterDemandRead(t *testing.T) {
	first, second := testPayload(4*1024), testPayload(6*1024)
	c, server := newBodyTestConn(t)
	serveResponses(t, server,
		"222 0 <a@b> body\r\n"+encodeBody(first)+".\r\n",
		"222 0 <c@d> body\r\n"+encodeBody(second)+".\r\n")

	source := &countingBuffer{}
	if _, err := c.DecodeBodyWithBuffer("<a@b>", source); err != nil {
		t.Fatalf("demand read: %v", err)
	}
	got, err := c.GetDecodedBody("<c@d>")
	if err != nil {
		t.Fatalf("pooled read after demand read: %v", err)
	}
	if !bytes.Equal(got, second) {
		t.Errorf("pooled read returned %d bytes, want %d", len(got), len(second))
	}
	if source.calls != 1 {
		t.Errorf("DecodeBuffer calls = %d, want 1; the source must not survive its response", source.calls)
	}
}

// TestPipelineBodiesUsesBufferSource covers the batch path, including that a
// skipped slot never demands storage.
func TestPipelineBodiesUsesBufferSource(t *testing.T) {
	first, second := testPayload(16*1024), testPayload(20*1024)
	c, server := newBodyTestConn(t)
	ids := []string{"<one@x>", "<two@x>", "<three@x>"}
	sources := []*countingBuffer{{}, {}, {}}
	destinations := []BodyDestination{
		{BufferSource: sources[0]},
		{BufferSource: sources[1], Skip: true},
		{BufferSource: sources[2]},
	}
	done := pipelineSkipServer(t, c, server, func(reader *bufio.Reader) error {
		if err := readPipelineSkipCommands(reader, []string{ids[0], ids[2]}); err != nil {
			return err
		}
		_, err := server.Write([]byte("222 0 <one@x> body\r\n" + encodeBody(first) + ".\r\n" +
			"222 0 <three@x> body\r\n" + encodeBody(second) + ".\r\n"))
		return err
	})

	results, err := c.PipelineBodies(ids, destinations)
	if err != nil {
		t.Fatalf("PipelineBodies: %v", err)
	}
	if serverErr := <-done; serverErr != nil {
		t.Fatalf("server: %v", serverErr)
	}
	if len(results) != len(ids) {
		t.Fatalf("results = %d, want %d", len(results), len(ids))
	}
	if !bytes.Equal(results[0].Body, first) || !bytes.Equal(results[2].Body, second) {
		t.Error("pipeline did not return the exact payloads through the demand sources")
	}
	if len(results[1].Body) != 0 {
		t.Error("skipped slot produced a body")
	}
	if sources[0].calls != 1 || sources[2].calls != 1 {
		t.Errorf("DecodeBuffer calls = %d/%d, want 1 each", sources[0].calls, sources[2].calls)
	}
	if sources[1].calls != 0 {
		t.Errorf("skipped slot demanded storage %d times", sources[1].calls)
	}
	if backing(results[0].Body) != backing(sources[0].buf) {
		t.Error("pipeline result does not use the supplied caller storage")
	}
}

// TestBufferSourceMemoizesAcrossRetries pins the memoization contract: a
// second demand for the same destination reuses one backing array.
func TestBufferSourceMemoizesAcrossRetries(t *testing.T) {
	source := &countingBuffer{}
	first := source.DecodeBuffer()
	second := source.DecodeBuffer()
	if backing(first) != backing(second) {
		t.Error("source returned different backing arrays for one destination")
	}
	if source.calls != 2 {
		t.Errorf("DecodeBuffer calls = %d, want 2", source.calls)
	}
}

// BenchmarkPredecodeAllocation measures the allocation the two paths perform
// for a response that never reaches yEnc decoding. The eager case is exactly
// what the original fetcher did: allocate the decode buffer before sending.
func BenchmarkPredecodeAllocation(b *testing.B) {
	const capacity = 1 << 20
	b.Run("eager", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			c, server := newBodyBenchConn(b)
			serveBenchResponse(server, "430 no such article\r\n")
			_, _ = c.DecodeBodyInto("<a@b>", make([]byte, 0, DecodedBodyCapacity(capacity)))
			_ = c.conn.Close()
			_ = server.Close()
		}
	})
	b.Run("demand", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			c, server := newBodyBenchConn(b)
			serveBenchResponse(server, "430 no such article\r\n")
			_, _ = c.DecodeBodyWithBuffer("<a@b>", &benchBuffer{capacity: capacity})
			_ = c.conn.Close()
			_ = server.Close()
		}
	})
}

type benchBuffer struct {
	capacity int
	buf      []byte
}

func (b *benchBuffer) DecodeBuffer() []byte {
	if b.buf == nil {
		b.buf = make([]byte, 0, DecodedBodyCapacity(int64(b.capacity)))
	}
	return b.buf
}

func newBodyBenchConn(b *testing.B) (*Connection, net.Conn) {
	client, server := net.Pipe()
	c := &Connection{
		conn:   client,
		reader: bufio.NewReaderSize(client, 128*1024),
		writer: bufio.NewWriterSize(client, 4*1024),
	}
	c.bodyDec = nntpyenc.NewBodyDecoder(&bodyReader{c: c}, c.nextBodyBuffer)
	return c, server
}

func serveBenchResponse(server net.Conn, response string) {
	go func() {
		reader := bufio.NewReader(server)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		_, _ = server.Write([]byte(response))
	}()
}
