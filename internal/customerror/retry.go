package customerror

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
)

// These catch errors that aren't exported as typed errors.
var retriableErrorStrings = []string{
	"use of closed network connection",
	"unexpected EOF",
	"connection reset by peer",
	"connection refused",
	"broken pipe",
	"i/o timeout",
	"TLS handshake timeout",
	"no such host",
	"server misbehaving",
	"connection timed out",
	"network is unreachable",
	"no route to host",
	"transport connection broken",
	"http2: client connection lost",
	"http2: server sent GOAWAY",
	"http2: timeout awaiting",
	"stream error:",
	"bad record MAC",
	"server closed idle connection",
	"client connection force closed",
	"context deadline exceeded",
}

var permanentErrorStrings = []string{
	"404",
	"not found",
	"403",
	"forbidden",
	"401",
	"unauthorized",
	"402",
	"payment required",
	"410",
	"gone",
	"invalid api key",
	"file not exist",
	"no such file",
}

// IsRetriableError returns true if the error is likely transient and should be retried.
func IsRetriableError(err error) bool {
	if err == nil {
		return false
	}

	// Typed retry rules take precedence over message text.
	if r, ok := errors.AsType[interface {
		error
		IsRetryable() bool
	}](err); ok {
		return r.IsRetryable()
	}

	if IsPermanentError(err) {
		return false
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if errors.Is(err, context.Canceled) {
		return false
	}

	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// io.ErrClosedPipe means the underlying pipe/connection was closed mid-transfer.
	// This is transient (the remote end reset) and should be retried like EPIPE.
	if errors.Is(err, io.ErrClosedPipe) {
		return true
	}

	if netErr, ok := errors.AsType[net.Error](err); ok && netErr.Timeout() {
		return true
	}

	if errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, syscall.EHOSTUNREACH) {
		return true
	}

	errStr := strings.ToLower(err.Error())
	for _, pattern := range retriableErrorStrings {
		if strings.Contains(errStr, strings.ToLower(pattern)) {
			return true
		}
	}

	unwrapped := errors.Unwrap(err)
	if unwrapped != nil && !errors.Is(unwrapped, err) {
		return IsRetriableError(unwrapped)
	}

	return false
}

// IsPermanentError returns true if the error should NOT be retried.
// These are typically 4xx HTTP errors or explicit access denials.
func IsPermanentError(err error) bool {
	if err == nil {
		return false
	}

	if p, ok := errors.AsType[interface {
		error
		IsPermanent() bool
	}](err); ok {
		return p.IsPermanent()
	}
	if r, ok := errors.AsType[interface {
		error
		IsRetryable() bool
	}](err); ok {
		return !r.IsRetryable()
	}

	errStr := strings.ToLower(err.Error())
	for _, pattern := range permanentErrorStrings {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}

	return false
}
