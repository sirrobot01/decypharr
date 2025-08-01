package nntp

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"github.com/chrisfarms/yenc"
	"github.com/rs/zerolog"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
)

// Connection represents an NNTP connection
type Connection struct {
	username, password, address string
	port                        int
	conn                        net.Conn
	text                        *textproto.Conn
	reader                      *bufio.Reader
	writer                      *bufio.Writer
	logger                      zerolog.Logger
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
		return classifyNNTPError(resp.Code, fmt.Sprintf("authentication failed: %s", resp.Message))
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
		InsecureSkipVerify: false,
	})

	c.conn = tlsConn
	c.reader = bufio.NewReader(tlsConn)
	c.writer = bufio.NewWriter(tlsConn)
	c.text = textproto.NewConn(tlsConn)

	c.logger.Debug().Msg("TLS encryption enabled")
	return nil
}

// ping sends a simple command to test the connection
func (c *Connection) ping() error {
	if err := c.sendCommand("DATE"); err != nil {
		return NewConnectionError(err)
	}
	_, err := c.readResponse()
	if err != nil {
		return NewConnectionError(err)
	}
	return nil
}

// sendCommand sends a command to the NNTP server
func (c *Connection) sendCommand(command string) error {
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

// GetBody retrieves article body by message ID with proper error classification
func (c *Connection) GetBody(messageID string) ([]byte, error) {
	messageID = FormatMessageID(messageID)
	if err := c.sendCommand(fmt.Sprintf("BODY %s", messageID)); err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to send BODY command: %w", err))
	}

	// Read the initial response
	resp, err := c.readResponse()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body response: %w", err))
	}

	if resp.Code != 222 {
		return nil, classifyNNTPError(resp.Code, resp.Message)
	}

	// Read the raw body data directly using textproto to preserve exact formatting for yEnc
	lines, err := c.text.ReadDotLines()
	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to read body data: %w", err))
	}

	// Join with \r\n to preserve original line endings and add final \r\n
	body := strings.Join(lines, "\r\n")
	if len(lines) > 0 {
		body += "\r\n"
	}

	return []byte(body), nil
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

// GetSegment retrieves a specific segment with proper error handling
func (c *Connection) GetSegment(messageID string, segmentNumber int) (*Segment, error) {
	messageID = FormatMessageID(messageID)
	body, err := c.GetBody(messageID)
	if err != nil {
		return nil, err // GetBody already returns classified errors
	}

	return &Segment{
		MessageID: messageID,
		Number:    segmentNumber,
		Bytes:     int64(len(body)),
		Data:      body,
	}, nil
}

// Stat retrieves article statistics by message ID with proper error classification
func (c *Connection) Stat(messageID string) (articleNumber int, echoedID string, err error) {
	messageID = FormatMessageID(messageID)

	if err = c.sendCommand(fmt.Sprintf("STAT %s", messageID)); err != nil {
		return 0, "", NewConnectionError(fmt.Errorf("failed to send STAT: %w", err))
	}

	resp, err := c.readResponse()
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

// close closes the NNTP connection
func (c *Connection) close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

func DecodeYenc(reader io.Reader) (*yenc.Part, error) {
	part, err := yenc.Decode(reader)
	if err != nil {
		return nil, NewYencDecodeError(fmt.Errorf("failed to create yenc decoder: %w", err))
	}
	return part, nil
}

func IsValidMessageID(messageID string) bool {
	if len(messageID) < 3 {
		return false
	}
	return strings.Contains(messageID, "@")
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
