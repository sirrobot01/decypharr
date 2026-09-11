package nntp

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	nntpyenc "github.com/sirrobot01/decypharr/internal/nntp/yenc"
	"github.com/sirrobot01/decypharr/internal/utils"
)

// Note: Timeout values are defined in TimeoutConfig (client.go).
// Use timeouts.StreamBodyTimeout for read deadlines.

// bodyBufPool reuses storage for decoded articles not retained by a caller.
var bodyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 0, 1<<20)
		return &b
	},
}

func getBodyBuf() []byte { return *bodyBufPool.Get().(*[]byte) }

func putBodyBuf(b []byte) {
	if cap(b) == 0 {
		return
	}
	b = b[:0]
	bodyBufPool.Put(&b)
}

// DecodedBodyCapacity matches the decoder's initial growth policy. Supplying
// this capacity lets DecodeBodyInto keep the caller's allocation.
func DecodedBodyCapacity(decodedSize int64) int {
	const chunk = int64(32 * 1024)
	const maxPart = int64(10 * 1024 * 1024)
	if decodedSize < 0 {
		decodedSize = 0
	}
	n := ((decodedSize + 64 + chunk - 1) / chunk * chunk) + chunk
	n = max(n, 1024)
	n = min(n, maxPart)
	return int(n)
}

// bodyReader is the stable reader identity the yEnc decoder holds for the
// life of a connection: it follows c.reader (which is replaced on a STARTTLS
// upgrade) and records read progress for the body idle janitor.
type bodyReader struct {
	c     *Connection
	reads uint8
}

// progressUpdateStride amortizes the monotonic-clock update across body reads.
const progressUpdateStride = 4

func (b *bodyReader) Read(p []byte) (int, error) {
	n, err := b.c.reader.Read(p)
	if n > 0 {
		b.reads++
		if b.reads >= progressUpdateStride {
			b.c.lastProgressNS.Store(nanotimeNow())
			b.reads = 0
		}
	}
	return n, err
}

// nextBodyWithIdleDeadline uses the shared janitor to break a stalled decode.
func (c *Connection) nextBodyWithIdleDeadline(idle time.Duration) (nntpyenc.BodyResult, error) {
	if idle <= 0 {
		idle = 60 * time.Second
	}
	// Disable any deadline carried in from earlier on this connection.
	_ = c.conn.SetReadDeadline(time.Time{})

	// Arm the janitor for this decode; the connection itself is registered
	// for its whole lifetime (see createConnection/Close). idleNS=0 on exit
	// disarms.
	c.lastProgressNS.Store(nanotimeNow())
	c.idleNS.Store(int64(idle))
	defer c.idleNS.Store(0)

	res, err := c.bodyDec.Next()
	if err != nil {
		// The janitor sets idleNS to 0 after closing a stalled conn, but
		// the race-free signal is "did we make progress within the
		// deadline?". If not, format as a stall error.
		if nanotimeNow()-c.lastProgressNS.Load() > int64(idle) {
			return res, fmt.Errorf("stream idle for %s: %w", idle, err)
		}
	}
	return res, err
}

// nanotimeNow returns the monotonic clock in nanoseconds. Uses time.Now's
// monotonic reading via Sub(zero): one runtime.nanotime call, no wall-clock
// overhead, no allocation.
var nanotimeEpoch = time.Now()

func nanotimeNow() int64 {
	return int64(time.Since(nanotimeEpoch))
}

// bodyIdleJanitor sweeps connections currently in nextBodyWithIdleDeadline
// and closes any whose last-progress timestamp is older than their idle
// deadline. One goroutine per process, started lazily on first add().
var bodyIdleJanitor = newBodyJanitor()

const bodyJanitorInterval = 5 * time.Second

type bodyJanitor struct {
	mu      sync.Mutex
	conns   map[*Connection]struct{}
	started atomic.Bool
}

