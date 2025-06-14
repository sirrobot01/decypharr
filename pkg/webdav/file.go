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
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   20,
		MaxConnsPerHost:       50,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     false,
	},
	Timeout: 0,
}

type File struct {
	cache       *store.Cache
	fileId      string
	torrentName string

	modTime time.Time

	size         int64
	offset       int64
	isDir        bool
	children     []os.FileInfo
	reader       io.ReadCloser
	seekPending  bool
	content      []byte
	isRar        bool
	name         string
	metadataOnly bool

	downloadLink string
	link         string
}

// File interface implementations for File

func (f *File) Close() error {
	if f.reader != nil {
		f.reader.Close()
		f.reader = nil
	}
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

func (f *File) stream() (*http.Response, error) {
	client := sharedClient
	_log := f.cache.Logger()

	downloadLink, err := f.getDownloadLink()
	if err != nil {
		_log.Trace().Msgf("Failed to get download link for %s: %v", f.name, err)
		return nil, err
	}

	if downloadLink == "" {
		_log.Trace().Msgf("Failed to get download link for %s. Empty download link", f.name)
		return nil, fmt.Errorf("empty download link")
	}

	byteRange, err := f.getDownloadByteRange()
	if err != nil {
		_log.Trace().Msgf("Failed to get download byte range for %s: %v", f.name, err)
		return nil, err
	}

	req, err := http.NewRequest("GET", downloadLink, nil)
	if err != nil {
		_log.Trace().Msgf("Failed to create HTTP request: %v", err)
		return nil, err
	}

	if byteRange == nil {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", max(0, f.offset)))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", byteRange[0]+max(0, f.offset)))
	}

	// Make the request
	resp, err := client.Do(req)
	if err != nil {
		_log.Trace().Msgf("HTTP request failed: %v", err)
		return nil, err
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		f.downloadLink = ""

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
				_log.Trace().Msgf("Failed to read response body: %v", readErr)
				return nil, fmt.Errorf("failed to read error response: %w", readErr)
			}

			bodyStr := string(body)
			if strings.Contains(bodyStr, "You can not download this file because you have exceeded your traffic on this hoster") {
				_log.Trace().Msgf("Bandwidth exceeded for %s. Download token will be disabled if you have more than one", f.name)
				f.cache.MarkDownloadLinkAsInvalid(f.link, downloadLink, "bandwidth_exceeded")
				// Retry with a different API key if it's available
				return f.stream()
			}

			return nil, fmt.Errorf("service unavailable: %s", bodyStr)

		case http.StatusNotFound:
			cleanupResp(resp)
			// Mark download link as not found
			// Regenerate a new download link
			_log.Trace().Msgf("Link not found (404) for %s. Marking link as invalid and regenerating", f.name)
			f.cache.MarkDownloadLinkAsInvalid(f.link, downloadLink, "link_not_found")
			// Generate a new download link
			downloadLink, err := f.getDownloadLink()
			if err != nil {
				_log.Trace().Msgf("Failed to get download link for %s. %s", f.name, err)
				return nil, err
			}

			if downloadLink == "" {
				_log.Trace().Msgf("Failed to get download link for %s", f.name)
				return nil, fmt.Errorf("failed to regenerate download link")
			}

			req, err := http.NewRequest("GET", downloadLink, nil)
			if err != nil {
				return nil, err
			}

			// Set the range header again
			if byteRange == nil {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", max(0, f.offset)))
			} else {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", byteRange[0]+max(0, f.offset)))
			}

			newResp, err := client.Do(req)
			if err != nil {
				return nil, err
			}

			if newResp.StatusCode != http.StatusOK && newResp.StatusCode != http.StatusPartialContent {
				cleanupResp(newResp)
				_log.Trace().Msgf("Regenerated link also failed with status %d", newResp.StatusCode)
				f.cache.MarkDownloadLinkAsInvalid(f.link, downloadLink, newResp.Status)
				return nil, fmt.Errorf("failed with status code %d even after link regeneration", newResp.StatusCode)
			}

			return newResp, nil

		default:
			body, _ := io.ReadAll(resp.Body)
			cleanupResp(resp)

			_log.Trace().Msgf("Unexpected status code %d for %s: %s", resp.StatusCode, f.name, string(body))
			return nil, fmt.Errorf("unexpected status code %d: %s", resp.StatusCode, string(body))
		}
	}
	return resp, nil
}

func (f *File) Read(p []byte) (n int, err error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}
	if f.metadataOnly {
		return 0, io.EOF
	}
	if f.content != nil {
		if f.offset >= int64(len(f.content)) {
			return 0, io.EOF
		}
		n = copy(p, f.content[f.offset:])
		f.offset += int64(n)
		return n, nil
	}

	// If we haven't started streaming the file yet or need to reposition
	if f.reader == nil || f.seekPending {
		if f.reader != nil {
			f.reader.Close()
			f.reader = nil
		}

		// Make the request to get the file
		resp, err := f.stream()
		if err != nil {
			return 0, err
		}
		if resp == nil {
			return 0, fmt.Errorf("stream returned nil response")
		}

		f.reader = resp.Body
		f.seekPending = false
	}

	n, err = f.reader.Read(p)
	f.offset += int64(n)

	if err != nil {
		f.reader.Close()
		f.reader = nil
	}

	return n, err
}

func (f *File) Seek(offset int64, whence int) (int64, error) {
	if f.isDir {
		return 0, os.ErrInvalid
	}

	newOffset := f.offset
	switch whence {
	case io.SeekStart:
		newOffset = offset
	case io.SeekCurrent:
		newOffset += offset
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

	// Only mark seek as pending if position actually changed
	if newOffset != f.offset {
		f.offset = newOffset
		f.seekPending = true
	}
	return f.offset, nil
}

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

func (f *File) ReadAt(p []byte, off int64) (n int, err error) {
	// Save current position

	// Seek to requested position
	_, err = f.Seek(off, io.SeekStart)
	if err != nil {
		return 0, err
	}

	// Read the data
	n, err = f.Read(p)
	return n, err
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
