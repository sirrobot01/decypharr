package webdav

import (
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/store"
)

var sharedClient = &http.Client{
	Transport: &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   50,
		MaxConnsPerHost:       100,
		IdleConnTimeout:       300 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		DisableKeepAlives:     false,
		WriteBufferSize:       64 * 1024,
		ReadBufferSize:        64 * 1024,
	},
	Timeout: 0,
}

type streamError struct {
	Err                   error
	StatusCode            int
	IsClientDisconnection bool
}

func (e *streamError) Error() string {
	return e.Err.Error()
}

func (e *streamError) Unwrap() error {
	return e.Err
}

type File struct {
	name         string
	torrentName  string
	link         string
	downloadLink string
	size         int64
	isDir        bool
	fileId       string
	isRar        bool
	metadataOnly bool
	content      []byte
	children     []os.FileInfo // For directories
	cache        *store.Cache
	modTime      time.Time

	// Minimal state for interface compliance only
	readOffset int64 // Only used for Read() method compliance
}

// File interface implementations for File

func (f *File) Close() error {
	if f.isDir {
		return nil // No resources to close for directories
	}

	// For files, we don't have any resources to close either
	// This is just to satisfy the os.File interface
	f.content = nil
	f.children = nil
	f.downloadLink = ""
	f.readOffset = 0
	return nil
}

func (f *File) getDownloadLink() (string, error) {
	// Check if we already have a final URL cached

	if f.downloadLink != "" && isValidURL(f.downloadLink) {
		return f.downloadLink, nil
	}
	downloadLink, err := f.cache.GetDownloadLink(f.torrentName, f.name, f.link)
	if err != nil {
		return "", err
	}
	if downloadLink != "" && isValidURL(downloadLink) {
		f.downloadLink = downloadLink
		return downloadLink, nil
	}
	return "", os.ErrNotExist
}

func (f *File) getDownloadByteRange() (*[2]int64, error) {
	byteRange, err := f.cache.GetDownloadByteRange(f.torrentName, f.name)
	if err != nil {
		return nil, err
	}
	return byteRange, nil
}

func (f *File) servePreloadedContent(w http.ResponseWriter, r *http.Request) error {
	content := f.content
	size := int64(len(content))

	// Handle range requests for preloaded content
	if rangeHeader := r.Header.Get("Range"); rangeHeader != "" {
		ranges, err := parseRange(rangeHeader, size)
		if err != nil || len(ranges) != 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", size))
			return &streamError{Err: fmt.Errorf("invalid range"), StatusCode: http.StatusRequestedRangeNotSatisfiable}
		}

		start, end := ranges[0].start, ranges[0].end
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", fmt.Sprintf("%d", end-start+1))
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)

		_, err = w.Write(content[start : end+1])
		return err
	}

	// Full content
	w.Header().Set("Content-Length", fmt.Sprintf("%d", size))
	w.Header().Set("Accept-Ranges", "bytes")
	w.WriteHeader(http.StatusOK)

	_, err := w.Write(content)
	return err
}

func (f *File) StreamResponse(w http.ResponseWriter, r *http.Request) error {
	// Handle preloaded content files
	if f.content != nil {
		return f.servePreloadedContent(w, r)
	}

	// Try streaming with retry logic
	return f.streamWithRetry(w, r, 0)
}

func (f *File) streamWithRetry(w http.ResponseWriter, r *http.Request, retryCount int) error {
	const maxRetries = 3
	_log := f.cache.Logger()

	// Get download link (with caching optimization)
	downloadLink, err := f.getDownloadLink()
	if err != nil {
		return &streamError{Err: err, StatusCode: http.StatusPreconditionFailed}
	}

	if downloadLink == "" {
		return &streamError{Err: fmt.Errorf("empty download link"), StatusCode: http.StatusNotFound}
	}

	// Create upstream request with streaming optimizations
	upstreamReq, err := http.NewRequest("GET", downloadLink, nil)
	if err != nil {
		return &streamError{Err: err, StatusCode: http.StatusInternalServerError}
	}

	setVideoStreamingHeaders(upstreamReq)

	// Handle range requests (critical for video seeking)
	isRangeRequest := f.handleRangeRequest(upstreamReq, r, w)
	if isRangeRequest == -1 {
		return &streamError{Err: fmt.Errorf("invalid range"), StatusCode: http.StatusRequestedRangeNotSatisfiable}
	}

	resp, err := sharedClient.Do(upstreamReq)
	if err != nil {
		return &streamError{Err: err, StatusCode: http.StatusServiceUnavailable}
	}
	defer resp.Body.Close()

	// Handle upstream errors with retry logic
	shouldRetry, retryErr := f.handleUpstream(resp, retryCount, maxRetries)
	if shouldRetry && retryCount < maxRetries {
		// Retry with new download link
		_log.Debug().
			Int("retry_count", retryCount+1).
			Str("file", f.name).
			Msg("Retrying stream request")
		return f.streamWithRetry(w, r, retryCount+1)
	}
	if retryErr != nil {
		return retryErr
	}

	setVideoResponseHeaders(w, resp, isRangeRequest == 1)

	// Stream with optimized buffering for video
	return f.streamVideoOptimized(w, resp.Body)
}

