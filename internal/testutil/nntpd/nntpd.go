// Package nntpd is an in-process fake NNTP server for benchmarks and tests.
// It speaks just enough of the protocol for the client in internal/nntp —
// greeting, AUTHINFO USER/PASS, DATE, STAT, BODY, QUIT — serving pre-encoded
// yEnc articles with configurable per-response RTT and per-connection
// bandwidth.
package nntpd

import (
	"bufio"
	"bytes"
	"fmt"
	"hash/crc32"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config controls simulated network behavior.
type Config struct {
	// RTT is the artificial delay applied before every response, simulating
	// one network round trip per command.
	RTT time.Duration
	// Bandwidth caps body streaming per connection in bytes/second.
	// 0 means unlimited.
	Bandwidth int64
}

// Server listens on a loopback port until Close.
type Server struct {
	cfg      Config
	ln       net.Listener
	mu       sync.Mutex
	articles map[string][]byte
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
	closed   atomic.Bool
	// Bodies counts BODY responses served, for bodies/op bench metrics.
	Bodies atomic.Int64
}

func New(cfg Config) (*Server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{
		cfg:      cfg,
		ln:       ln,
		articles: make(map[string][]byte),
		conns:    make(map[net.Conn]struct{}),
	}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr returns the host and port the server listens on.
func (s *Server) Addr() (string, int) {
	return "127.0.0.1", s.ln.Addr().(*net.TCPAddr).Port
}

// AddArticle registers a pre-encoded yEnc body (without the ".\r\n"
// terminator) under messageID, which must include the angle brackets.
func (s *Server) AddArticle(messageID string, encodedBody []byte) {
	s.mu.Lock()
	s.articles[messageID] = encodedBody
	s.mu.Unlock()
}

// Close stops the listener and tears down every open connection.
func (s *Server) Close() {
	if s.closed.Swap(true) {
		return
	}
	_ = s.ln.Close()
	s.mu.Lock()
	for conn := range s.conns {
		_ = conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		s.conns[conn] = struct{}{}
		s.mu.Unlock()
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, conn)
		s.mu.Unlock()
		_ = conn.Close()
		s.wg.Done()
	}()

	reader := bufio.NewReaderSize(conn, 4096)
	writer := bufio.NewWriterSize(conn, 256*1024)

	if s.respond(writer, "200 nntpd ready") != nil {
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var arg string
		if len(fields) > 1 {
			arg = fields[len(fields)-1]
		}

		switch strings.ToUpper(fields[0]) {
		case "AUTHINFO":
			if len(fields) > 1 && strings.EqualFold(fields[1], "USER") {
				err = s.respond(writer, "381 password required")
			} else {
				err = s.respond(writer, "281 authentication accepted")
			}
		case "DATE":
			err = s.respond(writer, "111 20260101000000")
		case "STAT":
			if s.lookup(arg) != nil {
				err = s.respond(writer, "223 0 "+arg)
			} else {
				err = s.respond(writer, "430 no such article")
			}
		case "BODY":
			body := s.lookup(arg)
			if body == nil {
				err = s.respond(writer, "430 no such article")
				break
			}
			s.Bodies.Add(1)
			s.sleepRTT()
			if _, err = writer.WriteString("222 0 " + arg + " body\r\n"); err != nil {
				return
			}
			if err = s.writeThrottled(writer, body); err != nil {
				return
			}
			if _, err = writer.WriteString(".\r\n"); err != nil {
				return
			}
			err = writer.Flush()
		case "QUIT":
			_ = s.respond(writer, "205 bye")
			return
		default:
			err = s.respond(writer, "500 unknown command")
		}
		if err != nil {
			return
		}
	}
}

func (s *Server) lookup(messageID string) []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.articles[messageID]
}

func (s *Server) respond(w *bufio.Writer, line string) error {
	s.sleepRTT()
	if _, err := w.WriteString(line + "\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

func (s *Server) sleepRTT() {
	if s.cfg.RTT > 0 {
		time.Sleep(s.cfg.RTT)
	}
}

// writeThrottled streams data, pacing flushes to the configured bandwidth.
func (s *Server) writeThrottled(w *bufio.Writer, data []byte) error {
	if s.cfg.Bandwidth <= 0 {
		_, err := w.Write(data)
		return err
	}
	const chunk = 64 * 1024
	for off := 0; off < len(data); off += chunk {
		end := min(off+chunk, len(data))
		if _, err := w.Write(data[off:end]); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
		time.Sleep(time.Duration(float64(end-off) / float64(s.cfg.Bandwidth) * float64(time.Second)))
	}
	return nil
}

// Pattern returns deterministic bytes addressable by file offset, so reads
// can be verified at any position.
func Pattern(offset int64, n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte((offset + int64(i)) % 251)
	}
	return p
}

// Encode produces the yEnc-encoded article body for one part of a file,
// with a correct pcrc32 so decodes exercise CRC verification. offset is the
// part's start within the file, fileSize the whole file's size.
func Encode(payload []byte, name string, part int, fileSize, offset int64) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "=ybegin part=%d line=128 size=%d name=%s\r\n", part, fileSize, name)
	fmt.Fprintf(&buf, "=ypart begin=%d end=%d\r\n", offset+1, offset+int64(len(payload)))
	col := 0
	for _, b := range payload {
		e := b + 42
		if e == 0 || e == '\n' || e == '\r' || e == '=' || e == '\t' || e == ' ' || e == '.' {
			buf.WriteByte('=')
			buf.WriteByte(e + 64)
			col += 2
		} else {
			buf.WriteByte(e)
			col++
		}
		if col >= 128 {
			buf.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		buf.WriteString("\r\n")
	}
	fmt.Fprintf(&buf, "=yend size=%d part=%d pcrc32=%08x\r\n", len(payload), part, crc32.ChecksumIEEE(payload))
	return buf.Bytes()
}
