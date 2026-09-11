package nntp

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
	"testing"

	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
)

// testPayload returns bytes from 'A'..'Z' cycling; every encoded byte
// (b+42) lands outside yEnc's escape set, so the wire form needs no escapes.
func testPayload(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = 'A' + byte(i%26)
	}
	return p
}

// encodeBody produces the yEnc article body for a payload from testPayload,
// with a correct pcrc32 so decodes exercise CRC verification.
func encodeBody(payload []byte) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "=ybegin part=1 line=128 size=%d name=test.bin\r\n", len(payload))
	fmt.Fprintf(&buf, "=ypart begin=1 end=%d\r\n", len(payload))
	for i := 0; i < len(payload); i += 128 {
		for _, b := range payload[i:min(i+128, len(payload))] {
			buf.WriteByte(b + 42)
		}
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "=yend size=%d pcrc32=%08x\r\n", len(payload), crc32.ChecksumIEEE(payload))
	return buf.String()
}

func newBodyTestConn(t *testing.T) (*Connection, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	c := &Connection{
		conn:   client,
		reader: bufio.NewReaderSize(client, 128*1024),
		writer: bufio.NewWriterSize(client, 4*1024),
	}
	c.bodyDec = nntpyenc.NewBodyDecoder(&bodyReader{c: c}, c.nextBodyBuffer)
	return c, server
}

func TestDecodeBodyIntoUsesCallerStorage(t *testing.T) {
	c, server := newBodyTestConn(t)
	payload := testPayload(64 * 1024)
	serveResponses(t, server, "222 0 <a@b> body\r\n"+encodeBody(payload)+".\r\n")

	dst := make([]byte, 0, DecodedBodyCapacity(int64(len(payload))))
	got, err := c.DecodeBodyInto("<a@b>", dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("payload corrupted")
	}
	if &got[0] != &dst[:1][0] {
		t.Fatal("decoder replaced caller storage")
	}
}

// serveResponses answers each incoming command line with the next canned
// response, then keeps the pipe open.
func serveResponses(t *testing.T, server net.Conn, responses ...string) {
	t.Helper()
	go func() {
		reader := bufio.NewReader(server)
		for _, resp := range responses {
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
			if _, err := server.Write([]byte(resp)); err != nil {
				return
			}
		}
	}()
}

func TestRequestBodyDecodesAndReusesConnection(t *testing.T) {
	c, server := newBodyTestConn(t)

	first := testPayload(300 * 1024)
	second := testPayload(64 * 1024)
	serveResponses(t, server,
		"222 0 <a@b> body\r\n"+encodeBody(first)+".\r\n",
		"222 0 <c@d> body\r\n"+encodeBody(second)+".\r\n",
	)

	for i, want := range [][]byte{first, second} {
		res, err := c.requestBody("<x@y>")
		if err != nil {
			t.Fatalf("requestBody %d: %v", i+1, err)
		}
		if !bytes.Equal(res.Data, want) {
			t.Fatalf("article %d: got %d bytes, want %d", i+1, len(res.Data), len(want))
		}
		if res.Meta.FileName != "test.bin" {
			t.Errorf("article %d FileName = %q", i+1, res.Meta.FileName)
		}
		if res.Meta.PartSize != int64(len(want)) {
			t.Errorf("article %d PartSize = %d, want %d", i+1, res.Meta.PartSize, len(want))
		}
		putBodyBuf(res.Data)
	}
}

func TestRequestBodyStatusNotFound(t *testing.T) {
	c, server := newBodyTestConn(t)
	serveResponses(t, server, "430 no such article\r\n")

	_, err := c.requestBody("<gone@b>")
	var nntpErr *Error
	if !errors.As(err, &nntpErr) || nntpErr.Type != ErrorTypeArticleNotFound {
		t.Fatalf("err = %v, want ErrorTypeArticleNotFound", err)
	}
}

