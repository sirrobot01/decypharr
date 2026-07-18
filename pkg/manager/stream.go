package manager

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/retry"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

const (
	streamBufferSize = 256 * 1024
)

// streamBufPool provides reusable buffers for streaming to reduce GC pressure.
// Each buffer is 256KB - prevents per-request allocations.
var streamBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, streamBufferSize)
		return &buf
	},
}

// ActiveStream represents a currently active streaming file
type ActiveStream struct {
	ID         string `json:"id"`
	EntryName  string `json:"entry_name"`
	FileName   string `json:"file_name"`
	FileSize   int64  `json:"file_size"`
	Source     string `json:"source"` // "torrent" or "nzb"
	StartedAt  int64  `json:"started_at"`
	LastActive int64  `json:"last_active"` // Last activity timestamp (for observability)
	Debrid     string `json:"debrid,omitempty"`
	Client     string `json:"client,omitempty"` // Client identifier (User-Agent for WebDAV, "DFS" for DFS)
}

// === Active Streams Tracking ===

// registerStream registers an active stream for observability.
// Returns the stream ID so the caller can remove it when streaming completes.
func (m *Manager) registerStream(entryName, fileName string, fileSize int64, source, debrid, client string) string {
	// Use deterministic ID to ensure a single entry per file
	streamID := entryName + ":" + fileName
	now := utils.NowUnix()

	stream := &ActiveStream{
		ID:         streamID,
		EntryName:  entryName,
		FileName:   fileName,
		FileSize:   fileSize,
		Source:     source,
		StartedAt:  now,
		LastActive: now,
		Debrid:     debrid,
		Client:     client,
	}

	m.activeStreams.Store(streamID, stream)
	return streamID
}

// unregisterStream removes an active stream entry if it exists.
func (m *Manager) unregisterStream(streamID string) {
	if streamID == "" {
		return
	}
	m.activeStreams.Delete(streamID)
}

// GetActiveStreams returns all currently active streams.
func (m *Manager) GetActiveStreams() []*ActiveStream {
	var streams []*ActiveStream
	m.activeStreams.Range(func(_ string, stream *ActiveStream) bool {
		streams = append(streams, stream)
		return true
	})
	return streams
}

// GetActiveStreamsCount returns the number of active streams.
func (m *Manager) GetActiveStreamsCount() int {
	return m.activeStreams.Size()
}

type StreamError struct {
	Err       error
	Retryable bool
	LinkError bool // true if we should try a new link
}

func (e StreamError) Error() string {
	return e.Err.Error()
}

// StreamMetadata describes the headers/status for a streaming response before data flows.
type StreamMetadata struct {
	Header        http.Header
	StatusCode    int
	ContentLength int64
}

// StreamReadyFunc allows callers to copy headers/status before streaming begins.
type StreamReadyFunc func(*StreamMetadata) error

// isConnectionError checks if the error is related to connection issues
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Check for common connection errors
	if strings.Contains(errStr, "EOF") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection refused") {
		return true
	}

	// Check for net.Error types
	var netErr net.Error
	return errors.As(err, &netErr)
}

// Stream streams a file from an entry to the provided writer within the specified byte range.
// client identifies the caller (e.g., User-Agent for WebDAV, "DFS" for DFS mount).
func (m *Manager) Stream(ctx context.Context, entry *storage.Entry, filename string, start, end int64, writer io.Writer, onReady StreamReadyFunc, client string) error {
	if writer == nil {
		return fmt.Errorf("writer is nil")
	}

	// get file info for size
	file, ok := entry.Files[filename]
	if !ok {
		return retry.Unrecoverable(fmt.Errorf("file %s not found", filename))
	}
	start, end, err := normalizeStreamRange(file.Size, start, end)

	if err != nil {
		return retry.Unrecoverable(fmt.Errorf("invalid stream range for file %s: %w", filename, err))
	}

	// Route based on protocol
	if entry.Protocol == config.ProtocolNZB {
		return m.streamUsenet(ctx, entry, filename, start, end, writer, onReady)
	}

	// Default to HTTP streaming for torrents
	return m.streamHTTP(ctx, entry, filename, start, end, writer, onReady)
}

