package link

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"syscall"
	"time"
)

// ErrorCategory defines the type of link error and its retry behavior
type ErrorCategory int

const (
	// CategoryPermanent - Don't retry (file deleted, unauthorized)
	CategoryPermanent ErrorCategory = iota
	// CategoryRetryable - retry same link (timeout, 503)
	CategoryRetryable
	// CategoryRefetchable - Get new link (expired, invalid code)
	CategoryRefetchable
	// CategoryAccountIssue - Disable account (bandwidth exceeded)
	CategoryAccountIssue
	// CategoryThrottled - Back off, honoring RetryAfter; the link itself is fine (429)
	CategoryThrottled
)

// String returns a human-readable name for the error category
func (c ErrorCategory) String() string {
	switch c {
	case CategoryPermanent:
		return "permanent"
	case CategoryRetryable:
		return "retryable"
	case CategoryRefetchable:
		return "refetchable"
	case CategoryAccountIssue:
		return "account_issue"
	case CategoryThrottled:
		return "throttled"
	default:
		return "unknown"
	}
}

// Error represents a structured error with retry semantics
type Error struct {
	Err        error
	Category   ErrorCategory
	Code       string        // Error code from provider (e.g., "bandwidth_exceeded", "404")
	RetryAfter time.Duration // For CategoryThrottled: server-requested wait, 0 if unspecified
}

// Error implements the error interface
func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Err.Error())
	}
	return e.Err.Error()
}

// Unwrap returns the underlying error
func (e *Error) Unwrap() error {
	return e.Err
}

// ShouldRetry returns true if the same link should be retried
func (e *Error) ShouldRetry() bool {
	return e.Category == CategoryRetryable
}

// ShouldRefetch returns true if a new link should be fetched
func (e *Error) ShouldRefetch() bool {
	return e.Category == CategoryRefetchable
}

// ShouldDisableAccount returns true if the account should be disabled
func (e *Error) ShouldDisableAccount() bool {
	return e.Category == CategoryAccountIssue
}

// IsPermanent returns true if the error is permanent and no retry should happen
func (e *Error) IsPermanent() bool {
	return e.Category == CategoryPermanent
}

// Sentinel errors
var (
	ErrUnauthorized        = errors.New("unauthorized access to download link")
	ErrLinkNotFound        = errors.New("download link not found")
	ErrBandwidthExceeded   = errors.New("bandwidth limit exceeded")
	ErrInvalidDownloadCode = errors.New("invalid download code")
	ErrLinkExpired         = errors.New("download link expired")
	ErrFileNotAvailable    = errors.New("file not available for download")
	ErrNoActiveAccount     = errors.New("no active account available")
	ErrClientNotFound      = errors.New("debrid client not found")
	ErrPlacementNotFound   = errors.New("placement not found for entry")
	ErrFileMissing         = errors.New("file missing in entry")
	ErrEmptyLink           = errors.New("download link is empty")
)

// HTTP error sentinels
var (
	Err404 = errors.New("HTTP 404 Not Found")
	Err429 = errors.New("HTTP 429 Too Many Requests")
	Err503 = errors.New("HTTP 503 Service Unavailable")
	// ErrLinkRejected is a 400 from the provider/CDN, which in practice means
	// the presigned link expired or rotated rather than a malformed request.
	ErrLinkRejected = errors.New("HTTP 400: link rejected")
)

// NewLinkError creates a new LinkError with the given error and category
func NewLinkError(err error, category ErrorCategory, code string) *Error {
	return &Error{
		Err:      err,
		Category: category,
		Code:     code,
	}
}

// NewPermanentError creates a permanent error
func NewPermanentError(err error, code string) *Error {
	return NewLinkError(err, CategoryPermanent, code)
}

// NewRetryableError creates a retryable error
func NewRetryableError(err error, code string) *Error {
	return NewLinkError(err, CategoryRetryable, code)
}

// NewRefetchableError creates an error that requires refetching the link
func NewRefetchableError(err error, code string) *Error {
	return NewLinkError(err, CategoryRefetchable, code)
}

// NewAccountError creates an error that requires disabling the account
func NewAccountError(err error, code string) *Error {
	return NewLinkError(err, CategoryAccountIssue, code)
}

