package usenet

import (
	"bytes"
	"context"
	"fmt"
	"github.com/nwaples/rardecode/v2"
	"github.com/sirrobot01/decypharr/internal/utils"
	"io"
	"strings"
	"time"
)

type RarParser struct {
	streamer *Streamer
}

func NewRarParser(s *Streamer) *RarParser {
	return &RarParser{streamer: s}
}

func (p *RarParser) ExtractFileRange(ctx context.Context, file *NZBFile, password string, start, end int64, writer io.Writer) error {
	info, err := p.getFileInfo(ctx, file, password)
	if err != nil {
		return fmt.Errorf("failed to get file info: %w", err)
	}

	requiredSegments := p.calculateSmartSegmentRanges(file, info, start, end)
	return p.extract(ctx, requiredSegments, password, info.FileName, start, end, writer)
}

func (p *RarParser) calculateSmartSegmentRanges(file *NZBFile, fileInfo *ExtractedFileInfo, start, end int64) []SegmentRange {
	totalSegments := len(file.Segments)

	// For store compression, be more conservative with seeking
	compressionOverhead := 1.1 // Increase to 10% overhead

	estimatedArchiveStart := int64(float64(start) * compressionOverhead)
	estimatedArchiveEnd := int64(float64(end) * compressionOverhead)

	startSegmentIndex := int(float64(estimatedArchiveStart) / float64(fileInfo.ArchiveSize) * float64(totalSegments))
	endSegmentIndex := int(float64(estimatedArchiveEnd) / float64(fileInfo.ArchiveSize) * float64(totalSegments))

	// More conservative buffers for seeking
	if start > 0 {
		// For seeking, always include more context
		headerBuffer := min(10, startSegmentIndex) // Up to 10 segments back
		startSegmentIndex = max(0, startSegmentIndex-headerBuffer)
	} else {
		startSegmentIndex = 0
	}

	// Larger end buffer for segment boundaries and RAR footer
	endBuffer := 10 + int(float64(totalSegments)*0.02) // 2% of total segments as buffer
	endSegmentIndex = min(totalSegments-1, endSegmentIndex+endBuffer)

	// Ensure minimum segment count for seeking
	minSegmentsForSeek := 20
	if endSegmentIndex-startSegmentIndex < minSegmentsForSeek {
		endSegmentIndex = min(totalSegments-1, startSegmentIndex+minSegmentsForSeek)
	}

	return convertSegmentIndicesToRanges(file, startSegmentIndex, endSegmentIndex)
}

