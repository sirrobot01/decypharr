package usenet

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

var (
	ErrConnectionFailed  = errors.New("failed to connect to NNTP server")
	ErrServerUnavailable = errors.New("NNTP server unavailable")
	ErrRateLimitExceeded = errors.New("rate limit exceeded")
	ErrDownloadTimeout   = errors.New("download timeout")
)

// ErrInvalidNZBf creates a formatted error for NZB validation failures
func ErrInvalidNZBf(format string, args ...interface{}) error {
	return fmt.Errorf("invalid NZB: "+format, args...)
}

// Error represents a structured usenet error
type Error struct {
	Code       string
	Message    string
	Err        error
	ServerAddr string
	Timestamp  time.Time
	Retryable  bool
}

func (e *Error) Error() string {
	if e.ServerAddr != "" {
		return fmt.Sprintf("usenet error [%s] on %s: %s", e.Code, e.ServerAddr, e.Message)
	}
	return fmt.Sprintf("usenet error [%s]: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	return e.Err
}

func (e *Error) Is(target error) bool {
	if target == nil {
		return false
	}
	return e.Err != nil && errors.Is(e.Err, target)
}

// NewUsenetError creates a new UsenetError
func NewUsenetError(code, message string, err error) *Error {
	return &Error{
		Code:      code,
		Message:   message,
		Err:       err,
		Timestamp: time.Now(),
		Retryable: isRetryableError(err),
	}
}

// NewServerError creates a new UsenetError with server address
func NewServerError(code, message, serverAddr string, err error) *Error {
	return &Error{
		Code:       code,
		Message:    message,
		Err:        err,
		ServerAddr: serverAddr,
		Timestamp:  time.Now(),
		Retryable:  isRetryableError(err),
	}
}

// isRetryableError determines if an error is retryable
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Network errors are generally retryable
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout()
	}

	// DNS errors are retryable
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return dnsErr.Temporary()
	}

	// Connection refused is retryable
	if errors.Is(err, net.ErrClosed) {
		return true
	}

	// Check error message for retryable conditions
	errMsg := strings.ToLower(err.Error())
	retryableMessages := []string{
		"connection refused",
		"connection reset",
		"connection timed out",
		"network is unreachable",
		"host is unreachable",
		"temporary failure",
		"service unavailable",
		"server overloaded",
		"rate limit",
		"too many connections",
	}

	for _, msg := range retryableMessages {
		if strings.Contains(errMsg, msg) {
			return true
		}
	}

	return false
}

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxRetries      int
	InitialDelay    time.Duration
	MaxDelay        time.Duration
	BackoffFactor   float64
	RetryableErrors []error
}

// DefaultRetryConfig returns a default retry configuration
func DefaultRetryConfig() *RetryConfig {
	return &RetryConfig{
		MaxRetries:    3,
		InitialDelay:  1 * time.Second,
		MaxDelay:      30 * time.Second,
		BackoffFactor: 2.0,
		RetryableErrors: []error{
			ErrConnectionFailed,
			ErrServerUnavailable,
			ErrRateLimitExceeded,
			ErrDownloadTimeout,
		},
	}
}

// ShouldRetry determines if an error should be retried
func (rc *RetryConfig) ShouldRetry(err error, attempt int) bool {
	if attempt >= rc.MaxRetries {
		return false
	}

	// Check if it's a retryable UsenetError
	var usenetErr *Error
	if errors.As(err, &usenetErr) {
		return usenetErr.Retryable
	}

	// Check if it's in the list of retryable errors
	for _, retryableErr := range rc.RetryableErrors {
		if errors.Is(err, retryableErr) {
			return true
		}
	}

	return isRetryableError(err)
}

// GetDelay calculates the delay for the next retry
func (rc *RetryConfig) GetDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return rc.InitialDelay
	}

	delay := time.Duration(float64(rc.InitialDelay) * float64(attempt) * rc.BackoffFactor)
	if delay > rc.MaxDelay {
		delay = rc.MaxDelay
	}

	return delay
}

