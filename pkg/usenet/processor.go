package usenet

import (
	"context"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"path/filepath"
	"time"
)

// Processor handles NZB processing and download orchestration
type Processor struct {
	store          Store
	parser         *NZBParser
	downloadWorker *DownloadWorker
	logger         zerolog.Logger
	client         *nntp.Client
}

// ProcessRequest represents a request to process an NZB
type ProcessRequest struct {
	NZBContent  []byte
	Name        string
	Arr         *arr.Arr
	Action      string // "download", "symlink", "none"
	DownloadDir string
}

// NewProcessor creates a new usenet processor
func NewProcessor(config *config.Usenet, logger zerolog.Logger, store Store, client *nntp.Client) (*Processor, error) {
	processor := &Processor{
		store:  store,
		logger: logger.With().Str("component", "usenet-processor").Logger(),
		client: client,
	}

	// Initialize download worker
	processor.downloadWorker = NewDownloadWorker(config, client, processor)
	processor.parser = NewNZBParser(client, nil, processor.logger)

	return processor, nil
}

// Process processes an NZB for download/streaming
func (p *Processor) Process(ctx context.Context, req *ProcessRequest) (*NZB, error) {
	if len(req.NZBContent) == 0 {
		return nil, fmt.Errorf("NZB content is empty")
	}

	// Validate NZB content
	if err := ValidateNZB(req.NZBContent); err != nil {
		return nil, fmt.Errorf("invalid NZB content: %w", err)
	}
	nzb, err := p.process(ctx, req)
	if err != nil {
		p.logger.Error().
			Err(err).
			Msg("Failed to process NZB content")
		return nil, fmt.Errorf("failed to process NZB content: %w", err)
	}
	return nzb, nil
}

func (p *Processor) process(ctx context.Context, req *ProcessRequest) (*NZB, error) {
	nzb, err := p.parser.Parse(ctx, req.Name, req.Arr.Name, req.NZBContent)
	if err != nil {
		p.logger.Error().
			Err(err).
			Msg("Failed to parse NZB content")
		return nil, fmt.Errorf("failed to parse NZB content: %w", err)
	}
	if nzb == nil {
		p.logger.Error().
			Msg("Parsed NZB is nil")
		return nil, fmt.Errorf("parsed NZB is nil")
	}
	p.logger.Info().
		Str("nzb_id", nzb.ID).
		Msg("Successfully parsed NZB content")

	if existing := p.store.Get(nzb.ID); existing != nil {
		p.logger.Info().Str("nzb_id", nzb.ID).Msg("NZB already exists")
		return existing, nil
	}

	p.logger.Info().
		Str("nzb_id", nzb.ID).
		Msg("Creating new NZB download job")

	downloadDir := req.DownloadDir
	if req.Arr != nil {
		downloadDir = filepath.Join(downloadDir, req.Arr.Name)
	}

	job := &DownloadJob{
		NZB:         nzb,
		Action:      req.Action,
		DownloadDir: downloadDir,
		Callback: func(completedNZB *NZB, err error) {
			if err != nil {
				p.logger.Error().
					Err(err).
					Str("nzb_id", completedNZB.ID).
					Msg("Download job failed")
				return
			}
			p.logger.Info().
				Str("nzb_id", completedNZB.ID).
				Msg("Download job completed successfully")
		},
	}
	// Check availability before submitting the job
	//if err := p.downloadWorker.CheckAvailability(ctx, job); err != nil {
	//	p.logger.Error().
	//		Err(err).
	//		Str("nzb_id", nzb.ID).
	//		Msg("NZB availability check failed")
	//	return nil, fmt.Errorf("availability check failed for NZB %s: %w", nzb.ID, err)
	//}
	// Mark NZB as downloaded but not completed
	nzb.Downloaded = true
	nzb.AddedOn = time.Now()
	p.store.AddToQueue(nzb)

	if err := p.store.Add(nzb); err != nil {
		return nil, err
	} // Add the downloaded NZB to the store asynchronously
	p.logger.Info().
		Str("nzb_id", nzb.ID).
		Msg("NZB added to queue")

	go func() {
		if err := p.downloadWorker.Process(ctx, job); err != nil {
			p.logger.Error().
				Err(err).
				Str("nzb_id", nzb.ID).
				Msg("Failed to submit download job")
		}
	}()

	return nzb, nil
}
