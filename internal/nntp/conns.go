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

// Note: Timeout values are defined in TimeoutConfig (client.go)
// Use timeouts.timeouts.StreamBodyTimeout for read deadlines

// bodyCopyBufPool provides reusable 128KB buffers for idle-deadline body
// copies, keeping the streaming hot path allocation-free.
var bodyCopyBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, 128*1024)
		return &b
	},
}

// copyBodyWithIdleDeadline copies src→dst, resetting the connection read
// deadline before every read so the timeout behaves as an *idle* deadline
// rather than an absolute cap on the whole transfer. A slow-but-progressing
// segment is no longer killed mid-stream; only a genuine stall (no bytes for
// `idle`) trips the deadline.
func (c *Connection) copyBodyWithIdleDeadline(dst io.Writer, src io.Reader, idle time.Duration) (int64, error) {
	if idle <= 0 {
		idle = 60 * time.Second
	}
	bufPtr := bodyCopyBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer bodyCopyBufPool.Put(bufPtr)

	var total int64
	for {
		_ = c.conn.SetReadDeadline(utils.Now().Add(idle))
		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[:nr])
			total += int64(nw)
			if ew != nil {
				return total, ew
			}
			if nw != nr {
				return total, io.ErrShortWrite
			}
		}
		if er != nil {
			if er == io.EOF {
				return total, nil
			}
			return total, er
		}
	}
}

