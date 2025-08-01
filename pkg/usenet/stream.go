package usenet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/chrisfarms/yenc"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"io"
	"net/http"
	"sync"
	"time"
)

var groupCache = sync.Map{}

type Streamer struct {
	logger       zerolog.Logger
	client       *nntp.Client
	store        Store
	cache        *SegmentCache
	chunkSize    int
	maxRetries   int
	retryDelayMs int
}

type segmentResult struct {
	index int
	data  []byte
	err   error
}

type FlushingWriter struct {
	writer io.Writer
}

func (fw *FlushingWriter) Write(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}

	written, err := fw.writer.Write(data)
	if err != nil {
		return written, err
	}

	if written != len(data) {
		return written, io.ErrShortWrite
	}

	// Auto-flush if possible
	if flusher, ok := fw.writer.(http.Flusher); ok {
		flusher.Flush()
	}

	return written, nil
}

func (fw *FlushingWriter) WriteAndFlush(data []byte) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	written, err := fw.Write(data)
	return int64(written), err
}

func (fw *FlushingWriter) WriteString(s string) (int, error) {
	return fw.Write([]byte(s))
}

func (fw *FlushingWriter) WriteBytes(data []byte) (int, error) {
	return fw.Write(data)
}

func NewStreamer(client *nntp.Client, cache *SegmentCache, store Store, chunkSize int, logger zerolog.Logger) *Streamer {
	return &Streamer{
		logger:       logger.With().Str("component", "streamer").Logger(),
		cache:        cache,
		store:        store,
		client:       client,
		chunkSize:    chunkSize,
		maxRetries:   3,
		retryDelayMs: 2000,
	}
}

func (s *Streamer) Stream(ctx context.Context, file *NZBFile, start, end int64, writer io.Writer) error {
	if file == nil {
		return fmt.Errorf("file cannot be nil")
	}

	if start < 0 {
		start = 0
	}

	if err := s.getSegmentSize(ctx, file); err != nil {
		return fmt.Errorf("failed to get segment size: %w", err)
	}

	if file.IsRarArchive {
		return s.streamRarExtracted(ctx, file, start, end, writer)
	}
	if end >= file.Size {
		end = file.Size - 1
	}
	if start > end {
		return fmt.Errorf("invalid range: start=%d > end=%d", start, end)
	}

	ranges := file.GetSegmentsInRange(file.SegmentSize, start, end)
	if len(ranges) == 0 {
		return fmt.Errorf("no segments found for range [%d, %d]", start, end)
	}

	writer = &FlushingWriter{writer: writer}
	return s.stream(ctx, ranges, writer)
}

func (s *Streamer) streamRarExtracted(ctx context.Context, file *NZBFile, start, end int64, writer io.Writer) error {
	parser := NewRarParser(s)
	return parser.ExtractFileRange(ctx, file, file.Password, start, end, writer)
}

