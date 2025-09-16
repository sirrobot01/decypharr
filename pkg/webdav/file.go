package webdav

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

type retryAction int

const (
	noRetry retryAction = iota
	retryWithLimit
	retryAlways
)

const (
	MaxNetworkRetries = 3
	MaxLinkRetries    = 10
)

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
	downloadLink types.DownloadLink
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
	f.downloadLink = types.DownloadLink{}
	f.readOffset = 0
	return nil
}

func (f *File) getDownloadLink() (types.DownloadLink, error) {
	// Check if we already have a final URL cached

	if f.downloadLink.Valid() == nil {
		return f.downloadLink, nil
	}
	downloadLink, err := f.cache.GetDownloadLink(f.torrentName, f.name, f.link)
	if err != nil {
		return downloadLink, err
	}
	err = downloadLink.Valid()
	if err != nil {
		return types.DownloadLink{}, err
	}
	f.downloadLink = downloadLink
	return downloadLink, nil
}

func (f *File) getDownloadByteRange() (*[2]int64, error) {
	byteRange, err := f.cache.GetDownloadByteRange(f.torrentName, f.name)
	if err != nil {
		return nil, err
	}
	return byteRange, nil
}

// setVideoStreamingHeaders sets the necessary headers for video streaming
// It returns error and a boolean indicating if the request is a range request
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
	if f.content != nil {
		return f.servePreloadedContent(w, r)
	}

	return f.streamWithRetry(w, r, 0, 0)
}

func (f *File) streamWithRetry(w http.ResponseWriter, r *http.Request, networkRetries, recoverableRetries int) error {

	_log := f.cache.Logger()

	downloadLink, err := f.getDownloadLink()
	if err != nil {
		return &streamError{Err: err, StatusCode: http.StatusPreconditionFailed}
	}

	upstreamReq, err := http.NewRequest("GET", downloadLink.DownloadLink, nil)
	if err != nil {
		return &streamError{Err: err, StatusCode: http.StatusInternalServerError}
	}

	setVideoStreamingHeaders(upstreamReq)

	isRangeRequest := f.handleRangeRequest(upstreamReq, r, w)
	if isRangeRequest == -1 {
		return &streamError{Err: fmt.Errorf("invalid range"), StatusCode: http.StatusRequestedRangeNotSatisfiable}
	}

	resp, err := f.cache.Download(upstreamReq)
	if err != nil {
		// Network error - retry with limit
		if networkRetries < MaxNetworkRetries {
			_log.Debug().
				Int("network_retries", networkRetries+1).
				Err(err).
				Msg("Network error, retrying")
			return f.streamWithRetry(w, r, networkRetries+1, recoverableRetries)
		}
		return &streamError{Err: err, StatusCode: http.StatusServiceUnavailable}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		retryType, retryErr := f.handleUpstreamError(downloadLink, resp)

		switch retryType {
		case retryAlways:
			if recoverableRetries >= MaxLinkRetries {
				return &streamError{
					Err:        fmt.Errorf("max link retries exceeded (%d)", MaxLinkRetries),
					StatusCode: http.StatusServiceUnavailable,
				}
			}

			_log.Debug().
				Int("recoverable_retries", recoverableRetries+1).
				Str("file", f.name).
				Msg("Recoverable error, retrying")
			return f.streamWithRetry(w, r, 0, recoverableRetries+1) // Reset network retries

		case retryWithLimit:
			if networkRetries < MaxNetworkRetries {
				_log.Debug().
					Int("network_retries", networkRetries+1).
					Str("file", f.name).
					Msg("Network error, retrying")
				return f.streamWithRetry(w, r, networkRetries+1, recoverableRetries)
			}
			fallthrough

		case noRetry:
			if retryErr != nil {
				return retryErr
			}
			return &streamError{
				Err:        fmt.Errorf("non-retryable error: status %d", resp.StatusCode),
				StatusCode: http.StatusBadGateway,
			}
		}
	}

	// Success - stream the response
	statusCode := http.StatusOK
	if isRangeRequest == 1 {
		statusCode = http.StatusPartialContent
	}

	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}

	if contentRange := resp.Header.Get("Content-Range"); contentRange != "" && isRangeRequest == 1 {
		w.Header().Set("Content-Range", contentRange)
	}

	return f.streamBuffer(w, resp.Body, statusCode)
}