// TrackStream registers an active stream for observability and returns the stream ID.
// Call UntrackStream with the returned ID when streaming completes.
func (m *Manager) TrackStream(entry *storage.Entry, filename, client string) string {
	if entry == nil {
		return ""
	}
	file, ok := entry.Files[filename]
	if !ok {
		return ""
	}

	var source, debrid string
	if entry.Protocol == config.ProtocolNZB {
		source = "nzb"
	} else {
		source = "torrent"
		debrid = entry.ActiveProvider
	}

	return m.registerStream(entry.Name, filename, file.Size, source, debrid, client)
}

// UntrackStream removes a previously-registered active stream if the ID is non-empty.
func (m *Manager) UntrackStream(streamID string) {
	m.unregisterStream(streamID)
}

// streamHTTP handles streaming for torrent files via HTTP
func (m *Manager) streamHTTP(ctx context.Context, torrent *storage.Entry, filename string, start, end int64, writer io.Writer, onReady StreamReadyFunc) error {
	file, ok := torrent.Files[filename]
	if !ok {
		return fmt.Errorf("file not found in entry: %s", filename)
	}

	expectedLen := end - start + 1

	// Get the validated download link using the link service
	downloadLink, err := m.linkService.GetLink(ctx, torrent, filename)
	if err != nil {
		return fmt.Errorf("failed to get download link: %w", err)
	}

	// Get buffer from pool - reduces GC pressure significantly
	bufPtr := streamBufPool.Get().(*[]byte)
	buf := *bufPtr
	defer streamBufPool.Put(bufPtr)

	resp, reqErr := m.doRequest(ctx, downloadLink.DownloadLink, start, end)
	if reqErr != nil {
		// Network/connection error - retriable
		return reqErr
	}

	// Got response - check status
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		var header http.Header
		if onReady != nil {
			header = resp.Header.Clone()
		}
		meta := &StreamMetadata{
			Header:        header,
			StatusCode:    resp.StatusCode,
			ContentLength: resp.ContentLength,
		}

		isPartial := expectedLen > 0 && (start > 0 || end < file.Size-1)
		if expectedLen > 0 {
			meta.ContentLength = expectedLen
			if header != nil {
				header["Content-Length"] = []string{strconv.FormatInt(expectedLen, 10)}
			}
		}
		if isPartial && resp.StatusCode == http.StatusOK {
			meta.StatusCode = http.StatusPartialContent
			if header != nil {
				header["Content-Range"] = []string{buildContentRange(start, end, file.Size)}
			}
		}

		if onReady != nil {
			if readyErr := onReady(meta); readyErr != nil {
				resp.Body.Close()
				return retry.Unrecoverable(readyErr)
			}
		}

		// Stream response body into provided writer
		reader := io.Reader(resp.Body)
		if expectedLen > 0 {
			reader = io.LimitReader(resp.Body, expectedLen)
		}
		n, copyErr := io.CopyBuffer(writer, reader, buf)
		resp.Body.Close()

		if expectedLen > 0 && n < expectedLen && copyErr == nil {
			copyErr = io.ErrUnexpectedEOF
		}

		if copyErr != nil && copyErr != io.EOF {
			// Check if this is a retriable error (timeout, network issue)
			// vs a permanent error (context cancelled by user)
			if ctx.Err() != nil {
				// User/system cancelled - don't retry
				return retry.Unrecoverable(ctx.Err())
			}
			if isConnectionError(copyErr) || strings.Contains(copyErr.Error(), "timeout") {
				// Network/timeout error - retriable
				return copyErr
			}
			// Unknown error - don't retry to avoid infinite loops
			return retry.Unrecoverable(copyErr)
		}
		return nil
	}

	resp.Body.Close()
	return retry.Unrecoverable(StreamError{
		Err:       fmt.Errorf("unexpected HTTP status: %d", resp.StatusCode),
		Retryable: false,
		LinkError: false,
	})
}