// RetryWithBackoff retries a function with exponential backoff
func RetryWithBackoff(config *RetryConfig, operation func() error) error {
	var lastErr error

	for attempt := 0; attempt <= config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := config.GetDelay(attempt)
			time.Sleep(delay)
		}

		err := operation()
		if err == nil {
			return nil
		}

		lastErr = err

		if !config.ShouldRetry(err, attempt) {
			break
		}
	}

	return lastErr
}

// CircuitBreakerConfig defines circuit breaker behavior
type CircuitBreakerConfig struct {
	MaxFailures     int
	ResetTimeout    time.Duration
	CheckInterval   time.Duration
	FailureCallback func(error)
}

// CircuitBreaker implements a circuit breaker pattern for NNTP connections
type CircuitBreaker struct {
	config      *CircuitBreakerConfig
	failures    int
	lastFailure time.Time
	state       string // "closed", "open", "half-open"
	mu          *sync.RWMutex
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config *CircuitBreakerConfig) *CircuitBreaker {
	if config == nil {
		config = &CircuitBreakerConfig{
			MaxFailures:   5,
			ResetTimeout:  60 * time.Second,
			CheckInterval: 10 * time.Second,
		}
	}

	return &CircuitBreaker{
		config: config,
		state:  "closed",
		mu:     &sync.RWMutex{},
	}
}

// Execute executes an operation through the circuit breaker
func (cb *CircuitBreaker) Execute(operation func() error) error {
	cb.mu.RLock()
	state := cb.state
	failures := cb.failures
	lastFailure := cb.lastFailure
	cb.mu.RUnlock()

	// Check if we should attempt reset
	if state == "open" && time.Since(lastFailure) > cb.config.ResetTimeout {
		cb.mu.Lock()
		cb.state = "half-open"
		cb.mu.Unlock()
		state = "half-open"
	}

	if state == "open" {
		return NewUsenetError("circuit_breaker_open",
			fmt.Sprintf("circuit breaker is open (failures: %d)", failures),
			ErrServerUnavailable)
	}

	err := operation()

	cb.mu.Lock()
	defer cb.mu.Unlock()

	if err != nil {
		cb.failures++
		cb.lastFailure = time.Now()

		if cb.failures >= cb.config.MaxFailures {
			cb.state = "open"
		}

		if cb.config.FailureCallback != nil {
			go func() {
				cb.config.FailureCallback(err)
			}()
		}

		return err
	}

	// Success - reset if we were in half-open state
	if cb.state == "half-open" {
		cb.state = "closed"
		cb.failures = 0
	}

	return nil
}

// GetState returns the current circuit breaker state
func (cb *CircuitBreaker) GetState() string {
	cb.mu.RLock()
	defer cb.mu.RUnlock()
	return cb.state
}

// Reset manually resets the circuit breaker
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = "closed"
	cb.failures = 0
}

// ValidationError represents validation errors
type ValidationError struct {
	Field   string
	Value   interface{}
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error for field '%s': %s", e.Field, e.Message)
}

// ValidateNZBContent validates NZB content
func ValidateNZBContent(content []byte) error {
	if len(content) == 0 {
		return &ValidationError{
			Field:   "content",
			Value:   len(content),
			Message: "NZB content cannot be empty",
		}
	}

	if len(content) > 100*1024*1024 { // 100MB limit
		return &ValidationError{
			Field:   "content",
			Value:   len(content),
			Message: "NZB content exceeds maximum size limit (100MB)",
		}
	}

	contentStr := string(content)
	if !strings.Contains(contentStr, "<nzb") {
		maxLen := 100
		if len(contentStr) < maxLen {
			maxLen = len(contentStr)
		}
		return &ValidationError{
			Field:   "content",
			Value:   contentStr[:maxLen],
			Message: "content does not appear to be valid NZB format",
		}
	}

	return nil
}
