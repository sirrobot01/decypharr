package usenet

import (
	"context"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sourcegraph/conc/pool"
)

// segmentResult holds a fetched segment and its index for ordered writing
type segmentResult struct {
	index int
	data  []byte
	err   error
}

// ProgressCallback is called periodically during download with progress info
// downloaded: total bytes written so far, speed: bytes per second (estimated)
type ProgressCallback func(downloaded int64, speed int64)

// Download downloads a file by fetching segments in parallel and streaming to writer in order.
// Bytes flow to the writer progressively as in-order segments complete - no waiting for all segments.
// If progressCallback is provided, it will be called after each segment write with current progress.
func (u *Usenet) Download(ctx context.Context, nzoID, filename string, writer io.Writer, progressCallback ProgressCallback) error {
	// get file metadata
	file, err := u.getFile(nzoID, filename)
	if err != nil {
		return fmt.Errorf("failed to get file: %w", err)
	}

	if len(file.Segments) == 0 {
		return fmt.Errorf("file has no segments: %s", file.Name)
	}

	// Track progress
	var completedSegments atomic.Int64
	var downloadedBytes atomic.Int64

	// Channel for segment results - buffered to allow parallel fetching ahead
	resultChan := make(chan segmentResult, max(u.processingMaxConnections, 1)*2)

	// Map to hold out-of-order segments waiting to be written
	pendingSegments := make(map[int][]byte)
	var pendingMu sync.Mutex
	nextToWrite := 0

	// Error tracking
	var writeErr error
	var writeErrMu sync.Mutex

	// Writer goroutine - writes segments in order as they arrive
	var writerWg sync.WaitGroup
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for result := range resultChan {
			if result.err != nil {
				writeErrMu.Lock()
				if writeErr == nil {
					writeErr = result.err
				}
				writeErrMu.Unlock()
				continue
			}

			pendingMu.Lock()
			pendingSegments[result.index] = result.data

			// Write all consecutive segments starting from nextToWrite
			for {
				data, exists := pendingSegments[nextToWrite]
				if !exists {
					break
				}
				delete(pendingSegments, nextToWrite)
				pendingMu.Unlock()

				// Write to output
				n, err := writer.Write(data)
				if err != nil {
					writeErrMu.Lock()
					if writeErr == nil {
						writeErr = fmt.Errorf("write failed at segment %d: %w", nextToWrite, err)
					}
					writeErrMu.Unlock()
					pendingMu.Lock()
					break
				}

				completedSegments.Add(1)
				downloaded := downloadedBytes.Add(int64(n))
				nextToWrite++

				// Call progress callback if provided
				if progressCallback != nil {
					// Estimate speed (rough: assume ~1s per segment batch)
					completed := completedSegments.Load()
					speed := downloaded / max(1, completed) * int64(max(u.processingMaxConnections, 1))
					progressCallback(downloaded, speed)
				}

				pendingMu.Lock()
			}
			pendingMu.Unlock()
		}
	}()

	// Fetch segments in parallel
	p := pool.New().WithContext(ctx).WithMaxGoroutines(max(u.processingMaxConnections, 1))

	for idx, segment := range file.Segments {
		segIdx := idx
		seg := segment

		p.Go(func(ctx context.Context) error {
			// Check for write errors
			writeErrMu.Lock()
			if writeErr != nil {
				writeErrMu.Unlock()
				return writeErr
			}
			writeErrMu.Unlock()

			// Check context
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Fetch segment using manager with failover
			var data []byte
			err := u.nntp.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
				d, e := conn.GetDecodedBody(seg.MessageID)
				data = d
				return e
			})
			if err != nil {
				resultChan <- segmentResult{index: segIdx, err: fmt.Errorf("segment %d: %w", segIdx, err)}
				return nil // Don't stop other workers
			}

			// Handle SegmentDataStart for sliced segments
			if seg.SegmentDataStart > 0 {
				if seg.SegmentDataStart >= int64(len(data)) {
					resultChan <- segmentResult{index: segIdx, err: fmt.Errorf("segment %d: offset exceeds data", segIdx)}
					return nil
				}
				data = data[seg.SegmentDataStart:]
			}

			// Trim to expected size
			if int64(len(data)) > seg.Bytes {
				data = data[:seg.Bytes]
			}

			resultChan <- segmentResult{index: segIdx, data: data}
			return nil
		})
	}

	// Wait for all fetches to complete, then close result channel
	fetchErr := p.Wait()
	close(resultChan)

	// Wait for writer to finish
	writerWg.Wait()

	// Check for errors
	if writeErr != nil {
		return writeErr
	}
	if fetchErr != nil {
		return fetchErr
	}

	u.logger.Info().
		Str("file", filename).
		Int64("bytes", downloadedBytes.Load()).
		Msg("Download complete")

	return nil
}
