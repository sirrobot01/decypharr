package usenet

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"io"
	"os"
)

// Usenet interface for usenet operations
type Usenet interface {
	Start(ctx context.Context) error
	IsReady() chan struct{}
	ProcessNZB(ctx context.Context, req *ProcessRequest) (*NZB, error)
	GetDownloadByteRange(nzoID string, filename string) (int64, int64, error)
	Close()
	Logger() zerolog.Logger
	Stream(ctx context.Context, nzbID string, filename string, start, end int64, writer io.Writer) error

	Store() Store
	Client() *nntp.Client
}

// Client implements UsenetClient
type usenet struct {
	client    *nntp.Client
	store     Store
	processor *Processor
	parser    *NZBParser
	streamer  *Streamer
	cache     *SegmentCache
	logger    zerolog.Logger
	ready     chan struct{}
}

// New creates a new usenet client
func New() Usenet {
	cfg := config.Get()
	usenetConfig := cfg.Usenet
	if usenetConfig == nil || len(usenetConfig.Providers) == 0 {
		// No usenet providers configured, return nil
		return nil
	}
	_logger := logger.New("usenet")
	client, err := nntp.NewClient(usenetConfig.Providers)
	if err != nil {
		_logger.Error().Err(err).Msg("Failed to create usenet client")
		return nil
	}
	store := NewStore(cfg, _logger)
	processor, err := NewProcessor(usenetConfig, _logger, store, client)
	if err != nil {
		_logger.Error().Err(err).Msg("Failed to create usenet processor")
		return nil
	}

	// Create cache and components
	cache := NewSegmentCache(_logger)
	parser := NewNZBParser(client, cache, _logger)
	streamer := NewStreamer(client, cache, store, usenetConfig.Chunks, _logger)

	return &usenet{
		store:     store,
		client:    client,
		processor: processor,
		parser:    parser,
		streamer:  streamer,
		cache:     cache,
		logger:    _logger,
		ready:     make(chan struct{}),
	}
}

func (c *usenet) Start(ctx context.Context) error {
	// Init the client
	if err := c.client.InitPools(); err != nil {
		c.logger.Error().Err(err).Msg("Failed to initialize usenet client pools")
		return fmt.Errorf("failed to initialize usenet client pools: %w", err)
	}
	// Initialize the store
	if err := c.store.Load(); err != nil {
		c.logger.Error().Err(err).Msg("Failed to initialize usenet store")
		return fmt.Errorf("failed to initialize usenet store: %w", err)
	}
	close(c.ready)
	c.logger.Info().Msg("Usenet client initialized")
	return nil
}

func (c *usenet) IsReady() chan struct{} {
	return c.ready
}

func (c *usenet) Store() Store {
	return c.store
}

func (c *usenet) Client() *nntp.Client {
	return c.client
}

func (c *usenet) Logger() zerolog.Logger {
	return c.logger
}

func (c *usenet) ProcessNZB(ctx context.Context, req *ProcessRequest) (*NZB, error) {
	return c.processor.Process(ctx, req)
}

// GetNZB retrieves an NZB by ID
func (c *usenet) GetNZB(nzoID string) *NZB {
	return c.store.Get(nzoID)
}

// DeleteNZB deletes an NZB
func (c *usenet) DeleteNZB(nzoID string) error {
	return c.store.Delete(nzoID)
}

// PauseNZB pauses an NZB download
func (c *usenet) PauseNZB(nzoID string) error {
	return c.store.UpdateStatus(nzoID, "paused")
}

// ResumeNZB resumes an NZB download
func (c *usenet) ResumeNZB(nzoID string) error {
	return c.store.UpdateStatus(nzoID, "downloading")
}

func (c *usenet) Close() {
	if c.store != nil {
		if err := c.store.Close(); err != nil {
			c.logger.Error().Err(err).Msg("Failed to close store")
		}
	}

	c.logger.Info().Msg("Usenet client closed")
}

// GetListing returns the file listing of the NZB directory
func (c *usenet) GetListing(folder string) []os.FileInfo {
	return c.store.GetListing(folder)
}

func (c *usenet) GetDownloadByteRange(nzoID string, filename string) (int64, int64, error) {
	return int64(0), int64(0), nil
}

func (c *usenet) RemoveNZB(nzoID string) error {
	if err := c.store.Delete(nzoID); err != nil {
		return fmt.Errorf("failed to delete NZB %s: %w", nzoID, err)
	}
	c.logger.Info().Msgf("NZB %s deleted successfully", nzoID)
	return nil
}

// Stream streams a file using the new simplified streaming system
func (c *usenet) Stream(ctx context.Context, nzbID string, filename string, start, end int64, writer io.Writer) error {
	// Get NZB from store
	nzb := c.GetNZB(nzbID)
	if nzb == nil {
		return fmt.Errorf("NZB %s not found", nzbID)
	}

	// Get file
	file := nzb.GetFileByName(filename)
	if file == nil {
		return fmt.Errorf("file %s not found in NZB %s", filename, nzbID)
	}
	if file.NzbID == "" {
		file.NzbID = nzbID // Ensure NZB ID is set for the file
	}

	// Stream using the new streamer
	return c.streamer.Stream(ctx, file, start, end, writer)
}