func newBodyJanitor() *bodyJanitor {
	return &bodyJanitor{conns: make(map[*Connection]struct{})}
}

func (j *bodyJanitor) ensureRunning() {
	if !j.started.CompareAndSwap(false, true) {
		return
	}
	go j.run()
}

func (j *bodyJanitor) add(c *Connection) {
	j.ensureRunning()
	j.mu.Lock()
	j.conns[c] = struct{}{}
	j.mu.Unlock()
}

func (j *bodyJanitor) remove(c *Connection) {
	j.mu.Lock()
	delete(j.conns, c)
	j.mu.Unlock()
}

func (j *bodyJanitor) run() {
	tick := time.NewTicker(bodyJanitorInterval)
	defer tick.Stop()
	for range tick.C {
		j.sweep()
	}
}

// sweep closes every registered connection whose armed body copy has made no
// progress within its idle deadline. Snapshot under the lock and act outside
// it so a slow Close() can't hold up other registrations.
func (j *bodyJanitor) sweep() {
	now := nanotimeNow()
	var stalled []*Connection
	j.mu.Lock()
	for c := range j.conns {
		idle := c.idleNS.Load()
		if idle <= 0 {
			continue
		}
		if now-c.lastProgressNS.Load() > idle {
			stalled = append(stalled, c)
		}
	}
	j.mu.Unlock()
	for _, c := range stalled {
		_ = c.conn.Close() // unblocks the in-flight Read
	}
}