func (f *File) handleUpstream(resp *http.Response, retryCount, maxRetries int) (shouldRetry bool, err error) {
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
		return false, nil
	}

	_log := f.cache.Logger()

	// Clean up response body properly
	cleanupResp := func(resp *http.Response) {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	switch resp.StatusCode {
	case http.StatusServiceUnavailable:
		// Read the body to check for specific error messages
		body, readErr := io.ReadAll(resp.Body)
		cleanupResp(resp)

		if readErr != nil {
			_log.Error().Err(readErr).Msg("Failed to read response body")
			return false, &streamError{
				Err:        fmt.Errorf("failed to read error response: %w", readErr),
				StatusCode: http.StatusServiceUnavailable,
			}
		}

		bodyStr := string(body)
		if strings.Contains(bodyStr, "you have exceeded your traffic") {
			_log.Debug().
				Str("file", f.name).
				Int("retry_count", retryCount).
				Msg("Bandwidth exceeded. Marking link as invalid")

			f.cache.MarkDownloadLinkAsInvalid(f.link, f.downloadLink, "bandwidth_exceeded")

			// Retry with a different API key if available and we haven't exceeded retries
			if retryCount < maxRetries {
				return true, nil
			}

			return false, &streamError{
				Err:        fmt.Errorf("bandwidth exceeded after %d retries", retryCount),
				StatusCode: http.StatusServiceUnavailable,
			}
		}

		return false, &streamError{
			Err:        fmt.Errorf("service unavailable: %s", bodyStr),
			StatusCode: http.StatusServiceUnavailable,
		}

	case http.StatusNotFound:
		cleanupResp(resp)

		_log.Debug().
			Str("file", f.name).
			Int("retry_count", retryCount).
			Msg("Link not found (404). Marking link as invalid and regenerating")

		f.cache.MarkDownloadLinkAsInvalid(f.link, f.downloadLink, "link_not_found")

		// Try to regenerate download link if we haven't exceeded retries
		if retryCount < maxRetries {
			// Clear cached link to force regeneration
			f.downloadLink = ""
			return true, nil
		}

		return false, &streamError{
			Err:        fmt.Errorf("file not found after %d retries", retryCount),
			StatusCode: http.StatusNotFound,
		}

	default:
		body, _ := io.ReadAll(resp.Body)
		cleanupResp(resp)

		_log.Error().
			Int("status_code", resp.StatusCode).
			Str("file", f.name).
			Str("response_body", string(body)).
			Msg("Unexpected upstream error")

		return false, &streamError{
			Err:        fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body)),
			StatusCode: http.StatusBadGateway,
		}
	}
}

func (f *File) handleRangeRequest(upstreamReq *http.Request, r *http.Request, w http.ResponseWriter) int {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// For video files, apply byte range if exists
		if byteRange, _ := f.getDownloadByteRange(); byteRange != nil {
			upstreamReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", byteRange[0], byteRange[1]))
		}
		return 0 // No range request
	}

	// Parse range request
	ranges, err := parseRange(rangeHeader, f.size)
	if err != nil || len(ranges) != 1 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", f.size))
		return -1 // Invalid range
	}

	// Apply byte range offset if exists
	byteRange, _ := f.getDownloadByteRange()
	start, end := ranges[0].start, ranges[0].end

	if byteRange != nil {
		// Add bounds checking to prevent overflow
		if start > f.size-byteRange[0] || end > f.size-byteRange[0] {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", f.size))
			return -1 // Invalid range after offset
		}
		start += byteRange[0]
		end += byteRange[0]
	}

	upstreamReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	return 1 // Valid range request
}

