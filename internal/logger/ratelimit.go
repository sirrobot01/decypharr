package logger

import (
	"time"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/rs/zerolog"
)

// nopLogger is a pre-created no-op logger for suppressed log events.
var nopLogger = zerolog.Nop()

// RateLimitedLogger wraps a zerolog.Logger with deduplication to prevent log spam.
// Same message (by key) will only be logged once within the specified window.
type RateLimitedLogger struct {
	logger   zerolog.Logger
	window   time.Duration
	seen     *xsync.Map[string, time.Time]
	maxItems int // Prevent unbounded memory growth
}

// NewRateLimitedLogger creates a new rate-limited logger.
// window: time duration to suppress duplicate messages
// maxItems: maximum number of unique keys to track (prevents memory leak)

type Options func(*RateLimitedLogger)

func WithLogger(logger zerolog.Logger) Options {
	return func(r *RateLimitedLogger) {
		r.logger = logger
	}
}

func NewRateLimitedLogger(opts ...Options) *RateLimitedLogger {
	r := &RateLimitedLogger{
		logger:   Default(),
		window:   1 * time.Minute,
		seen:     xsync.NewMap[string, time.Time](),
		maxItems: 1000,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// shouldLog returns true if this key should be logged (not seen recently).
// Thread-safe.
func (r *RateLimitedLogger) shouldLog(key string) bool {
	// Check if seen recently
	if lastSeen, ok := r.seen.Load(key); ok {
		now := time.Now()
		if now.Sub(lastSeen) < r.window {
			return false // Suppress
		}
	}

	now := time.Now()

	// Evict old entries if map is too large
	if r.seen.Size() >= r.maxItems {
		cutoff := now.Add(-r.window)

		r.seen.Range(func(key string, value time.Time) bool {
			if value.Before(cutoff) {
				r.seen.Delete(key)
			}
			return true
		})
		// If still too large, clear half
		if r.seen.Size() >= r.maxItems {
			i := 0
			r.seen.Range(func(key string, value time.Time) bool {
				if i%2 == 0 {
					r.seen.Delete(key)
				}
				i++
				return true
			})
		}
	}

	r.seen.Store(key, now)
	return true
}

// RateLimitedEvent provides a fluent API for rate-limited logging.
// It holds a reference to the parent logger and key, evaluating the rate limit
// on each log call rather than at creation time.
type RateLimitedEvent struct {
	parent *RateLimitedLogger
	key    string
}

// Rate returns a RateLimitedEvent for fluent logging with deduplication.
// Usage: logger.Rate(key).Error().Err(err).Msg("message")
// The rate limit is checked on each Error/Warn/Info/Debug call, not at creation.
func (r *RateLimitedLogger) Rate(key string) *RateLimitedEvent {
	return &RateLimitedEvent{
		parent: r,
		key:    key,
	}
}

// Error returns an error event, or a no-op event if rate-limited.
func (e *RateLimitedEvent) Error() *zerolog.Event {
	if e.parent.shouldLog(e.key) {
		return e.parent.logger.Error()
	}
	return nopLogger.Error()
}

// Warn returns a warning event, or a no-op event if rate-limited.
func (e *RateLimitedEvent) Warn() *zerolog.Event {
	if e.parent.shouldLog(e.key) {
		return e.parent.logger.Warn()
	}
	return nopLogger.Warn()
}

// Info returns an info event, or a no-op event if rate-limited.
func (e *RateLimitedEvent) Info() *zerolog.Event {
	if e.parent.shouldLog(e.key) {
		return e.parent.logger.Info()
	}
	return nopLogger.Info()
}

// Debug returns a debug event, or a no-op event if rate-limited.
func (e *RateLimitedEvent) Debug() *zerolog.Event {
	if e.parent.shouldLog(e.key) {
		return e.parent.logger.Debug()
	}
	return nopLogger.Debug()
}

// --- Legacy API (still available) ---

// Error logs an error message with deduplication by key.
// Deprecated: Use Rate(key).Error() for cleaner API.
func (r *RateLimitedLogger) Error(key string) *zerolog.Event {
	if r.shouldLog(key) {
		return r.logger.Error()
	}
	return nil
}

// Warn logs a warning message with deduplication by key.
// Deprecated: Use Rate(key).Warn() for cleaner API.
func (r *RateLimitedLogger) Warn(key string) *zerolog.Event {
	if r.shouldLog(key) {
		return r.logger.Warn()
	}
	return nil
}

// Info logs an info message with deduplication by key.
// Deprecated: Use Rate(key).Info() for cleaner API.
func (r *RateLimitedLogger) Info(key string) *zerolog.Event {
	if r.shouldLog(key) {
		return r.logger.Info()
	}
	return nil
}

// Debug logs a debug message with deduplication by key.
// Deprecated: Use Rate(key).Debug() for cleaner API.
func (r *RateLimitedLogger) Debug(key string) *zerolog.Event {
	if r.shouldLog(key) {
		return r.logger.Debug()
	}
	return nil
}

// ErrorOnce logs an error only once per key until Reset is called.
// Useful for "permanent" errors that should only be logged once per session.
func (r *RateLimitedLogger) ErrorOnce(key string) *zerolog.Event {
	if _, ok := r.seen.Load(key); ok {
		return nopLogger.Error()
	}

	// Use far-future time to prevent re-logging
	r.seen.Store(key, time.Now().Add(24*365*time.Hour))
	return r.logger.Error()
}

// Reset clears all tracked messages, allowing them to be logged again.
func (r *RateLimitedLogger) Reset() {
	r.seen = xsync.NewMap[string, time.Time]()
}

// ResetKey allows a specific key to be logged again.
func (r *RateLimitedLogger) ResetKey(key string) {
	r.seen.Delete(key)
}

// Logger returns the underlying zerolog.Logger for non-rate-limited logging.
func (r *RateLimitedLogger) Logger() zerolog.Logger {
	return r.logger
}
