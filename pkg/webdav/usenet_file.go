package webdav

import (
	"context"
	"errors"
	"fmt"
	"github.com/sirrobot01/decypharr/pkg/usenet"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type UsenetFile struct {
	name         string
	nzbID        string
	downloadLink string
	size         int64
	isDir        bool
	fileId       string
	metadataOnly bool
	content      []byte
	children     []os.FileInfo // For directories
	usenet       usenet.Usenet
	modTime      time.Time
	readOffset   int64
	rPipe        io.ReadCloser
}

// UsenetFile interface implementations for UsenetFile

func (f *UsenetFile) Close() error {
	if f.isDir {
		return nil // No resources to close for directories
	}

	f.content = nil
	f.children = nil
	f.downloadLink = ""
	return nil
}

func (f *UsenetFile) servePreloadedContent(w http.ResponseWriter, r *http.Request) error {
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

func (f *UsenetFile) StreamResponse(w http.ResponseWriter, r *http.Request) error {
	// Handle preloaded content files
	if f.content != nil {
		return f.servePreloadedContent(w, r)
	}

	// Try streaming with retry logic
	return f.streamWithRetry(w, r, 0)
}

func (f *UsenetFile) streamWithRetry(w http.ResponseWriter, r *http.Request, retryCount int) error {
	start, end := f.getRange(r)

	if retryCount == 0 {
		contentLength := end - start + 1

		w.Header().Set("Content-Length", fmt.Sprintf("%d", contentLength))
		w.Header().Set("Accept-Ranges", "bytes")

		if r.Header.Get("Range") != "" {
			contentRange := fmt.Sprintf("bytes %d-%d/%d", start, end, f.size)
			w.Header().Set("Content-Range", contentRange)
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	}
	err := f.usenet.Stream(r.Context(), f.nzbID, f.name, start, end, w)

	if err != nil {
		if isConnectionError(err) || strings.Contains(err.Error(), "client disconnected") {
			return nil
		}
		// Don't treat cancellation as an error - it's expected for seek operations
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return &streamError{Err: fmt.Errorf("failed to stream file %s: %w", f.name, err), StatusCode: http.StatusInternalServerError}
	}
	return nil
}

// isConnectionError checks if the error is due to client disconnection
func isConnectionError(err error) bool {
	errStr := err.Error()
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) {
		return true // EOF or context cancellation is a common disconnection error
	}
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset by peer")
}

func (f *UsenetFile) getRange(r *http.Request) (int64, int64) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// No range header - return full file range (0 to size-1)
		return 0, f.size - 1
	}

	// Parse the range header for this specific file
	ranges, err := parseRange(rangeHeader, f.size)
	if err != nil || len(ranges) != 1 {
		return -1, -1
	}

	// Return the requested range (this is relative to the file, not the entire NZB)
	start, end := ranges[0].start, ranges[0].end
	if start < 0 || end < 0 || start > end || end >= f.size {
		return -1, -1 // Invalid range
	}

	return start, end
}

func (f *UsenetFile) Stat() (os.FileInfo, error) {
	if f.isDir {
		return &FileInfo{
			name:    f.name,
			size:    f.size,
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

func (f *UsenetFile) Read(p []byte) (int, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}

	// preloaded content (unchanged)
	if f.metadataOnly {
		return 0, io.EOF
	}
	if f.content != nil {
		if f.readOffset >= int64(len(f.content)) {
			return 0, io.EOF
		}
		n := copy(p, f.content[f.readOffset:])
		f.readOffset += int64(n)
		return n, nil
	}

	if f.rPipe == nil {
		pr, pw := io.Pipe()
		f.rPipe = pr

		// start fetch from current offset
		go func(start int64) {
			err := f.usenet.Stream(context.Background(), f.nzbID, f.name, start, f.size-1, pw)
			if err := pw.CloseWithError(err); err != nil {
				return
			}
		}(f.readOffset)
	}

	n, err := f.rPipe.Read(p)
	f.readOffset += int64(n)
	return n, err
}

// Seek simply moves the readOffset pointer within [0…size]
func (f *UsenetFile) Seek(offset int64, whence int) (int64, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}

	// preload path (unchanged)
	var newOffset int64
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset = f.readOffset + offset
	case io.SeekEnd:
		newOffset = f.size + offset
	default:
		return 0, os.ErrInvalid
	}
	if newOffset < 0 {
		newOffset = 0
	}
	if newOffset > f.size {
		newOffset = f.size
	}

	// drop in-flight stream
	if f.rPipe != nil {
		f.rPipe.Close()
		f.rPipe = nil
	}
	f.readOffset = newOffset
	return f.readOffset, nil
}

func (f *UsenetFile) Write(_ []byte) (n int, err error) {
	return 0, os.ErrPermission
}

func (f *UsenetFile) Readdir(count int) ([]os.FileInfo, error) {
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