func TestRequestBodyCrcMismatch(t *testing.T) {
	c, server := newBodyTestConn(t)

	payload := testPayload(4 * 1024)
	body := encodeBody(payload)
	good := fmt.Sprintf("pcrc32=%08x", crc32.ChecksumIEEE(payload))
	serveResponses(t, server, "222 0 <a@b> body\r\n"+strings.Replace(body, good, "pcrc32=deadbeef", 1)+".\r\n")

	_, err := c.requestBody("<a@b>")
	if !errors.Is(err, nntpyenc.ErrCrcMismatch) {
		t.Fatalf("err = %v, want ErrCrcMismatch", err)
	}
	var nntpErr *Error
	if !errors.As(err, &nntpErr) || nntpErr.Type != ErrorTypeYencDecode {
		t.Fatalf("err = %v, want ErrorTypeYencDecode", err)
	}
}

func TestRequestBodyNonYencBody(t *testing.T) {
	c, server := newBodyTestConn(t)
	serveResponses(t, server, "222 0 <a@b> body\r\nplain text, no yEnc here\r\n.\r\n")

	_, err := c.requestBody("<a@b>")
	var nntpErr *Error
	if !errors.As(err, &nntpErr) || nntpErr.Type != ErrorTypeArticleNotFound {
		t.Fatalf("err = %v, want ErrorTypeArticleNotFound for yEnc-less body", err)
	}
}

func TestRequestBodyMidStreamDisconnect(t *testing.T) {
	c, server := newBodyTestConn(t)

	body := encodeBody(testPayload(64 * 1024))
	go func() {
		reader := bufio.NewReader(server)
		if _, err := reader.ReadString('\n'); err != nil {
			return
		}
		_, _ = server.Write([]byte("222 0 <a@b> body\r\n" + body[:len(body)/2]))
		_ = server.Close()
	}()

	_, err := c.requestBody("<a@b>")
	var nntpErr *Error
	if !errors.As(err, &nntpErr) || nntpErr.Type != ErrorTypeConnection {
		t.Fatalf("err = %v, want ErrorTypeConnection", err)
	}
}

type sizeRecordingWriter struct {
	buf   bytes.Buffer
	sizes []int
}

func (w *sizeRecordingWriter) Write(p []byte) (int, error) {
	w.sizes = append(w.sizes, len(p))
	return w.buf.Write(p)
}

// TestStreamBodySingleWrite pins the write granularity of the streaming
// path: the whole decoded article must reach the segment cache in one Write,
// so a segment costs one pwrite+lock cycle instead of one per decoder read.
func TestStreamBodySingleWrite(t *testing.T) {
	c, server := newBodyTestConn(t)

	payload := testPayload(300 * 1024)
	serveResponses(t, server, "222 0 <a@b> body\r\n"+encodeBody(payload)+".\r\n")

	dst := &sizeRecordingWriter{}
	n, err := c.StreamBody("<a@b>", dst)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(payload)) {
		t.Fatalf("streamed %d bytes, want %d", n, len(payload))
	}
	if !bytes.Equal(dst.buf.Bytes(), payload) {
		t.Fatal("payload corrupted")
	}
	if len(dst.sizes) != 1 {
		t.Fatalf("article written in %d writes, want 1 (sizes: %v)", len(dst.sizes), dst.sizes)
	}
}

func TestGetHeaderPrefixSnippet(t *testing.T) {
	c, server := newBodyTestConn(t)

	payload := testPayload(32 * 1024)
	serveResponses(t, server, "222 0 <a@b> body\r\n"+encodeBody(payload)+".\r\n")

	meta, err := c.GetHeaderPrefix("<a@b>", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(meta.Snippet, payload[:100]) {
		t.Fatal("snippet mismatch")
	}
	if meta.Size != int64(len(payload)) || meta.Begin != 1 || meta.End != int64(len(payload)) {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestGetDecodedBodyWithMetadataDoesNotRetainScratchBuffer(t *testing.T) {
	t.Parallel()

	conn, server := newBodyTestConn(t)
	payload := testPayload(4 * 1024)
	serveResponses(t, server, "222 0 <a@b> body\r\n"+encodeBody(payload)+".\r\n")

	decoded, metadata, err := conn.GetDecodedBodyWithMetadata("<a@b>")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, payload) {
		t.Fatal("payload corrupted")
	}
	if metadata == nil || metadata.PartSize != int64(len(payload)) {
		t.Fatalf("metadata = %+v", metadata)
	}
	if cap(decoded) >= 1<<20 {
		t.Fatalf("4 KiB escaping body retains %d-byte scratch allocation", cap(decoded))
	}
}