func (f *File) handleUpstreamError(downloadLink types.DownloadLink, resp *http.Response) (retryAction, error) {
	_log := f.cache.Logger()

	cleanupResp := func(resp *http.Response) {
		if resp.Body != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}

	switch resp.StatusCode {
	case http.StatusServiceUnavailable:
		body, readErr := io.ReadAll(resp.Body)
		cleanupResp(resp)

		if readErr != nil {
			_log.Error().Err(readErr).Msg("Failed to read response body")
			return retryWithLimit, nil
		}

		bodyStr := string(body)
		if strings.Contains(bodyStr, "you have exceeded your traffic") {
			_log.Debug().
				Str("token", utils.Mask(downloadLink.Token)).
				Str("file", f.name).
				Msg("Bandwidth exceeded for account, invalidating link")

			f.cache.MarkDownloadLinkAsInvalid(f.downloadLink, "bandwidth_exceeded")
			f.downloadLink = types.DownloadLink{}
			return retryAlways, nil
		}

		return noRetry, &streamError{
			Err:        fmt.Errorf("service unavailable: %s", bodyStr),
			StatusCode: http.StatusServiceUnavailable,
		}

	case http.StatusNotFound:
		cleanupResp(resp)
		_log.Debug().
			Str("file", f.name).
			Msg("Link not found, invalidating and regenerating")

		f.cache.MarkDownloadLinkAsInvalid(f.downloadLink, "link_not_found")
		f.downloadLink = types.DownloadLink{}
		return retryAlways, nil

	default:
		body, _ := io.ReadAll(resp.Body)
		cleanupResp(resp)

		_log.Error().
			Int("status_code", resp.StatusCode).
			Str("file", f.name).
			Str("response_body", string(body)).
			Msg("Unexpected upstream error")

		return retryWithLimit, &streamError{
			Err:        fmt.Errorf("upstream error %d: %s", resp.StatusCode, string(body)),
			StatusCode: http.StatusBadGateway,
		}
	}
}

func (f *File) streamBuffer(w http.ResponseWriter, src io.Reader, statusCode int) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response does not support flushing")
	}

	smallBuf := make([]byte, 64*1024) // 64 KB
	if n, err := src.Read(smallBuf); n > 0 {
		// Write status code just before first successful write
		w.WriteHeader(statusCode)

		if _, werr := w.Write(smallBuf[:n]); werr != nil {
			if isClientDisconnection(werr) {
				return &streamError{Err: werr, StatusCode: 0, IsClientDisconnection: true}
			}
			// Headers already sent, can't send HTTP error response
			return &streamError{Err: werr, StatusCode: 0, IsClientDisconnection: false}
		}
		flusher.Flush()
	} else if err != nil && err != io.EOF {
		return &streamError{Err: err, StatusCode: http.StatusInternalServerError}
	}

	buf := make([]byte, 256*1024) // 256 KB
	for {
		n, readErr := src.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				if isClientDisconnection(writeErr) {
					return &streamError{Err: writeErr, StatusCode: 0, IsClientDisconnection: true}
				}
				// Headers already sent, can't send HTTP error response
				return &streamError{Err: writeErr, StatusCode: 0, IsClientDisconnection: false}
			}
			flusher.Flush()
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			if isClientDisconnection(readErr) {
				return &streamError{Err: readErr, StatusCode: 0, IsClientDisconnection: true}
			}
			return readErr
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
		start += byteRange[0]
		end += byteRange[0]
	}

	upstreamReq.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	return 1 // Valid range request
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
