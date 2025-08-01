package nntp

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"net"
	"net/textproto"
	"sync"
	"sync/atomic"
	"time"
)

// Pool manages a pool of NNTP connections
type Pool struct {
	address, username, password string
	maxConns, port              int
	ssl                         bool
	useTLS                      bool
	connections                 chan *Connection
	logger                      zerolog.Logger
	closed                      atomic.Bool
	totalConnections            atomic.Int32
	activeConnections           atomic.Int32
}

// Segment represents a usenet segment
type Segment struct {
	MessageID string
	Number    int
	Bytes     int64
	Data      []byte
}

// Article represents a complete usenet article
type Article struct {
	MessageID string
	Subject   string
	From      string
	Date      string
	Groups    []string
	Body      []byte
	Size      int64
}

// Response represents an NNTP server response
type Response struct {
	Code    int
	Message string
	Lines   []string
}

// GroupInfo represents information about a newsgroup
type GroupInfo struct {
	Name  string
	Count int // Number of articles in the group
	Low   int // Lowest article number
	High  int // Highest article number
}

// NewPool creates a new NNTP connection pool
func NewPool(provider config.UsenetProvider, logger zerolog.Logger) (*Pool, error) {
	maxConns := provider.Connections
	if maxConns <= 0 {
		maxConns = 1
	}

	pool := &Pool{
		address:     provider.Host,
		username:    provider.Username,
		password:    provider.Password,
		port:        provider.Port,
		maxConns:    maxConns,
		ssl:         provider.SSL,
		useTLS:      provider.UseTLS,
		connections: make(chan *Connection, maxConns),
		logger:      logger,
	}

	return pool.initializeConnections()
}

func (p *Pool) initializeConnections() (*Pool, error) {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var successfulConnections []*Connection
	var errs []error

	// Create connections concurrently
	for i := 0; i < p.maxConns; i++ {
		wg.Add(1)
		go func(connIndex int) {
			defer wg.Done()

			conn, err := p.createConnection()

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errs = append(errs, err)
			} else {
				successfulConnections = append(successfulConnections, conn)
			}
		}(i)
	}

	// Wait for all connection attempts to complete
	wg.Wait()

	// Add successful connections to the pool
	for _, conn := range successfulConnections {
		p.connections <- conn
	}
	p.totalConnections.Store(int32(len(successfulConnections)))

	if len(successfulConnections) == 0 {
		return nil, fmt.Errorf("failed to create any connections: %v", errs)
	}

	// Log results
	p.logger.Info().
		Str("server", p.address).
		Int("port", p.port).
		Int("requested_connections", p.maxConns).
		Int("successful_connections", len(successfulConnections)).
		Int("failed_connections", len(errs)).
		Msg("NNTP connection pool created")

	// If some connections failed, log a warning but continue
	if len(errs) > 0 {
		p.logger.Warn().
			Int("failed_count", len(errs)).
			Msg("Some connections failed during pool initialization")
	}

	return p, nil
}

// Get retrieves a connection from the pool
func (p *Pool) Get(ctx context.Context) (*Connection, error) {
	if p.closed.Load() {
		return nil, NewConnectionError(fmt.Errorf("connection pool is closed"))
	}

	select {
	case conn := <-p.connections:
		if conn == nil {
			return nil, NewConnectionError(fmt.Errorf("received nil connection from pool"))
		}
		p.activeConnections.Add(1)

		if err := conn.ping(); err != nil {
			p.activeConnections.Add(-1)
			err := conn.close()
			if err != nil {
				return nil, err
			}
			// Create a new connection
			newConn, err := p.createConnection()
			if err != nil {
				return nil, NewConnectionError(fmt.Errorf("failed to create replacement connection: %w", err))
			}
			p.activeConnections.Add(1)
			return newConn, nil
		}

		return conn, nil
	case <-ctx.Done():
		return nil, NewTimeoutError(ctx.Err())
	}
}

// Put returns a connection to the pool
func (p *Pool) Put(conn *Connection) {
	if conn == nil {
		return
	}

	defer p.activeConnections.Add(-1)

	if p.closed.Load() {
		conn.close()
		return
	}

	// Try non-blocking first
	select {
	case p.connections <- conn:
		return
	default:
	}

	// If pool is full, this usually means we have too many connections
	// Force return by making space (close oldest connection)
	select {
	case oldConn := <-p.connections:
		oldConn.close()       // Close the old connection
		p.connections <- conn // Put the new one back
	case <-time.After(1 * time.Second):
		// Still can't return - close this connection
		conn.close()
	}
}

// Close closes all connections in the pool
func (p *Pool) Close() error {

	if p.closed.Load() {
		return nil
	}
	p.closed.Store(true)

	close(p.connections)
	for conn := range p.connections {
		err := conn.close()
		if err != nil {
			return err
		}
	}

	p.logger.Info().Msg("NNTP connection pool closed")
	return nil
}

// createConnection creates a new NNTP connection with proper error handling
func (p *Pool) createConnection() (*Connection, error) {
	addr := fmt.Sprintf("%s:%d", p.address, p.port)

	var conn net.Conn
	var err error

	if p.ssl {
		conn, err = tls.DialWithDialer(&net.Dialer{}, "tcp", addr, &tls.Config{
			InsecureSkipVerify: false,
		})
	} else {
		conn, err = net.Dial("tcp", addr)
	}

	if err != nil {
		return nil, NewConnectionError(fmt.Errorf("failed to connect to %s: %w", addr, err))
	}

	reader := bufio.NewReaderSize(conn, 256*1024) // 256KB buffer for better performance
	writer := bufio.NewWriterSize(conn, 256*1024) // 256KB buffer for better performance
	text := textproto.NewConn(conn)

	nntpConn := &Connection{
		username: p.username,
		password: p.password,
		address:  p.address,
		port:     p.port,
		conn:     conn,
		text:     text,
		reader:   reader,
		writer:   writer,
		logger:   p.logger,
	}

	// Read welcome message
	_, err = nntpConn.readResponse()
	if err != nil {
		conn.Close()
		return nil, NewConnectionError(fmt.Errorf("failed to read welcome message: %w", err))
	}

	// Authenticate if credentials are provided
	if p.username != "" && p.password != "" {
		if err := nntpConn.authenticate(); err != nil {
			conn.Close()
			return nil, err // authenticate() already returns NNTPError
		}
	}

	// Enable TLS if requested (STARTTLS)
	if p.useTLS && !p.ssl {
		if err := nntpConn.startTLS(); err != nil {
			conn.Close()
			return nil, err // startTLS() already returns NNTPError
		}
	}
	return nntpConn, nil
}

func (p *Pool) ConnectionCount() int {
	return int(p.totalConnections.Load())
}

func (p *Pool) ActiveConnections() int {
	return int(p.activeConnections.Load())
}

func (p *Pool) IsFree() bool {
	return p.ActiveConnections() < p.maxConns
}
