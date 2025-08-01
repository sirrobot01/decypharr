package nntp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"sync/atomic"
	"time"
)

// Client represents a failover NNTP client that manages multiple providers
type Client struct {
	providers       []config.UsenetProvider
	pools           *xsync.Map[string, *Pool]
	logger          zerolog.Logger
	closed          atomic.Bool
	minimumMaxConns int // Minimum number of max connections across all pools
}

func NewClient(providers []config.UsenetProvider) (*Client, error) {

	client := &Client{
		providers: providers,
		logger:    logger.New("nntp"),
		pools:     xsync.NewMap[string, *Pool](),
	}
	if len(providers) == 0 {
		return nil, fmt.Errorf("no NNTP providers configured")
	}
	return client, nil
}

func (c *Client) InitPools() error {

	var initErrors []error
	successfulPools := 0

	for _, provider := range c.providers {
		serverPool, err := NewPool(provider, c.logger)
		if err != nil {
			c.logger.Error().
				Err(err).
				Str("server", provider.Host).
				Int("port", provider.Port).
				Msg("Failed to initialize server pool")
			initErrors = append(initErrors, err)
			continue
		}
		if c.minimumMaxConns == 0 {
			// Set minimumMaxConns to the max connections of the first successful pool
			c.minimumMaxConns = serverPool.ConnectionCount()
		} else {
			c.minimumMaxConns = min(c.minimumMaxConns, serverPool.ConnectionCount())
		}

		c.pools.Store(provider.Name, serverPool)
		successfulPools++
	}

	if successfulPools == 0 {
		return fmt.Errorf("failed to initialize any server pools: %v", initErrors)
	}

	c.logger.Info().
		Int("providers", len(c.providers)).
		Msg("NNTP client created")

	return nil
}

func (c *Client) Close() {
	if c.closed.Load() {
		c.logger.Warn().Msg("NNTP client already closed")
		return
	}

	c.pools.Range(func(key string, value *Pool) bool {
		if value != nil {
			err := value.Close()
			if err != nil {
				return false
			}
		}
		return true
	})

	c.closed.Store(true)
	c.logger.Info().Msg("NNTP client closed")
}

func (c *Client) GetConnection(ctx context.Context) (*Connection, func(), error) {
	if c.closed.Load() {
		return nil, nil, fmt.Errorf("nntp client is closed")
	}

	// Prevent workers from waiting too long for connections
	connCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	providerCount := len(c.providers)

	for _, provider := range c.providers {
		pool, ok := c.pools.Load(provider.Name)
		if !ok {
			return nil, nil, fmt.Errorf("no pool found for provider %s", provider.Name)
		}

		if !pool.IsFree() && providerCount > 1 {
			continue
		}

		conn, err := pool.Get(connCtx) // Use timeout context
		if err != nil {
			if errors.Is(err, ErrNoAvailableConnection) || errors.Is(err, context.DeadlineExceeded) {
				continue
			}
			return nil, nil, fmt.Errorf("error getting connection from provider %s: %w", provider.Name, err)
		}

		if conn == nil {
			continue
		}

		return conn, func() { pool.Put(conn) }, nil
	}

	return nil, nil, ErrNoAvailableConnection
}

func (c *Client) DownloadHeader(ctx context.Context, messageID string) (*YencMetadata, error) {
	conn, cleanup, err := c.GetConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	data, err := conn.GetBody(messageID)
	if err != nil {
		return nil, err
	}

	// yEnc decode
	part, err := DecodeYencHeaders(bytes.NewReader(data))
	if err != nil || part == nil {
		return nil, fmt.Errorf("failed to decode segment")
	}

	// Return both the filename and decoded data
	return part, nil
}

func (c *Client) MinimumMaxConns() int {
	return c.minimumMaxConns
}

func (c *Client) TotalActiveConnections() int {
	total := 0
	c.pools.Range(func(key string, value *Pool) bool {
		if value != nil {
			total += value.ActiveConnections()
		}
		return true
	})
	return total
}

func (c *Client) Pools() *xsync.Map[string, *Pool] {
	return c.pools
}

func (c *Client) GetProviders() []config.UsenetProvider {
	return c.providers
}
