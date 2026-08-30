package link

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
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
	default:
		return "unknown"
	}
}

// Error represents a structured error with retry semantics
type Error struct {
	Err      error
	Category ErrorCategory
	Code     string // Error code from provider (e.g., "bandwidth_exceeded", "404")
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
	// Transient provider codes have to be Refetchable, not Retryable.
	//
	// On the link-validation path only ShouldDisableAccount() and
	// ShouldRefetch() are consulted. A CategoryRetryable error is acted on by
	// neither, so it falls through and the failure is memoised against the
	// download URL. For providers whose download URL is deterministic the cache
	// key never rotates, which makes a rate limit as permanent as a hard
	// failure: the file stays unreadable for the rest of the process lifetime
	// and only a restart clears it.
	//
	// Refetchable is the category that escapes the cache: it drops the stored
	// entry and returns a fresh link without re-validating, so it cannot loop.
	case "429":
		return NewRefetchableError(Err429, code)
	case "503":
		return NewRefetchableError(Err503, code)
	case "500", "502", "504":
		return NewRefetchableError(fmt.Errorf("HTTP %s from provider", code), code)
	default:
		// An unrecognised code is not evidence of permanent failure. Treating it
		// as permanent means one transient 400 poisons the file until restart,
		// which is what users see as "playback works, then stops until I
		// restart the container". Allow a refetch instead, which also clears any
		// poisoned cache entry.
		return NewRefetchableError(fmt.Errorf("unknown error code: %s", code), code)
	}
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

// IsRetryable returns true if the error allows for retries (inverse of IsPermanent)
func (e *Error) IsRetryable() bool {
	return !e.IsPermanent() && e.Category != CategoryAccountIssue
}

// ShouldBackoff returns true if the error requires backoff before retry
func (e *Error) ShouldBackoff() bool {
	return e.ShouldRetry()
}

// RetryAfter returns the recommended wait time before retrying (0 = no specific wait)
func (e *Error) RetryAfter() time.Duration {
	// Check for Retry-After in error code or HTTP status
	switch e.Code {
	case "429":
		return 60 * time.Second // Default backoff for rate limit
	case "503":
		return 30 * time.Second // Default backoff for service unavailable
	default:
		return 0
	}
}

// ClassifyTransportError classifies transport-level errors into link error categories
func ClassifyTransportError(err error) *Error {
	if err == nil {
		return nil
	}

	// Check for timeout errors
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return NewRetryableError(err, "timeout")
	}

	// Check for context cancellation
	if errors.Is(err, errors.New("context canceled")) || errors.Is(err, errors.New("context deadline exceeded")) {
		return NewPermanentError(err, "context_canceled")
	}

	// Check for EOF on read/write
	if errors.Is(err, io.EOF) {
		return NewRetryableError(err, "eof")
	}

	// Default to refetchable for unknown transport errors
	return NewRefetchableError(err, "transport_error")
}

// ClassifyStreamStatus classifies HTTP status codes into link error categories
func ClassifyStreamStatus(status int, header http.Header) *Error {
	switch status {
	case http.StatusOK, http.StatusPartialContent:
		return nil // Success
	case http.StatusUnauthorized:
		return NewPermanentError(fmt.Errorf("HTTP %d unauthorized", status), "401")
	case http.StatusForbidden:
		return NewPermanentError(fmt.Errorf("HTTP %d forbidden", status), "403")
	case http.StatusNotFound:
		return NewPermanentError(Err404, "404")
	case http.StatusTooManyRequests:
		return NewRefetchableError(Err429, "429")
	case http.StatusInternalServerError:
		return NewRefetchableError(fmt.Errorf("HTTP 500 server error"), "500")
	case http.StatusBadGateway:
		return NewRefetchableError(fmt.Errorf("HTTP 502 bad gateway"), "502")
	case http.StatusServiceUnavailable:
		return NewRefetchableError(Err503, "503")
	case http.StatusGatewayTimeout:
		return NewRefetchableError(fmt.Errorf("HTTP 504 gateway timeout"), "504")
	default:
		if status >= 500 {
			return NewRefetchableError(fmt.Errorf("HTTP %d server error", status), fmt.Sprintf("%d", status))
		}
		if status >= 400 {
			// 4xx auth-shaped statuses and 404 usually mean the presigned link expired or rotated
			return NewRefetchableError(fmt.Errorf("HTTP %d client error", status), fmt.Sprintf("%d", status))
		}
		return NewRetryableError(fmt.Errorf("HTTP %d", status), fmt.Sprintf("%d", status))
	}
}
