package usenet

import (
	"context"
	"errors"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"golang.org/x/sync/errgroup"
	"os"
	"path/filepath"
	"time"
)

// DownloadWorker manages concurrent NZB downloads
type DownloadWorker struct {
	client       *nntp.Client
	processor    *Processor
	logger       zerolog.Logger
	skipPreCache bool   // Skip pre-caching for faster processing
	mountFolder  string // Folder where downloads are mounted
}

// DownloadJob represents a download job for an NZB
type DownloadJob struct {
	NZB         *NZB
	Action      string
	Priority    int
	Callback    func(*NZB, error)
	DownloadDir string
}

// NewDownloadWorker creates a new download worker
func NewDownloadWorker(config *config.Usenet, client *nntp.Client, processor *Processor) *DownloadWorker {

	dw := &DownloadWorker{
		processor:    processor,
		client:       client,
		logger:       logger.New("usenet-download-worker"),
		skipPreCache: config.SkipPreCache,
		mountFolder:  config.MountFolder,
	}
	return dw
}

func (dw *DownloadWorker) CheckAvailability(ctx context.Context, job *DownloadJob) error {
	dw.logger.Debug().
		Str("nzb_id", job.NZB.ID).
		Msg("Checking NZB availability")

	// Grab first file to extract message IDs
	firstFile := job.NZB.Files[0]
	if len(firstFile.Segments) == 0 {
		return fmt.Errorf("no segments found in first file of NZB")
	}

	segments := firstFile.Segments

	// Smart sampling: check first, last, and some middle segments
	samplesToCheck := dw.getSampleSegments(segments)

	// Create error group for concurrent checking
	g, gCtx := errgroup.WithContext(ctx)

	// Limit concurrent goroutines to prevent overwhelming the NNTP server
	maxConcurrency := len(samplesToCheck)
	if maxConns := dw.client.MinimumMaxConns(); maxConns < maxConcurrency {
		maxConcurrency = maxConns
	}
	g.SetLimit(maxConcurrency)

	// Check each segment concurrently
	for i, segment := range samplesToCheck {
		segment := segment // capture loop variable
		segmentNum := i + 1

		g.Go(func() error {
			select {
			case <-gCtx.Done():
				return gCtx.Err() // Return if context is canceled
			default:
			}
			conn, cleanup, err := dw.client.GetConnection(gCtx)
			if err != nil {
				return fmt.Errorf("failed to get NNTP connection: %w", err)
			}
			defer cleanup() // Ensure connection is returned to the pool
			// Check segment availability
			seg, err := conn.GetSegment(segment.MessageID, segmentNum)

			if err != nil {
				return fmt.Errorf("failed to check segment %d availability: %w", segmentNum, err)
			}
			if seg == nil {
				return fmt.Errorf("segment %d not found", segmentNum)
			}

			return nil
		})
	}

	// Wait for all checks to complete
	if err := g.Wait(); err != nil {
		return fmt.Errorf("availability check failed: %w", err)
	}

	// Update storage with availability info
	if err := dw.processor.store.Update(job.NZB); err != nil {
		dw.logger.Warn().Err(err).Msg("Failed to update NZB with availability info")
	}

	return nil
}

func (dw *DownloadWorker) Process(ctx context.Context, job *DownloadJob) error {
	var (
		finalPath string
		err       error
	)

	defer func(err error) {
		if job.Callback != nil {
			job.Callback(job.NZB, err)
		}
	}(err)

	switch job.Action {
	case "download":
		finalPath, err = dw.downloadNZB(ctx, job)
	case "symlink":
		finalPath, err = dw.symlinkNZB(ctx, job)
	case "none":
		return nil
	default:
		// Use symlink as default action
		finalPath, err = dw.symlinkNZB(ctx, job)
	}
	if err != nil {
		return err
	}

	if finalPath == "" {
		err = fmt.Errorf("final path is empty after processing job: %s", job.Action)
		return err
	}

	// Use atomic transition to completed state
	return dw.processor.store.MarkAsCompleted(job.NZB.ID, finalPath)
}

// downloadNZB downloads an NZB to the specified directory
func (dw *DownloadWorker) downloadNZB(ctx context.Context, job *DownloadJob) (string, error) {
	dw.logger.Info().
		Str("nzb_id", job.NZB.ID).
		Str("download_dir", job.DownloadDir).
		Msg("Starting NZB download")

	// TODO: implement download logic

	return job.DownloadDir, nil
}