func (p *RarParser) extract(ctx context.Context, segmentRanges []SegmentRange, password, targetFileName string, start, end int64, writer io.Writer) error {
	pipeReader, pipeWriter := io.Pipe()

	extractionErr := make(chan error, 1)
	streamingErr := make(chan error, 1)

	// RAR extraction goroutine
	go func() {
		defer func() {
			pipeReader.Close()
			if r := recover(); r != nil {
				extractionErr <- fmt.Errorf("extraction panic: %v", r)
			}
		}()

		rarReader, err := rardecode.NewReader(pipeReader, rardecode.Password(password))
		if err != nil {
			extractionErr <- fmt.Errorf("failed to create RAR reader: %w", err)
			return
		}

		found := false
		for {
			select {
			case <-ctx.Done():
				extractionErr <- ctx.Err()
				return
			default:
			}

			header, err := rarReader.Next()
			if err == io.EOF {
				if !found {
					extractionErr <- fmt.Errorf("target file %s not found in downloaded segments", targetFileName)
				} else {
					extractionErr <- fmt.Errorf("reached EOF before completing range extraction")
				}
				return
			}
			if err != nil {
				extractionErr <- fmt.Errorf("failed to read RAR header: %w", err)
				return
			}

			if header.Name == targetFileName || utils.IsMediaFile(header.Name) {
				found = true
				err = p.extractRangeFromReader(ctx, rarReader, start, end, writer)
				extractionErr <- err
				return
			} else if !header.IsDir {
				err = p.skipFileEfficiently(ctx, rarReader)
				if err != nil && ctx.Err() == nil {
					extractionErr <- fmt.Errorf("failed to skip file %s: %w", header.Name, err)
					return
				}
			}
		}
	}()

	// Streaming goroutine
	go func() {
		defer pipeWriter.Close()
		err := p.streamer.stream(ctx, segmentRanges, pipeWriter)
		streamingErr <- err
	}()

	// Wait with longer timeout for seeking operations
	select {
	case err := <-extractionErr:
		return err
	case err := <-streamingErr:
		if err != nil && !p.isSkippableError(err) {
			return fmt.Errorf("segment streaming failed: %w", err)
		}
		// Longer timeout for seeking operations
		select {
		case err := <-extractionErr:
			return err
		case <-time.After(30 * time.Second): // Increased from 5 seconds
			return fmt.Errorf("extraction timeout after 30 seconds")
		}
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *RarParser) extractRangeFromReader(ctx context.Context, reader io.Reader, start, end int64, writer io.Writer) error {
	// Skip to start position efficiently
	if start > 0 {
		skipped, err := p.smartSkip(ctx, reader, start)
		if err != nil {
			return fmt.Errorf("failed to skip to position %d (skipped %d): %w", start, skipped, err)
		}
	}

	// Copy requested range
	bytesToCopy := end - start + 1
	copied, err := p.smartCopy(ctx, writer, reader, bytesToCopy)
	if err != nil && err != io.EOF {
		return fmt.Errorf("failed to copy range (copied %d/%d): %w", copied, bytesToCopy, err)
	}

	return nil
}

func (p *RarParser) smartSkip(ctx context.Context, reader io.Reader, bytesToSkip int64) (int64, error) {
	const skipBufferSize = 64 * 1024 // Larger buffer for skipping

	buffer := make([]byte, skipBufferSize)
	var totalSkipped int64

	for totalSkipped < bytesToSkip {
		select {
		case <-ctx.Done():
			return totalSkipped, ctx.Err()
		default:
		}

		toRead := skipBufferSize
		if remaining := bytesToSkip - totalSkipped; remaining < int64(toRead) {
			toRead = int(remaining)
		}

		n, err := reader.Read(buffer[:toRead])
		if n > 0 {
			totalSkipped += int64(n)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return totalSkipped, err
		}
	}

	return totalSkipped, nil
}

func (p *RarParser) smartCopy(ctx context.Context, dst io.Writer, src io.Reader, bytesToCopy int64) (int64, error) {
	const copyBufferSize = 32 * 1024

	buffer := make([]byte, copyBufferSize)
	var totalCopied int64

	for totalCopied < bytesToCopy {
		select {
		case <-ctx.Done():
			return totalCopied, ctx.Err()
		default:
		}

		toRead := copyBufferSize
		if remaining := bytesToCopy - totalCopied; remaining < int64(toRead) {
			toRead = int(remaining)
		}

		n, err := src.Read(buffer[:toRead])
		if n > 0 {
			written, writeErr := dst.Write(buffer[:n])
			if writeErr != nil {
				return totalCopied, writeErr
			}
			totalCopied += int64(written)
		}

		if err != nil {
			if err == io.EOF {
				break
			}
			return totalCopied, err
		}
	}

	return totalCopied, nil
}

func (p *RarParser) skipFileEfficiently(ctx context.Context, reader io.Reader) error {
	_, err := p.smartSkip(ctx, reader, 1<<62) // Very large number
	if err == io.EOF {
		return nil // EOF is expected when skipping
	}
	return err
}

func (p *RarParser) getFileInfo(ctx context.Context, file *NZBFile, password string) (*ExtractedFileInfo, error) {
	headerSegments := p.getMinimalHeaders(file)

	var headerBuffer bytes.Buffer
	err := p.streamer.stream(ctx, headerSegments, &headerBuffer)
	if err != nil {
		return nil, fmt.Errorf("failed to download headers: %w", err)
	}

	reader := bytes.NewReader(headerBuffer.Bytes())
	rarReader, err := rardecode.NewReader(reader, rardecode.Password(password))
	if err != nil {
		return nil, fmt.Errorf("failed to create RAR reader (check password): %w", err)
	}

	totalArchiveSize := p.calculateTotalSize(file.SegmentSize, file.Segments)

	for {
		header, err := rarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			continue
		}

		if !header.IsDir && utils.IsMediaFile(header.Name) {
			return &ExtractedFileInfo{
				FileName:    header.Name,
				FileSize:    header.UnPackedSize,
				ArchiveSize: totalArchiveSize,
			}, nil
		}
	}

	return nil, fmt.Errorf("no media file found in RAR archive")
}

func (p *RarParser) getMinimalHeaders(file *NZBFile) []SegmentRange {
	headerCount := min(len(file.Segments), 4) // Minimal for password+headers
	return file.ConvertToSegmentRanges(file.Segments[:headerCount])
}

func (p *RarParser) calculateTotalSize(segmentSize int64, segments []NZBSegment) int64 {
	total := int64(0)
	for i, seg := range segments {
		if segmentSize <= 0 {
			segmentSize = seg.Bytes // Fallback to actual segment size if not set
		}
		if i == len(segments)-1 {
			segmentSize = seg.Bytes // Last segment uses actual size
		}
		total += segmentSize
	}
	return total
}

func (p *RarParser) isSkippableError(err error) bool {
	if err == nil {
		return true
	}
	errStr := err.Error()
	return strings.Contains(errStr, "client disconnected") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset")
}

func convertSegmentIndicesToRanges(file *NZBFile, startIndex, endIndex int) []SegmentRange {
	var segmentRanges []SegmentRange

	for i := startIndex; i <= endIndex && i < len(file.Segments); i++ {
		segment := file.Segments[i]

		// For RAR files, we want the entire segment (no partial byte ranges)
		segmentRange := SegmentRange{
			Segment:    segment,
			ByteStart:  0,                 // Always start at beginning of segment
			ByteEnd:    segment.Bytes - 1, // Always go to end of segment
			TotalStart: 0,                 // Not used for this approach
			TotalEnd:   segment.Bytes - 1, // Not used for this approach
		}

		segmentRanges = append(segmentRanges, segmentRange)
	}

	return segmentRanges
}
