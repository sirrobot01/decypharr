package rar

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"time"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/retry"
)

func NewHttpFile(url string) (*HttpFile, error) {
	file := &HttpFile{
		URL:        url,
		client:     &http.Client{Timeout: 60 * time.Second},
		MaxRetries: config.Get().Retries,
	}
	size, err := file.getFileSize()
	if err != nil {
		return nil, fmt.Errorf("get file size: %w", err)
	}
	file.FileSize = size
	return file, nil
}

func (f *HttpFile) doWithRetry(operation func() error) error {
	return retry.Do(
		func() error {
			err := operation()
			if err != nil && !errors.Is(err, ErrNetworkError) {
				return retry.Unrecoverable(err)
			}
			return err
		},
		retry.Attempts(uint(max(f.MaxRetries, 0))+1),
		retry.Delay(config.DefaultRetryDelay),
		retry.MaxDelay(config.DefaultRetryDelayMax),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
	)
}

func (f *HttpFile) getFileSize() (int64, error) {
	var size int64
	err := f.doWithRetry(func() error {
		req, err := http.NewRequest(http.MethodHead, f.URL, nil)
		if err != nil {
			return err
		}
		resp, err := f.client.Do(req)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNetworkError, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
		}
		size, err = strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid content length: %w", err)
		}
		if size < 0 {
			return fmt.Errorf("negative content length: %d", size)
		}
		return nil
	})
	return size, err
}

// ReadAt reads bytes at off. It returns an error for a short read.
func (f *HttpFile) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, fs.ErrInvalid
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off >= f.FileSize {
		return 0, io.EOF
	}
	requested := len(p)
	p = p[:min(int64(requested), f.FileSize-off)]
	var n int
	err := f.doWithRetry(func() error {
		n = 0
		req, err := http.NewRequest(http.MethodGet, f.URL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, off+int64(len(p))-1))
		resp, err := f.client.Do(req)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrNetworkError, err)
		}
		defer resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusPartialContent:
			n, err = io.ReadFull(resp.Body, p)
			return err
		case http.StatusOK:
			// Skip the prefix when the server ignores the Range header.
			if _, err := io.CopyN(io.Discard, resp.Body, off); err != nil {
				return err
			}
			n, err = io.ReadFull(resp.Body, p)
			return err
		case http.StatusRequestedRangeNotSatisfiable:
			return io.EOF
		default:
			return fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
		}
	})
	if err == nil && n < requested {
		err = io.EOF
	}
	return n, err
}
