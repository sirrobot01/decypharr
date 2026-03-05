package link

import (
	"errors"
	"fmt"
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
	case "bandwidth_exceeded":
		return NewAccountError(ErrBandwidthExceeded, code)
	case "bytes_limit_reached":
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
	case "503":
		return NewRetryableError(Err503, code)
	default:
		return NewPermanentError(fmt.Errorf("unknown error code: %s", code), code)
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