func (c *Connection) readResponseWithDeadline(timeout time.Duration) (Response, error) {
	if timeout <= 0 {
		timeout = timeouts.StreamBodyTimeout
	}
	_ = c.conn.SetReadDeadline(utils.Now().Add(timeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	return c.readResponse()
}

func (c *Connection) readResponseCodeWithDeadline(timeout time.Duration) (int, []byte, error) {
	if timeout <= 0 {
		timeout = timeouts.StreamBodyTimeout
	}
	_ = c.conn.SetReadDeadline(utils.Now().Add(timeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	return c.readResponseCode()
}

// Connection represents an NNTP connection
type Connection struct {
	username, password, address string
	// pool is the ProviderPool this connection belongs to, set at checkout
	// creation. address alone cannot identify it: two accounts on the same
	// host have distinct pools. Carrying the pointer keeps put/release free
	// of map lookups (and of the allocation an ID string would cost).
	// Every dial goes through getOrCreateFromPool, so this is always set;
	// put and release still nil-check defensively.
	pool   *ProviderPool
	port   int
	conn   net.Conn
	text   *textproto.Reader
	reader *bufio.Reader
	writer *bufio.Writer
	logger zerolog.Logger
	closed atomic.Bool

	// bodyDec decodes complete BODY responses, reading through bodyReader.
	// Created once per connection; it retains a reusable 32KB read buffer.
	// Safe only because the protocol is strictly request/response — the
	// decoder never over-reads past the current response's terminator.
	bodyDec       *nntpyenc.BodyDecoder
	bodyTarget    []byte
	bodyTargetSet bool

	// Body-decode idle tracking. lastProgressNS is refreshed by bodyReader
	// while source reads make progress; idleNS is armed by
	// nextBodyWithIdleDeadline and read by the shared janitor goroutine
	// when sweeping for stalls. Stored in monotonic nanoseconds
	// (nanotimeNow). idleNS 0 means this connection isn't currently in a
	// body decode and the janitor should skip it.
	lastProgressNS atomic.Int64
	idleNS         atomic.Int64

	// writeTimeout bounds the next command write instead of the default
	// HandshakeTimeout. ping sets it for the length of its DATE so a health
	// check gets one budget for the whole round trip: without it the write
	// keeps the 10s handshake deadline and a peer that stopped reading
	// blocks the ping far past its own timeout. Only ever touched by the
	// single goroutine that owns the connection (it holds a pool slot and
	// the entry is out of the pool), so no synchronisation is needed.
	writeTimeout time.Duration
}

func (c *Connection) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	bodyIdleJanitor.remove(c)
	return c.conn.Close()
}

func (c *Connection) IsClosed() bool {
	return c.closed.Load()
}

func (c *Connection) authenticate() error {
	// Send AUTHINFO USER command
	if err := c.sendCommandArg("AUTHINFO USER", c.username); err != nil {
		return NewConnectionError(fmt.Errorf("failed to send username: %w", err))
	}

	resp, err := c.readResponse()
	if err != nil {
		return NewConnectionError(fmt.Errorf("failed to read user response: %w", err))
	}

	if resp.Code != 381 {
		return classifyNNTPError(resp.Code, fmt.Sprintf("unexpected response to AUTHINFO USER: %s", resp.Message))
	}

	// Send AUTHINFO PASS command
	if err := c.sendCommandArg("AUTHINFO PASS", c.password); err != nil {
		return NewConnectionError(fmt.Errorf("failed to send password: %w", err))
	}

	resp, err = c.readResponse()
	if err != nil {
		return NewConnectionError(fmt.Errorf("failed to read password response: %w", err))
	}

	if resp.Code != 281 {
		return classifyNNTPError(resp.Code, fmt.Sprintf("[%s] authentication failed: %s", c.address, resp.Message))
	}
	return nil
}

// startTLS initiates TLS encryption with proper error handling
func (c *Connection) startTLS() error {
	if err := c.sendCommand("STARTTLS"); err != nil {
		return NewConnectionError(fmt.Errorf("failed to send STARTTLS: %w", err))
	}

	resp, err := c.readResponse()
	if err != nil {
		return NewConnectionError(fmt.Errorf("failed to read STARTTLS response: %w", err))
	}

	if resp.Code != 382 {
		return classifyNNTPError(resp.Code, fmt.Sprintf("STARTTLS not supported: %s", resp.Message))
	}

	// Upgrade connection to TLS
	tlsConn := tls.Client(c.conn, &tls.Config{
		ServerName:         c.address,
		InsecureSkipVerify: true, // Match createConnection behavior
		MinVersion:         tls.VersionTLS12,
	})

	// Same sizing rationale as createConnection.
	c.conn = tlsConn
	c.reader = bufio.NewReaderSize(tlsConn, 128*1024)
	c.writer = bufio.NewWriterSize(tlsConn, 4*1024)
	c.text = textproto.NewReader(c.reader)

	c.logger.Debug().Msg("TLS encryption enabled")
	return nil
}

// ping sends a simple command to test the connection. timeout bounds the
// whole DATE round trip; <=0 uses PingTimeout. The budget differs by caller:
// a checkout verify-ping is user-visible latency and stays tight, while the
// reaper's background keepalive can afford to wait out congestion.
func (c *Connection) ping(timeout time.Duration) error {
	if c.conn == nil {
		return NewConnectionError(errors.New("connection is nil"))
	}
	if timeout <= 0 {
		timeout = timeouts.PingTimeout
	}
	_ = c.conn.SetDeadline(utils.Now().Add(timeout))
	c.writeTimeout = timeout
	defer func() {
		c.writeTimeout = 0
		_ = c.conn.SetDeadline(time.Time{})
	}()

	if err := c.sendCommand("DATE"); err != nil {
		return NewConnectionError(err)
	}
	resp, err := c.readResponse()
	if err != nil {
		return NewConnectionError(err)
	}
	if resp.Code != 111 {
		return NewConnectionError(fmt.Errorf("unexpected DATE response: %d %s", resp.Code, resp.Message))
	}
	return nil
}

// sendCommand sends a command to the NNTP server
func (c *Connection) sendCommand(command string) error {
	return c.sendCommandArg(command, "")
}

func (c *Connection) sendCommandArg(command, arg string) error {
	writeTimeout := c.writeTimeout
	if writeTimeout <= 0 {
		writeTimeout = timeouts.HandshakeTimeout
	}
	_ = c.conn.SetWriteDeadline(utils.Now().Add(writeTimeout))
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()

	if _, err := c.writer.WriteString(command); err != nil {
		return err
	}
	if arg != "" {
		if err := c.writer.WriteByte(' '); err != nil {
			return err
		}
		if _, err := c.writer.WriteString(arg); err != nil {
			return err
		}
	}
	if _, err := c.writer.WriteString("\r\n"); err != nil {
		return err
	}
	return c.writer.Flush()
}

// readResponse reads a response from the NNTP server
func (c *Connection) readResponse() (Response, error) {
	code, message, err := c.readResponseCode()
	if err != nil {
		return Response{}, err
	}

	return Response{
		Code:    code,
		Message: string(message),
	}, nil
}

// readResponseCode parses a short NNTP status line in-place from the connection
// buffer. Most BODY callers only need the code on success, so keeping the
// message as bytes avoids materializing a response string for every article.
func (c *Connection) readResponseCode() (int, []byte, error) {
	line, err := c.reader.ReadSlice('\n')
	if err != nil {
		return 0, nil, err
	}
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if len(line) < 3 ||
		line[0] < '0' || line[0] > '9' ||
		line[1] < '0' || line[1] > '9' ||
		line[2] < '0' || line[2] > '9' ||
		(len(line) > 3 && line[3] != ' ') {
		return 0, nil, fmt.Errorf("invalid response code: %s", line)
	}

	code := int(line[0]-'0')*100 + int(line[1]-'0')*10 + int(line[2]-'0')
	if len(line) == 3 {
		return code, nil, nil
	}
	return code, line[4:], nil
}

// readMultilineResponse reads a multiline response
func (c *Connection) readMultilineResponse() (*Response, error) {
	resp, err := c.readResponse()
	if err != nil {
		return nil, err
	}

	// Check if this is a multiline response
	if resp.Code < 200 || resp.Code >= 300 {
		return &resp, nil
	}

	lines, err := c.text.ReadDotLines()
	if err != nil {
		return nil, err
	}

	resp.Lines = lines
	return &resp, nil
}

// GetArticle retrieves an article by message ID with proper error classification
func (c *Connection) GetArticle(messageID string) (*Article, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommandArg("ARTICLE", messageID); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send ARTICLE command: %w", err))
	}

	resp, err := c.readMultilineResponse()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read article response: %w", err))
	}

	if resp.Code != 220 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	return c.parseArticle(messageID, resp.Lines)
}

// requestBody sends BODY and decodes the complete response through the
// per-connection decoder: status line, yEnc payload, and ".\r\n" terminator
// in one pass, with size and CRC verification. The returned Data buffer
// comes from bodyBufPool and the caller owns it. On error the connection may
// be mid-response and unusable; callers rely on the pool layer to discard
// errored connections.
func (c *Connection) requestBody(messageID string) (nntpyenc.BodyResult, error) {
	return c.requestBodyBuffered(messageID, nil, true)
}

func (c *Connection) requestBodyBuffered(messageID string, dst []byte, pooled bool) (nntpyenc.BodyResult, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommandArg("BODY", messageID); err != nil {
		return nntpyenc.BodyResult{}, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	if !pooled {
		c.bodyTarget = dst[:0]
		c.bodyTargetSet = true
		defer func() {
			c.bodyTarget = nil
			c.bodyTargetSet = false
		}()
	}
	res, err := c.nextBodyWithIdleDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		if pooled {
			putBodyBuf(res.Data)
		}
		res.Data = nil
		if res.StatusCode == 0 {
			return res, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
		}
		if errors.Is(err, nntpyenc.ErrDataMissing) {
			// The article exists but its body holds no yEnc data — same
			// taxonomy the segment fetcher applies to zero-byte bodies.
			return res, &Error{Type: ErrorTypeArticleNotFound, Code: res.StatusCode, Message: err.Error()}
		}
		return res, classifyTransferError("streaming yenc decode failed", err)
	}
	if res.StatusCode != 222 {
		if pooled {
			putBodyBuf(res.Data)
		}
		res.Data = nil
		return res, classifyNNTPError(res.StatusCode, res.Message)
	}
	return res, nil
}

func (c *Connection) nextBodyBuffer() []byte {
	if c.bodyTargetSet {
		c.bodyTargetSet = false
		return c.bodyTarget[:0]
	}
	return getBodyBuf()
}

func metadataFromResult(meta nntpyenc.DecoderMeta, snippet []byte) *YencMetadata {
	return &YencMetadata{
		Name:     meta.FileName,
		Size:     meta.FileSize,
		Part:     meta.PartNumber,
		Total:    meta.TotalParts,
		Offset:   meta.Offset,
		PartSize: meta.PartSize,
		Begin:    meta.Begin(),
		End:      meta.End(),
		Snippet:  snippet,
	}
}

// GetHeaderPrefix retrieves exact yEnc metadata plus a small decoded prefix
// while keeping the NNTP connection reusable (the whole response is consumed).
func (c *Connection) GetHeaderPrefix(messageID string, maxSnippet int) (*YencMetadata, error) {
	res, err := c.requestBody(messageID)
	if err != nil {
		// A parsed non-222 status leaves the connection at a clean response
		// boundary; anything else may leave part of the article on the wire.
		if res.StatusCode == 0 || res.StatusCode == 222 {
			_ = c.conn.Close()
		}
		return nil, err
	}
	var snippet []byte
	if maxSnippet > 0 {
		snippet = bytes.Clone(res.Data[:min(maxSnippet, len(res.Data))])
	}
	putBodyBuf(res.Data)
	return metadataFromResult(res.Meta, snippet), nil
}

// GetBody retrieves article body by message ID as raw bytes (used by GetHeader)
func (c *Connection) GetBody(messageID string) ([]byte, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommandArg("BODY", messageID); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	code, message, err := c.readResponseCodeWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if code != 222 {
		return nil, classifyNNTPError(code, string(message))
	}

	// Set read deadline to prevent hanging on stalled servers
	_ = c.conn.SetReadDeadline(utils.Now().Add(timeouts.StreamBodyTimeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	body, err := c.readDotBytes()
	if err != nil {
		return nil, classifyTransferError("failed to read body", err)
	}
	return body, nil
}

// GetDecodedBody retrieves and decodes an article body in one batch pass.
func (c *Connection) GetDecodedBody(messageID string) ([]byte, error) {
	decoded, _, err := c.GetDecodedBodyWithMetadata(messageID)
	return decoded, err
}

// GetDecodedBodyWithMetadata retrieves and decodes the article body while also
// returning the parsed yEnc metadata from the same pass. The returned slice
// escapes to the caller and is not recycled.
func (c *Connection) GetDecodedBodyWithMetadata(messageID string) ([]byte, *YencMetadata, error) {
	// The result escapes to a long-lived caller, so it must not take a 1 MiB
	// scratch buffer out of bodyBufPool permanently. Let rapidyenc allocate
	// storage sized for this article, just as DecodeBodyInto does when called
	// with an empty destination.
	res, err := c.requestBodyBuffered(messageID, nil, false)
	if err != nil {
		return nil, nil, err
	}
	return res.Data, metadataFromResult(res.Meta, nil), nil
}

// StreamBody decodes one article body and writes it to w in a single Write.
// On the streaming path w is the segment cache, where every Write costs a
// pwrite plus an exclusive buffer-lock acquisition; segment readers only see
// bytes after Finalize, so whole-article batching adds no visible latency.
func (c *Connection) StreamBody(messageID string, w io.Writer) (int64, error) {
	res, err := c.requestBody(messageID)
	if err != nil {
		return 0, err
	}
	n, err := w.Write(res.Data)
	putBodyBuf(res.Data)
	return int64(n), err
}

// DecodeBodyInto verifies one yEnc article into storage supplied by the
// caller. The returned slice belongs to the caller and may be retained.
func (c *Connection) DecodeBodyInto(messageID string, dst []byte) ([]byte, error) {
	res, err := c.requestBodyBuffered(messageID, dst, false)
	if err != nil {
		return nil, err
	}
	return res.Data, nil
}

// readDotBytes reads dot-terminated NNTP data using textproto.DotReader
// This matches Python nntplib's efficient buffered approach
func (c *Connection) readDotBytes() ([]byte, error) {
	// Use textproto's DotReader which efficiently handles dot-stuffing
	// and terminator detection with optimized buffered reading
	dotReader := c.text.DotReader()

	// Pre-allocate for typical usenet segment (~750KB)
	// Using io.ReadAll with pre-sized buffer hint
	buf := bytes.NewBuffer(make([]byte, 0, 800*1024))

	// Copy from DotReader to buffer
	_, err := io.Copy(buf, dotReader)
	if err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// GetHead retrieves article headers by message ID
func (c *Connection) GetHead(messageID string) ([]byte, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommandArg("HEAD", messageID); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send HEAD command: %w", err))
	}

	// Read the initial response
	resp, err := c.readResponse()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read head response: %w", err))
	}

	if resp.Code != 221 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	// Read the header data using textproto
	lines, err := c.text.ReadDotLines()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read header data: %w", err))
	}

	// Join with \r\n to preserve original line endings and add final \r\n
	headers := strings.Join(lines, "\r\n")
	if len(lines) > 0 {
		headers += "\r\n"
	}

	return []byte(headers), nil
}

func (c *Connection) Post(messageID, filename string, body []byte) error {
	now := utils.Now().Format("2006-01-02 15:04:05")
	if err := c.sendCommand("POST"); err != nil {
		return NewConnectionError(fmt.Errorf("failed to send POST command: %w", err))
	}

	resp, err := c.readResponse()
	if err != nil {
		return NewConnectionError(fmt.Errorf("failed to read POST response: %w", err))
	}

	// 340 = send article to be posted
	if resp.Code != 340 {
		// 440, 441, etc should be classified properly
		return classifyNNTPError(resp.Code, fmt.Sprintf("unexpected response to POST: %s", resp.Message))
	}

	// 2. Build RFC-822 style article (headers + blank line + body)
	var buf bytes.Buffer

	if filename != "" {
		buf.WriteString("Subject: " + filename + "\r\n")
	}

	buf.WriteString("Date: " + now + "\r\n")
	buf.WriteString("Newsgroups: " + "alt.binaries.friends" + "\r\n")
	if messageID != "" {
		// ensure proper <id> format
		msgID := FormatMessageID(messageID)
		buf.WriteString("Message-ID: " + msgID + "\r\n")
	}

	// End of headers
	buf.WriteString("\r\n")

	// 3. Body with CRLF normalization + dot-stuffing
	if len(body) > 0 {
		// Normalize to \n, then re-add \r\n
		body := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))
		lines := bytes.SplitSeq(body, []byte("\n"))

		for line := range lines {
			// Last split after trailing \n will give empty line; still write CRLF.
			if len(line) > 0 && line[0] == '.' {
				// dot-stuff per NNTP
				buf.WriteByte('.')
			}
			buf.Write(line)
			buf.WriteString("\r\n")
		}
	}

	// 4. Terminator line
	buf.WriteString(".\r\n")

	// 5. Send article data
	if _, err := c.writer.Write(buf.Bytes()); err != nil {
		return NewConnectionError(fmt.Errorf("failed to send article data: %w", err))
	}
	if err := c.writer.Flush(); err != nil {
		return NewConnectionError(fmt.Errorf("failed to flush article data: %w", err))
	}

	// 6. Final response
	resp, err = c.readResponse()
	if err != nil {
		return NewConnectionError(fmt.Errorf("failed to read post completion response: %w", err))
	}

	if resp.Code != 240 { // 240 = article received OK
		return classifyNNTPError(resp.Code, resp.Message)
	}

	return nil
}