// ErrorCodeToLinkError converts an error code string to a LinkError with appropriate category
func ErrorCodeToLinkError(code string) *Error {
	switch code {
	case "link_not_found":
		return NewPermanentError(ErrLinkNotFound, code)
	case "bandwidth_exceeded", "quota_exceeded", "daily_limit_exceeded", "bytes_limit_reached":
		return NewAccountError(ErrBandwidthExceeded, code)
	case "link_expired":
		return NewRefetchableError(ErrLinkExpired, code)
	case "file_not_available":
		return NewPermanentError(ErrFileNotAvailable, code)
	case "invalid_download_code":
		return NewRefetchableError(ErrInvalidDownloadCode, code)
	case "401", "unauthorized":
		return NewPermanentError(ErrUnauthorized, code)
	case "404":
		return NewPermanentError(Err404, code)
	case "429":
		return NewRetryableError(Err429, code)
	// Some providers (TorBox) return a bare 400 for a presigned link that has
	// expired or rotated. ClassifyStreamStatus already treats 400 at the CDN
	// layer as refetchable; the provider-API path must agree, otherwise a stale
	// link is classified permanent, fast-trips the VFS circuit breaker
	// (errorCount = maxErrorCount) and the file reads 0 bytes until cooldown
	// instead of simply refetching the link.
	case "400":
		return NewRefetchableError(ErrLinkRejected, code)
	case "503", "read_pxy_timeout":
		return NewRetryableError(Err503, code)
	default:
		return NewPermanentError(fmt.Errorf("unknown error code: %s", code), code)
	}
}

// ShouldBackoff returns true if the caller should wait (RetryAfter, or its own
// backoff) and retry the same link — the link is healthy, the account is hot.
func (e *Error) ShouldBackoff() bool {
	return e.Category == CategoryThrottled
}

// IsRetryable reports whether retrying could succeed. It lets callers outside
// this package (the vfs downloader's retry loop, via customerror's
// selfRetryable interface) respect the classification without importing the
// category constants. Only permanent errors are non-retryable.
func (e *Error) IsRetryable() bool {
	return e.Category != CategoryPermanent
}

// ClassifyStreamStatus classifies a non-2xx HTTP status observed while serving
// bytes from a link (CDN edge), as opposed to provider-API error codes which go
// through ErrorCodeToLinkError. At the CDN layer, 4xx auth-shaped statuses and
// 404 usually mean the presigned link expired or rotated — refetchable. A
// refreshed link that still fails escalates via the caller's attempt budget.
func ClassifyStreamStatus(status int, header http.Header) *Error {
	switch {
	case status == http.StatusBadRequest || status == http.StatusUnauthorized ||
		status == http.StatusForbidden || status == http.StatusGone:
		return NewRefetchableError(fmt.Errorf("HTTP %d: link rejected", status), strconv.Itoa(status))
	case status == http.StatusNotFound:
		return NewRefetchableError(Err404, "404")
	case status == http.StatusRequestedRangeNotSatisfiable:
		return NewPermanentError(errors.New("HTTP 416: requested range not satisfiable"), "416")
	case status == http.StatusTooManyRequests:
		e := NewLinkError(Err429, CategoryThrottled, "429")
		e.RetryAfter = parseRetryAfter(header.Get("Retry-After"))
		return e
	case status >= 500:
		return NewRetryableError(fmt.Errorf("HTTP %d", status), strconv.Itoa(status))
	default:
		return NewPermanentError(fmt.Errorf("unexpected HTTP status %d", status), strconv.Itoa(status))
	}
}

// ClassifyTransportError classifies an error from the transport layer (dial,
// TLS, mid-body read). Anything already classified passes through. Unknown
// errors default to retryable: mid-stream failures are retried on a bounded
// budget, so a wrong "retryable" costs a few attempts while a wrong
// "permanent" kills a recoverable stream.
func ClassifyTransportError(err error) *Error {
	if err == nil {
		return nil
	}
	if existing := GetLinkError(err); existing != nil {
		return existing
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// The caller decides whether this is its own cancellation or a
		// stall-watchdog firing; classified retryable for the latter.
		return NewRetryableError(err, "cancelled_or_stalled")
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return NewRetryableError(err, "short_body")
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.EPIPE),
		errors.Is(err, syscall.ECONNREFUSED):
		return NewRetryableError(err, "connection")
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return NewRetryableError(err, "network")
	}
	return NewRetryableError(err, "transport")
}

// parseRetryAfter parses a Retry-After header value: delta-seconds or HTTP-date.
func parseRetryAfter(value string) time.Duration {
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return 0
}

// IsLinkError checks if an error is a LinkError
func IsLinkError(err error) bool {
	var linkErr *Error
	return errors.As(err, &linkErr)
}

// GetLinkError extracts a LinkError from an error chain
func GetLinkError(err error) *Error {
	var linkErr *Error
	if errors.As(err, &linkErr) {
		return linkErr
	}
	return nil
}