func (s *Streamer) stream(ctx context.Context, ranges []SegmentRange, writer io.Writer) error {
	chunkSize := s.chunkSize

	for i := 0; i < len(ranges); i += chunkSize {
		end := min(i+chunkSize, len(ranges))
		chunk := ranges[i:end]

		// Download chunk concurrently
		results := make([]segmentResult, len(chunk))
		var wg sync.WaitGroup

		for j, segRange := range chunk {
			wg.Add(1)
			go func(idx int, sr SegmentRange) {
				defer wg.Done()
				data, err := s.processSegment(ctx, sr)
				results[idx] = segmentResult{index: idx, data: data, err: err}
			}(j, segRange)
		}

		wg.Wait()

		// Write chunk sequentially
		for j, result := range results {
			if result.err != nil {
				return fmt.Errorf("segment %d failed: %w", i+j, result.err)
			}

			if len(result.data) > 0 {
				_, err := writer.Write(result.data)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}

func (s *Streamer) processSegment(ctx context.Context, segRange SegmentRange) ([]byte, error) {
	segment := segRange.Segment
	// Try cache first
	if s.cache != nil {
		if cached, found := s.cache.Get(segment.MessageID); found {
			return s.extractRangeFromSegment(cached.Data, segRange)
		}
	}

	// Download with retries
	decodedData, err := s.downloadSegmentWithRetry(ctx, segment)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}

	// Cache full segment for future seeks
	if s.cache != nil {
		s.cache.Put(segment.MessageID, decodedData, segment.Bytes)
	}

	// Extract the specific range from this segment
	return s.extractRangeFromSegment(decodedData.Body, segRange)
}

func (s *Streamer) extractRangeFromSegment(data []byte, segRange SegmentRange) ([]byte, error) {
	// Use the segment range's pre-calculated offsets
	startOffset := segRange.ByteStart
	endOffset := segRange.ByteEnd + 1 // ByteEnd is inclusive, we need exclusive for slicing

	// Bounds check
	if startOffset < 0 || startOffset >= int64(len(data)) {
		return []byte{}, nil
	}

	if endOffset > int64(len(data)) {
		endOffset = int64(len(data))
	}

	if startOffset >= endOffset {
		return []byte{}, nil
	}

	// Extract the range
	result := make([]byte, endOffset-startOffset)
	copy(result, data[startOffset:endOffset])

	return result, nil
}

func (s *Streamer) downloadSegmentWithRetry(ctx context.Context, segment NZBSegment) (*yenc.Part, error) {
	var lastErr error

	for attempt := 0; attempt < s.maxRetries; attempt++ {
		// Check cancellation before each retry
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if attempt > 0 {
			delay := time.Duration(s.retryDelayMs*(1<<(attempt-1))) * time.Millisecond
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		data, err := s.downloadSegment(ctx, segment)
		if err == nil {
			return data, nil
		}

		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
	}

	return nil, fmt.Errorf("segment download failed after %d attempts: %w", s.maxRetries, lastErr)
}

// Updated to work with NZBSegment from SegmentRange
func (s *Streamer) downloadSegment(ctx context.Context, segment NZBSegment) (*yenc.Part, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	downloadCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	conn, cleanup, err := s.client.GetConnection(downloadCtx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if segment.Group != "" {
		if _, exists := groupCache.Load(segment.Group); !exists {
			if _, err := conn.SelectGroup(segment.Group); err != nil {
				return nil, fmt.Errorf("failed to select group %s: %w", segment.Group, err)
			}
			groupCache.Store(segment.Group, true)
		}
	}

	body, err := conn.GetBody(segment.MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get body for message %s: %w", segment.MessageID, err)
	}

	if body == nil || len(body) == 0 {
		return nil, fmt.Errorf("no body found for message %s", segment.MessageID)
	}

	data, err := nntp.DecodeYenc(bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to decode yEnc: %w", err)
	}

	// Adjust begin offset
	data.Begin -= 1

	return data, nil
}

func (s *Streamer) copySegmentData(writer io.Writer, data []byte) (int64, error) {
	if len(data) == 0 {
		return 0, nil
	}

	reader := bytes.NewReader(data)
	written, err := io.CopyN(writer, reader, int64(len(data)))
	if err != nil {
		return 0, fmt.Errorf("copyN failed %w", err)
	}

	if written != int64(len(data)) {
		return 0, fmt.Errorf("expected to copy %d bytes, only copied %d", len(data), written)
	}

	if fl, ok := writer.(http.Flusher); ok {
		fl.Flush()
	}

	return written, nil
}

func (s *Streamer) extractRangeWithGapHandling(data []byte, segStart, segEnd int64, globalStart, globalEnd int64) ([]byte, error) {
	// Calculate intersection using actual bounds
	intersectionStart := max(segStart, globalStart)
	intersectionEnd := min(segEnd, globalEnd+1) // +1 because globalEnd is inclusive

	// No overlap
	if intersectionStart >= intersectionEnd {
		return []byte{}, nil
	}

	// Calculate offsets within the actual data
	offsetInData := intersectionStart - segStart
	dataLength := intersectionEnd - intersectionStart
	// Bounds check
	if offsetInData < 0 || offsetInData >= int64(len(data)) {
		return []byte{}, nil
	}

	endOffset := offsetInData + dataLength
	if endOffset > int64(len(data)) {
		endOffset = int64(len(data))
		dataLength = endOffset - offsetInData
	}

	if dataLength <= 0 {
		return []byte{}, nil
	}

	// Extract the range
	result := make([]byte, dataLength)
	copy(result, data[offsetInData:endOffset])

	return result, nil
}

func (s *Streamer) getSegmentSize(ctx context.Context, file *NZBFile) error {
	if file.SegmentSize > 0 {
		return nil
	}
	if len(file.Segments) == 0 {
		return fmt.Errorf("no segments available for file %s", file.Name)
	}
	// Fetch the segment size and then store it in the file
	firstSegment := file.Segments[0]
	firstInfo, err := s.client.DownloadHeader(ctx, firstSegment.MessageID)
	if err != nil {
		return err
	}

	chunkSize := firstInfo.End - (firstInfo.Begin - 1)
	if chunkSize <= 0 {
		return fmt.Errorf("invalid segment size for file %s: %d", file.Name, chunkSize)
	}
	file.SegmentSize = chunkSize
	return s.store.UpdateFile(file.NzbID, file)
}