// Stat retrieves article statistics by message ID with proper error classification
func (c *Connection) Stat(messageID string) (articleNumber int, echoedID string, err error) {
	messageID = FormatMessageID(messageID)

	if err = c.sendCommandArg("STAT", messageID); err != nil {
		return 0, "", NewConnectionError(fmt.Errorf("failed to send STAT: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return 0, "", NewConnectionError(fmt.Errorf("failed to read STAT response: %w", err))
	}

	if resp.Code != 223 {
		return 0, "", classifyNNTPError(resp.Code, resp.Message)
	}

	fields := strings.Fields(resp.Message)
	if len(fields) < 2 {
		return 0, "", NewProtocolError(resp.Code, fmt.Sprintf("unexpected STAT response format: %q", resp.Message))
	}

	if articleNumber, err = strconv.Atoi(fields[0]); err != nil {
		return 0, "", NewProtocolError(resp.Code, fmt.Sprintf("invalid article number %q: %v", fields[0], err))
	}
	echoedID = fields[1]

	return articleNumber, echoedID, nil
}

// SelectGroup selects a newsgroup and returns group information
func (c *Connection) SelectGroup(groupName string) (*GroupInfo, error) {
	if err := c.sendCommandArg("GROUP", groupName); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send GROUP command: %w", err))
	}

	resp, err := c.readResponse()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read GROUP response: %w", err))
	}

	if resp.Code != 211 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	// Parse GROUP response: "211 number low high group-name"
	fields := strings.Fields(resp.Message)
	if len(fields) < 4 {
		return nil, NewProtocolError(resp.Code, fmt.Sprintf("unexpected GROUP response format: %q", resp.Message))
	}

	groupInfo := &GroupInfo{
		Name: groupName,
	}

	if count, err := strconv.Atoi(fields[0]); err == nil {
		groupInfo.Count = count
	}
	if low, err := strconv.Atoi(fields[1]); err == nil {
		groupInfo.Low = low
	}
	if high, err := strconv.Atoi(fields[2]); err == nil {
		groupInfo.High = high
	}

	return groupInfo, nil
}