func (c *Connection) readResponseWithDeadline(timeout time.Duration) (*Response, error) {
	if timeout <= 0 {
		timeout = timeouts.StreamBodyTimeout
	}
	_ = c.conn.SetReadDeadline(utils.Now().Add(timeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()
	return c.readResponse()
}

// Connection represents an NNTP connection
type Connection struct {
	username, password, address string
	port                        int
	conn                        net.Conn
	text                        *textproto.Reader
	reader                      *bufio.Reader
	writer                      *bufio.Writer
	logger                      zerolog.Logger
	closed                      atomic.Bool
}

func (c *Connection) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.conn.Close()
}

func (c *Connection) IsClosed() bool {
	return c.closed.Load()
}

func (c *Connection) authenticate() error {
	// Send AUTHINFO USER command
	if err := c.sendCommand(fmt.Sprintf("AUTHINFO USER %s", c.username)); err != nil {
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
	if err := c.sendCommand(fmt.Sprintf("AUTHINFO PASS %s", c.password)); err != nil {
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

	c.conn = tlsConn
	c.reader = bufio.NewReaderSize(tlsConn, 256*1024)
	c.writer = bufio.NewWriterSize(tlsConn, 256*1024)
	c.text = textproto.NewReader(c.reader)

	c.logger.Debug().Msg("TLS encryption enabled")
	return nil
}

// ping sends a simple command to test the connection
func (c *Connection) ping() error {
	if c.conn == nil {
		return NewConnectionError(errors.New("connection is nil"))
	}
	_ = c.conn.SetDeadline(utils.Now().Add(timeouts.PingTimeout))
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()

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
	_ = c.conn.SetWriteDeadline(utils.Now().Add(timeouts.HandshakeTimeout))
	defer func() { _ = c.conn.SetWriteDeadline(time.Time{}) }()

	_, err := fmt.Fprintf(c.writer, "%s\r\n", command)
	if err != nil {
		return err
	}
	return c.writer.Flush()
}

// readResponse reads a response from the NNTP server
func (c *Connection) readResponse() (*Response, error) {
	line, err := c.text.ReadLine()
	if err != nil {
		return nil, err
	}

	parts := strings.SplitN(line, " ", 2)
	code, err := strconv.Atoi(parts[0])
	if err != nil {
		return nil, fmt.Errorf("invalid response code: %s", parts[0])
	}

	message := ""
	if len(parts) > 1 {
		message = parts[1]
	}

	return &Response{
		Code:    code,
		Message: message,
	}, nil
}

// readMultilineResponse reads a multiline response
func (c *Connection) readMultilineResponse() (*Response, error) {
	resp, err := c.readResponse()
	if err != nil {
		return nil, err
	}

	// Check if this is a multiline response
	if resp.Code < 200 || resp.Code >= 300 {
		return resp, nil
	}

	lines, err := c.text.ReadDotLines()
	if err != nil {
		return nil, err
	}

	resp.Lines = lines
	return resp, nil
}

// GetArticle retrieves an article by message ID with proper error classification
func (c *Connection) GetArticle(messageID string) (*Article, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("ARTICLE %s", messageID)); err != nil {
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

func (c *Connection) GetHeader(messageID string, maxSnippet int) (*YencMetadata, error) {
	messageID = FormatMessageID(messageID)
	// Send BODY command to start streaming
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	// Set read deadline to prevent hanging on stalled servers
	_ = c.conn.SetReadDeadline(utils.Now().Add(timeouts.StreamBodyTimeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	dec := nntpyenc.AcquireDecoder(c.reader)
	defer nntpyenc.ReleaseDecoder(dec)

	// Read snippet to trigger header parsing and capture metadata.
	snippet := make([]byte, maxSnippet)
	n, err := io.ReadFull(dec, snippet)
	if err != nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
		_ = c.conn.Close()
		return nil, classifyTransferError("failed to read snippet", err)
	}
	// Truncate snippet to actual read size
	snippet = snippet[:n]
	meta := &YencMetadata{
		Name:     dec.Meta.FileName,
		Size:     dec.Meta.FileSize,
		Part:     dec.Meta.PartNumber,
		Total:    dec.Meta.TotalParts,
		Offset:   dec.Meta.Offset,
		PartSize: dec.Meta.PartSize,
		Begin:    dec.Meta.Begin(),
		End:      dec.Meta.End(),
		Snippet:  snippet,
	}

	// Close connection to stop stream
	_ = c.Close()

	return meta, nil
}

func metadataFromDecoder(dec *nntpyenc.Decoder, snippet []byte) *YencMetadata {
	return &YencMetadata{
		Name:     dec.Meta.FileName,
		Size:     dec.Meta.FileSize,
		Part:     dec.Meta.PartNumber,
		Total:    dec.Meta.TotalParts,
		Offset:   dec.Meta.Offset,
		PartSize: dec.Meta.PartSize,
		Begin:    dec.Meta.Begin(),
		End:      dec.Meta.End(),
		Snippet:  snippet,
	}
}

// GetHeaderPrefix retrieves exact yEnc metadata plus a small decoded prefix
// while keeping the NNTP connection reusable by draining the decoder to EOF.
func (c *Connection) GetHeaderPrefix(messageID string, maxSnippet int) (*YencMetadata, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	_ = c.conn.SetReadDeadline(utils.Now().Add(timeouts.StreamBodyTimeout))
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	dec := nntpyenc.AcquireDecoder(c.reader)
	defer nntpyenc.ReleaseDecoder(dec)

	var snippet []byte
	if maxSnippet > 0 {
		snippet = make([]byte, maxSnippet)
		n, readErr := io.ReadFull(dec, snippet)
		if readErr != nil && readErr != io.EOF && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			_ = c.conn.Close()
			return nil, classifyTransferError("failed to read snippet", readErr)
		}
		snippet = snippet[:n]
	}

	if _, err := c.copyBodyWithIdleDeadline(io.Discard, dec, timeouts.StreamBodyTimeout); err != nil {
		_ = c.conn.Close()
		return nil, classifyTransferError("failed to drain article body", err)
	}

	return metadataFromDecoder(dec, snippet), nil
}

// GetBody retrieves article body by message ID as raw bytes (used by GetHeader)
func (c *Connection) GetBody(messageID string) ([]byte, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
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

// GetDecodedBody retrieves and decodes article body using streaming yEnc decode.
// Uses textproto.DotReader + rapidyenc streaming decoder to decode while reading
// from the network - no intermediate buffering of the full body.
func (c *Connection) GetDecodedBody(messageID string) ([]byte, error) {
	decoded, _, err := c.GetDecodedBodyWithMetadata(messageID)
	return decoded, err
}

// GetDecodedBodyWithMetadata retrieves and decodes the article body while also
// returning the parsed yEnc metadata from the same pass.
func (c *Connection) GetDecodedBodyWithMetadata(messageID string) ([]byte, *YencMetadata, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return nil, nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return nil, nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return nil, nil, classifyNNTPError(resp.Code, resp.Message)
	}

	// Idle (not absolute) read deadline: reset on every read so a slow but
	// progressing transfer survives — only a real stall trips it.
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }()

	dec := nntpyenc.AcquireDecoder(c.reader)
	// Always release decoder back to pool, even on panic
	defer nntpyenc.ReleaseDecoder(dec)

	// Pre-allocate output buffer for decoded data (~700KB typical)
	output := bytes.NewBuffer(make([]byte, 0, 750*1024))
	_, err = c.copyBodyWithIdleDeadline(output, dec, timeouts.StreamBodyTimeout)

	if err != nil {
		return nil, nil, classifyTransferError("streaming yenc decode failed", err)
	}
	decoded := output.Bytes()

	return decoded, metadataFromDecoder(dec, nil), nil
}

func (c *Connection) StreamBody(messageID string, w io.Writer) (int64, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return 0, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	resp, err := c.readResponseWithDeadline(timeouts.StreamBodyTimeout)
	if err != nil {
		return 0, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return 0, classifyNNTPError(resp.Code, resp.Message)
	}

	// Idle (not absolute) read deadline: reset on every read so a slow but
	// progressing transfer survives — only a real stall trips it.
	defer func() { _ = c.conn.SetReadDeadline(time.Time{}) }() // Clear deadline

	dec := nntpyenc.AcquireDecoder(c.reader)
	// Always release decoder back to pool, even on panic
	defer nntpyenc.ReleaseDecoder(dec)
	n, err := c.copyBodyWithIdleDeadline(w, dec, timeouts.StreamBodyTimeout)
	if err != nil {
		return n, classifyTransferError("streaming yenc decode failed", err)
	}
	return n, nil
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
	if err := c.sendCommand(fmt.Sprintf("HEAD %s", messageID)); err != nil {
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
		lines := bytes.Split(body, []byte("\n"))

		for _, line := range lines {
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

	if err = c.sendCommand(fmt.Sprintf("STAT %s", messageID)); err != nil {
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
	if err := c.sendCommand(fmt.Sprintf("GROUP %s", groupName)); err != nil {
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
		if strings.HasPrefix(line, "Subject: ") {
			article.Subject = strings.TrimPrefix(line, "Subject: ")
		} else if strings.HasPrefix(line, "From: ") {
			article.From = strings.TrimPrefix(line, "From: ")
		} else if strings.HasPrefix(line, "Date: ") {
			article.Date = strings.TrimPrefix(line, "Date: ")
		} else if strings.HasPrefix(line, "Newsgroups: ") {
			groups := strings.TrimPrefix(line, "Newsgroups: ")
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