// streamUsenet handles streaming for NZB files via usenet
func (m *Manager) streamUsenet(ctx context.Context, entry *storage.Entry, filename string, start, end int64, writer io.Writer, onReady StreamReadyFunc) error {
	if m.usenet == nil {
		return retry.Unrecoverable(fmt.Errorf("usenet client not configured"))
	}

	file, ok := entry.Files[filename]
	if !ok {
		return retry.Unrecoverable(fmt.Errorf("file not found in entry: %s", filename))
	}

	contentLength := end - start + 1

	// Only build headers if onReady callback is provided (avoids allocations for DFS streaming)
	if onReady != nil {
		statusCode := http.StatusOK
		header := make(http.Header, 4) // Pre-size to avoid rehashing
		header["Accept-Ranges"] = []string{"bytes"}
		header["Content-Length"] = []string{strconv.FormatInt(contentLength, 10)}
		if start > 0 || end < file.Size-1 {
			statusCode = http.StatusPartialContent
			header["Content-Range"] = []string{buildContentRange(start, end, file.Size)}
		}
		header["Content-Type"] = []string{utils.GetContentType(filename)}

		if err := onReady(&StreamMetadata{
			Header:        header,
			StatusCode:    statusCode,
			ContentLength: contentLength,
		}); err != nil {
			return err
		}
	}

	// Stream NZB content directly into writer
	return m.usenet.Stream(ctx, entry.InfoHash, filename, start, end, writer)
}

func normalizeStreamRange(size, start, end int64) (int64, int64, error) {
	if size <= 0 {
		return 0, 0, fmt.Errorf("invalid file size %d", size)
	}

	if start < 0 {
		start = 0
	}
	if end == -1 || end >= size {
		end = size - 1
	}
	if start >= size {
		return 0, 0, fmt.Errorf("requested start %d beyond file size %d", start, size)
	}
	if end < start {
		return 0, 0, fmt.Errorf("invalid byte range %d-%d", start, end)
	}
	return start, end, nil
}

func (m *Manager) doRequest(ctx context.Context, url string, start, end int64) (*http.Response, error) {
	var resp *http.Response

	err := retry.Do(
		func() error {
			req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if reqErr != nil {
				return retry.Unrecoverable(StreamError{Err: reqErr, Retryable: false})
			}

			// Set range header
			if start > 0 || end > 0 {
				req.Header.Set("Range", buildHTTPRange(start, end))
			}

			// Set optimized headers for streaming
			req.Header.Set("Connection", "keep-alive")
			req.Header.Set("Accept-Encoding", "identity") // Disable compression for streaming
			req.Header.Set("Cache-Control", "no-cache")

			var doErr error
			resp, doErr = m.streamClient.Do(req)
			if doErr != nil {
				// Check if it's a connection error that we should retry
				if isConnectionError(doErr) {
					return doErr
				}
				return retry.Unrecoverable(StreamError{Err: doErr, Retryable: true})
			}

			// A 5xx from the CDN is a transient upstream failure, not a
			// connection error, so doErr is nil and the retry loop would exit
			// without retrying. Treat it as retryable here so retry.Do fires.
			if resp.StatusCode >= 500 {
				status := resp.StatusCode
				resp.Body.Close()
				resp = nil
				return fmt.Errorf("CDN returned HTTP %d", status)
			}
			return nil
		},
		retry.Context(ctx),
		retry.Attempts(uint(m.config.Retries)+1),
		retry.Delay(config.DefaultRetryDelay),
		retry.MaxDelay(config.DefaultRetryDelayMax),
		retry.DelayType(retry.FixedDelay),
		retry.LastErrorOnly(true),
	)

	if err != nil {
		return nil, StreamError{Err: fmt.Errorf("connection retry exhausted: %w", err), Retryable: true}
	}
	return resp, nil
}

func buildContentRange(start, end, total int64) string {
	var b strings.Builder
	b.Grow(64)
	b.WriteString("bytes ")
	b.WriteString(strconv.FormatInt(start, 10))
	b.WriteByte('-')
	if end >= start {
		b.WriteString(strconv.FormatInt(end, 10))
	} else {
		b.WriteByte('*')
	}
	b.WriteByte('/')
	b.WriteString(strconv.FormatInt(total, 10))
	return b.String()
}

func buildHTTPRange(start, end int64) string {
	var b strings.Builder
	b.Grow(32)
	b.WriteString("bytes=")
	b.WriteString(strconv.FormatInt(start, 10))
	b.WriteByte('-')
	if end >= start {
		b.WriteString(strconv.FormatInt(end, 10))
	}
	return b.String()
}