// parseArticle parses article data from response lines
func (c *Connection) parseArticle(messageID string, lines []string) (*Article, error) {
	article := &Article{
		MessageID: messageID,
		Groups:    []string{},
	}

	headerEnd := -1
	for i, line := range lines {
		if line == "" {
			headerEnd = i
			break
		}

		// Parse headers
		if after, ok := strings.CutPrefix(line, "Subject: "); ok {
			article.Subject = after
		} else if after, ok := strings.CutPrefix(line, "From: "); ok {
			article.From = after
		} else if after, ok := strings.CutPrefix(line, "Date: "); ok {
			article.Date = after
		} else if after, ok := strings.CutPrefix(line, "Newsgroups: "); ok {
			groups := after
			article.Groups = strings.Split(groups, ",")
			for i := range article.Groups {
				article.Groups[i] = strings.TrimSpace(article.Groups[i])
			}
		}
	}

	// Join body lines
	if headerEnd != -1 && headerEnd+1 < len(lines) {
		body := strings.Join(lines[headerEnd+1:], "\n")
		article.Body = []byte(body)
		article.Size = int64(len(article.Body))
	}

	return article, nil
}

// FormatMessageID ensures message ID has proper format
func FormatMessageID(messageID string) string {
	messageID = strings.TrimSpace(messageID)
	if !strings.HasPrefix(messageID, "<") {
		messageID = "<" + messageID
	}
	if !strings.HasSuffix(messageID, ">") {
		messageID = messageID + ">"
	}
	return messageID
}
