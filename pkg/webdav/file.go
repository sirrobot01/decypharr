package webdav

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/store"
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
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
	f.readOffset = 0
	return nil
}

func (f *File) getDownloadLink() (types.DownloadLink, error) {
	// Check if we already have a final URL cached
	downloadLink, err := f.cache.GetDownloadLink(f.torrentName, f.name, f.link)
	if err != nil {
		return downloadLink, err
	}
	err = downloadLink.Valid()
	if err != nil {
		return types.DownloadLink{}, err
	}
	return downloadLink, nil
}

func (f *File) getDownloadByteRange() (*[2]int64, error) {
	byteRange, err := f.cache.GetDownloadByteRange(f.torrentName, f.name)
	if err != nil {
		return nil, err
	}
	return byteRange, nil
}

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
	_logger := f.cache.Logger()

	start, end := f.getRange(r)

	resp, err := f.cache.Stream(r.Context(), start, end, f.getDownloadLink)
	if err != nil {
		_logger.Error().Err(err).Str("file", f.name).Msg("Failed to stream with initial link")
		return &streamError{Err: err, StatusCode: http.StatusRequestedRangeNotSatisfiable}
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	return f.handleSuccessfulResponse(w, resp, start, end)
}

func (f *File) handleSuccessfulResponse(w http.ResponseWriter, resp *http.Response, start, end int64) error {
	statusCode := http.StatusOK
	if start > 0 || end > 0 {
		statusCode = http.StatusPartialContent
	}

	// Copy relevant headers
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		w.Header().Set("Content-Length", contentLength)
	}

	if contentRange := resp.Header.Get("Content-Range"); contentRange != "" && statusCode == http.StatusPartialContent {
		w.Header().Set("Content-Range", contentRange)
	}

	// Copy other important headers
	if contentType := resp.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}

	return f.streamBuffer(w, resp.Body, statusCode)
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

func (f *File) getRange(r *http.Request) (int64, int64) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// For video files, apply byte range if exists
		if byteRange, _ := f.getDownloadByteRange(); byteRange != nil {
			return byteRange[0], byteRange[1]
		}
		return 0, 0
	}

	// Parse range request
	ranges, err := parseRange(rangeHeader, f.size)
	if err != nil || len(ranges) != 1 {
		// Invalid range, return full content
		return 0, 0
	}

	// Apply byte range offset if exists
	byteRange, _ := f.getDownloadByteRange()
	start, end := ranges[0].start, ranges[0].end

	if byteRange != nil {
		start += byteRange[0]
		end += byteRange[0]
	}
	return start, end
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
