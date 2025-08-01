package nntp

import (
	"errors"
	"fmt"
)

// Error types for NNTP operations
type ErrorType int

const (
	ErrorTypeUnknown ErrorType = iota
	ErrorTypeConnection
	ErrorTypeAuthentication
	ErrorTypeTimeout
	ErrorTypeArticleNotFound
	ErrorTypeGroupNotFound
	ErrorTypePermissionDenied
	ErrorTypeServerBusy
	ErrorTypeInvalidCommand
	ErrorTypeProtocol
	ErrorTypeYencDecode
	ErrorTypeNoAvailableConnection
)

// Error represents an NNTP-specific error
type Error struct {
	Type    ErrorType
	Code    int    // NNTP response code
	Message string // Error message
	Err     error  // Underlying error
}

// Predefined errors for common cases
var (
	ErrArticleNotFound       = &Error{Type: ErrorTypeArticleNotFound, Code: 430, Message: "article not found"}
	ErrGroupNotFound         = &Error{Type: ErrorTypeGroupNotFound, Code: 411, Message: "group not found"}
	ErrPermissionDenied      = &Error{Type: ErrorTypePermissionDenied, Code: 502, Message: "permission denied"}
	ErrAuthenticationFail    = &Error{Type: ErrorTypeAuthentication, Code: 482, Message: "authentication failed"}
	ErrServerBusy            = &Error{Type: ErrorTypeServerBusy, Code: 400, Message: "server busy"}
	ErrPoolNotFound          = &Error{Type: ErrorTypeUnknown, Code: 0, Message: "NNTP pool not found", Err: nil}
	ErrNoAvailableConnection = &Error{Type: ErrorTypeNoAvailableConnection, Code: 0, Message: "no available connection in pool", Err: nil}
)

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("NNTP %s (code %d): %s - %v", e.Type.String(), e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("NNTP %s (code %d): %s", e.Type.String(), e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (e *Error) Is(target error) bool {
	if t, ok := target.(*Error); ok {
		return e.Type == t.Type
	}
	return false
}

// IsRetryable returns true if the error might be resolved by retrying
func (e *Error) IsRetryable() bool {
	switch e.Type {
	case ErrorTypeConnection, ErrorTypeTimeout, ErrorTypeServerBusy:
		return true
	case ErrorTypeArticleNotFound, ErrorTypeGroupNotFound, ErrorTypePermissionDenied, ErrorTypeAuthentication:
		return false
	default:
		return false
	}
}

// ShouldStopParsing returns true if this error should stop the entire parsing process
func (e *Error) ShouldStopParsing() bool {
	switch e.Type {
	case ErrorTypeAuthentication, ErrorTypePermissionDenied:
		return true // Critical auth issues
	case ErrorTypeConnection:
		return false // Can continue with other connections
	case ErrorTypeArticleNotFound:
		return false // Can continue searching for other articles
	case ErrorTypeServerBusy:
		return false // Temporary issue
	default:
		return false
	}
}

func (et ErrorType) String() string {
	switch et {
	case ErrorTypeConnection:
		return "CONNECTION"
	case ErrorTypeAuthentication:
		return "AUTHENTICATION"
	case ErrorTypeTimeout:
		return "TIMEOUT"
	case ErrorTypeArticleNotFound:
		return "ARTICLE_NOT_FOUND"
	case ErrorTypeGroupNotFound:
		return "GROUP_NOT_FOUND"
	case ErrorTypePermissionDenied:
		return "PERMISSION_DENIED"
	case ErrorTypeServerBusy:
		return "SERVER_BUSY"
	case ErrorTypeInvalidCommand:
		return "INVALID_COMMAND"
	case ErrorTypeProtocol:
		return "PROTOCOL"
	case ErrorTypeYencDecode:
		return "YENC_DECODE"
	default:
		return "UNKNOWN"
	}
}

// Helper functions to create specific errors
func NewConnectionError(err error) *Error {
	return &Error{
		Type:    ErrorTypeConnection,
		Message: "connection failed",
		Err:     err,
	}
}

func NewTimeoutError(err error) *Error {
	return &Error{
		Type:    ErrorTypeTimeout,
		Message: "operation timed out",
		Err:     err,
	}
}

func NewProtocolError(code int, message string) *Error {
	return &Error{
		Type:    ErrorTypeProtocol,
		Code:    code,
		Message: message,
	}
}

func NewYencDecodeError(err error) *Error {
	return &Error{
		Type:    ErrorTypeYencDecode,
		Message: "yEnc decode failed",
		Err:     err,
	}
}

// classifyNNTPError classifies an NNTP response code into an error type
func classifyNNTPError(code int, message string) *Error {
	switch {
	case code == 430 || code == 423:
		return &Error{Type: ErrorTypeArticleNotFound, Code: code, Message: message}
	case code == 411:
		return &Error{Type: ErrorTypeGroupNotFound, Code: code, Message: message}
	case code == 502 || code == 503:
		return &Error{Type: ErrorTypePermissionDenied, Code: code, Message: message}
	case code == 481 || code == 482:
		return &Error{Type: ErrorTypeAuthentication, Code: code, Message: message}
	case code == 400:
		return &Error{Type: ErrorTypeServerBusy, Code: code, Message: message}
	case code == 500 || code == 501:
		return &Error{Type: ErrorTypeInvalidCommand, Code: code, Message: message}
	case code >= 400:
		return &Error{Type: ErrorTypeProtocol, Code: code, Message: message}
	default:
		return &Error{Type: ErrorTypeUnknown, Code: code, Message: message}
	}
}

func IsArticleNotFoundError(err error) bool {
	var nntpErr *Error
	if errors.As(err, &nntpErr) {
		return nntpErr.Type == ErrorTypeArticleNotFound
	}
	return false
}

func IsAuthenticationError(err error) bool {
	var nntpErr *Error
	if errors.As(err, &nntpErr) {
		return nntpErr.Type == ErrorTypeAuthentication
	}
	return false
}

func IsRetryableError(err error) bool {
	var nntpErr *Error
	if errors.As(err, &nntpErr) {
		return nntpErr.IsRetryable()
	}
	return false
}