// getSampleMessageIDs returns a smart sample of message IDs to check
func (dw *DownloadWorker) getSampleSegments(segments []NZBSegment) []NZBSegment {
	totalSegments := len(segments)

	// For small NZBs, check all segments
	if totalSegments <= 2 {
		return segments
	}

	var samplesToCheck []NZBSegment
	// Always check the first and last segments
	samplesToCheck = append(samplesToCheck, segments[0])               // First segment
	samplesToCheck = append(samplesToCheck, segments[totalSegments-1]) // Last segment
	return samplesToCheck
}

func (dw *DownloadWorker) symlinkNZB(ctx context.Context, job *DownloadJob) (string, error) {
	dw.logger.Info().
		Str("nzb_id", job.NZB.ID).
		Str("symlink_dir", job.DownloadDir).
		Msg("Creating symlinks for NZB")
	if job.NZB == nil {
		return "", fmt.Errorf("NZB is nil")
	}

	mountFolder := filepath.Join(dw.mountFolder, job.NZB.Name) // e.g. /mnt/rclone/usenet/__all__/TV_SHOW
	if mountFolder == "" {
		return "", fmt.Errorf("mount folder is empty")
	}
	symlinkPath := filepath.Join(job.DownloadDir, job.NZB.Name) // e.g. /mnt/symlinks/usenet/sonarr/TV_SHOW
	if err := os.MkdirAll(symlinkPath, 0755); err != nil {
		return "", fmt.Errorf("failed to create symlink directory: %w", err)
	}

	return dw.createSymlinksWebdav(job.NZB, mountFolder, symlinkPath)
}

func (dw *DownloadWorker) createSymlinksWebdav(nzb *NZB, mountPath, symlinkPath string) (string, error) {
	files := nzb.GetFiles()
	remainingFiles := make(map[string]NZBFile)
	for _, file := range files {
		remainingFiles[file.Name] = file
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	timeout := time.After(30 * time.Minute)
	filePaths := make([]string, 0, len(files))
	maxLogCount := 10 // Limit the number of log messages to avoid flooding

	for len(remainingFiles) > 0 {
		select {
		case <-ticker.C:
			entries, err := os.ReadDir(mountPath)
			if err != nil {
				if maxLogCount > 0 && !errors.Is(err, os.ErrNotExist) {
					// Only log if it's not a "not found" error
					// This is due to the fact the mount path may not exist YET
					dw.logger.Warn().
						Err(err).
						Str("mount_path", mountPath).
						Msg("Failed to read directory, retrying")
					maxLogCount--
				}
				continue
			}

			// Check which files exist in this batch
			for _, entry := range entries {
				filename := entry.Name()
				dw.logger.Info().
					Str("filename", filename).
					Msg("Checking file existence in mount path")

				if file, exists := remainingFiles[filename]; exists {
					fullFilePath := filepath.Join(mountPath, filename)
					fileSymlinkPath := filepath.Join(symlinkPath, file.Name)

					if err := os.Symlink(fullFilePath, fileSymlinkPath); err != nil && !os.IsExist(err) {
						dw.logger.Debug().Msgf("Failed to create symlink: %s: %v", fileSymlinkPath, err)
					} else {
						filePaths = append(filePaths, fileSymlinkPath)
						delete(remainingFiles, filename)
						dw.logger.Info().Msgf("File is ready: %s", file.Name)
					}
				}
			}

		case <-timeout:
			dw.logger.Warn().Msgf("Timeout waiting for files, %d files still pending", len(remainingFiles))
			return symlinkPath, fmt.Errorf("timeout waiting for files")
		}
	}

	if dw.skipPreCache {
		return symlinkPath, nil
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				dw.logger.Error().
					Interface("panic", r).
					Str("nzbName", nzb.Name).
					Msg("Recovered from panic in pre-cache goroutine")
			}
		}()
		if err := utils.PreCacheFile(filePaths); err != nil {
			dw.logger.Error().Msgf("Failed to pre-cache file: %s", err)
		} else {
			dw.logger.Debug().Msgf("Pre-cached %d files", len(filePaths))
		}
	}() // Pre-cache the files in the background
	// Pre-cache the first 256KB and 1MB of the file
	return symlinkPath, nil
}
