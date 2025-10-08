package store

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/debrid/types"
)

const (
	MaxNetworkRetries = 5
	MaxLinkRetries    = 10
)

type StreamError struct {
	Err       error
	Retryable bool
	LinkError bool // true if we should try a new link
}

func (e StreamError) Error() string {
	return e.Err.Error()
}

// isConnectionError checks if the error is related to connection issues
func (c *Cache) isConnectionError(err error) bool {
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

func (c *Cache) Stream(ctx context.Context, start, end int64, linkFunc func() (types.DownloadLink, error)) (*http.Response, error) {

	var lastErr error

	downloadLink, err := linkFunc()
	if err != nil {
		return nil, fmt.Errorf("failed to get download link: %w", err)
	}

	// Outer loop: Link retries
	for retry := 0; retry < MaxLinkRetries; retry++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		resp, err := c.doRequest(ctx, downloadLink.DownloadLink, start, end)
		if err != nil {
			// Network/connection error
			lastErr = err
			c.logger.Trace().
				Int("retries", retry).
				Err(err).
				Msg("Network request failed, retrying")

			// Backoff and continue network retry
			if retry < MaxLinkRetries {
				backoff := time.Duration(retry+1) * time.Second
				jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
				select {
				case <-time.After(backoff + jitter):
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				continue
			} else {
				return nil, fmt.Errorf("network request failed after retries: %w", lastErr)
			}
		}

		// Got response - check status
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusPartialContent {
			return resp, nil
		}

		// Bad status code - handle error
		streamErr := c.handleHTTPError(resp, downloadLink)
		resp.Body.Close()

		if !streamErr.Retryable {
			return nil, streamErr // Fatal error
		}

		if streamErr.LinkError {
			c.logger.Trace().
				Int("retries", retry).
				Msg("Link error, getting fresh link")
			lastErr = streamErr
			// Try new link
			downloadLink, err = linkFunc()
			if err != nil {
				return nil, fmt.Errorf("failed to get download link: %w", err)
			}
			continue
		}

		// Retryable HTTP error (429, 503, etc.) - retry network
		lastErr = streamErr
		c.logger.Trace().
			Err(lastErr).
			Str("downloadLink", downloadLink.DownloadLink).
			Str("link", downloadLink.Link).
			Int("retries", retry).
			Int("statusCode", resp.StatusCode).
			Msg("HTTP error, retrying")

		if retry < MaxNetworkRetries-1 {
			backoff := time.Duration(retry+1) * time.Second
			jitter := time.Duration(rand.Intn(1000)) * time.Millisecond
			select {
			case <-time.After(backoff + jitter):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	return nil, fmt.Errorf("stream failed after %d link retries: %w", MaxLinkRetries, lastErr)
}

func (c *Cache) StreamReader(ctx context.Context, start, end int64, linkFunc func() (types.DownloadLink, error)) (io.ReadCloser, error) {
	resp, err := c.Stream(ctx, start, end, linkFunc)
	if err != nil {
		return nil, err
	}

	// Validate we got the expected content
	if resp.ContentLength == 0 {
		resp.Body.Close()
		return nil, fmt.Errorf("received empty response")
	}

	return resp.Body, nil
}

func (c *Cache) doRequest(ctx context.Context, url string, start, end int64) (*http.Response, error) {
	var lastErr error
	// Retry loop specifically for connection-level failures (EOF, reset, etc.)
	for connRetry := 0; connRetry < 3; connRetry++ {
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, StreamError{Err: err, Retryable: false}
		}

		// Set range header
		if start > 0 || end > 0 {
			rangeHeader := fmt.Sprintf("bytes=%d-", start)
			if end > 0 {
				rangeHeader = fmt.Sprintf("bytes=%d-%d", start, end)
			}
			req.Header.Set("Range", rangeHeader)
		}

		// Set optimized headers for streaming
		req.Header.Set("Connection", "keep-alive")
		req.Header.Set("Accept-Encoding", "identity") // Disable compression for streaming
		req.Header.Set("Cache-Control", "no-cache")

		resp, err := c.streamClient.Do(req)
		if err != nil {
			lastErr = err

			// Check if it's a connection error that we should retry
			if c.isConnectionError(err) && connRetry < 2 {
				// Brief backoff before retrying with fresh connection
				time.Sleep(time.Duration(connRetry+1) * 100 * time.Millisecond)
				continue
			}

			return nil, StreamError{Err: err, Retryable: true}
		}
		return resp, nil
	}

	return nil, StreamError{Err: fmt.Errorf("connection retry exhausted: %w", lastErr), Retryable: true}
}

func (c *Cache) handleHTTPError(resp *http.Response, downloadLink types.DownloadLink) StreamError {
	body, _ := io.ReadAll(resp.Body)
	bodyStr := strings.ToLower(string(body))

	switch resp.StatusCode {
	case http.StatusNotFound:
		c.MarkLinkAsInvalid(downloadLink, "link_not_found")
		return StreamError{
			Err:       errors.New("download link not found"),
			Retryable: true,
			LinkError: true,
		}

	case http.StatusServiceUnavailable:
		if strings.Contains(bodyStr, "bandwidth") || strings.Contains(bodyStr, "traffic") {
			c.MarkLinkAsInvalid(downloadLink, "bandwidth_exceeded")
			return StreamError{
				Err:       errors.New("bandwidth limit exceeded"),
				Retryable: true,
				LinkError: true,
			}
		}
		fallthrough

	case http.StatusTooManyRequests:
		return StreamError{
			Err:       fmt.Errorf("HTTP %d: rate limited", resp.StatusCode),
			Retryable: true,
			LinkError: false,
		}

	default:
		retryable := resp.StatusCode >= 500
		return StreamError{
			Err:       fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body)),
			Retryable: retryable,
			LinkError: false,
		}
	}
}