func (f *File) streamVideoOptimized(w http.ResponseWriter, src io.Reader) error {
	// Use larger buffer for video streaming (better throughput)
	buf := make([]byte, 256*1024) // 256KB buffer for better performance
	flushInterval := 8 * 1024     // Flush every 8KB for responsive streaming
	var totalWritten int

	// Preread first chunk for immediate response
	n, err := src.Read(buf)
	if err != nil && err != io.EOF {
		if isClientDisconnection(err) {
			return &streamError{Err: err, StatusCode: 0, IsClientDisconnection: true}
		}
		return &streamError{Err: err, StatusCode: 0}
	}

	if n > 0 {
		// Write first chunk immediately
		_, writeErr := w.Write(buf[:n])
		if writeErr != nil {
			if isClientDisconnection(writeErr) {
				return &streamError{Err: writeErr, StatusCode: 0, IsClientDisconnection: true}
			}
			return &streamError{Err: writeErr, StatusCode: 0}
		}
		totalWritten += n

		// Flush immediately for faster video start
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}

	if err == io.EOF {
		return nil
	}

	// Stream remaining data with periodic flushing
	for {
		n, err := src.Read(buf)
		if n > 0 {
			_, writeErr := w.Write(buf[:n])
			if writeErr != nil {
				if isClientDisconnection(writeErr) {
					return &streamError{Err: writeErr, StatusCode: 0, IsClientDisconnection: true}
				}
				return &streamError{Err: writeErr, StatusCode: 0}
			}
			totalWritten += n

			// Flush periodically to maintain streaming performance
			if totalWritten%flushInterval == 0 {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
		}

		if err != nil {
			if err == io.EOF {
				// Final flush
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				return nil
			}
			if isClientDisconnection(err) {
				return &streamError{Err: err, StatusCode: 0, IsClientDisconnection: true}
			}
			return &streamError{Err: err, StatusCode: 0}
		}
	}
}

/*
These are the methods that implement the os.File interface for the File type.
Only Stat and ReadDir are used
*/

func (f *File) Stat() (os.FileInfo, error) {
	if f.isDir {
		return &FileInfo{
			name:    f.name,
			size:    0,
			mode:    0755 | os.ModeDir,
			modTime: f.modTime,
			isDir:   true,
		}, nil
	}

	return &FileInfo{
		name:    f.name,
		size:    f.size,
		mode:    0644,
		modTime: f.modTime,
		isDir:   false,
	}, nil
}

func (f *File) Read(p []byte) (n int, err error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}

	if f.metadataOnly {
		return 0, io.EOF
	}

	// For preloaded content files (like version.txt)
	if f.content != nil {
		if f.readOffset >= int64(len(f.content)) {
			return 0, io.EOF
		}
		n = copy(p, f.content[f.readOffset:])
		f.readOffset += int64(n)
		return n, nil
	}

	// For streaming files, return an error to force use of StreamResponse
	return 0, fmt.Errorf("use StreamResponse method for streaming files")
}

func (f *File) Seek(offset int64, whence int) (int64, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}

	// Only handle seeking for preloaded content
	if f.content != nil {
		newOffset := f.readOffset
		switch whence {
		case io.SeekStart:
			newOffset = offset
		case io.SeekCurrent:
			newOffset += offset
		case io.SeekEnd:
			newOffset = int64(len(f.content)) + offset
		default:
			return 0, os.ErrInvalid
		}

		if newOffset < 0 {
			newOffset = 0
		}
		if newOffset > int64(len(f.content)) {
			newOffset = int64(len(f.content))
		}

		f.readOffset = newOffset
		return f.readOffset, nil
	}

	// For streaming files, return error to force use of StreamResponse
	return 0, fmt.Errorf("use StreamResponse method for streaming files")
}

func (f *File) Write(p []byte) (n int, err error) {
	return 0, os.ErrPermission
}

func (f *File) Readdir(count int) ([]os.FileInfo, error) {
	if !f.isDir {
		return nil, os.ErrInvalid
	}

	if count <= 0 {
		return f.children, nil
	}

	if len(f.children) == 0 {
		return nil, io.EOF
	}

	if count > len(f.children) {
		count = len(f.children)
	}

	files := f.children[:count]
	f.children = f.children[count:]
	return files, nil
}
