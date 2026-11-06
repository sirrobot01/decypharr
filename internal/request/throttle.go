package request

import (
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// maxThrottle bounds one cooldown, so a bad Retry-After value cannot stop a
// provider until restart.
const maxThrottle = 2 * time.Hour

// Throttle is one cooldown for an API key. Providers count their limits per
// key, so every client that uses the key shares one Throttle. While it is
// active, requests fail without touching the network.
type Throttle struct {
	until atomic.Int64 // unix nanoseconds
}

// Trip starts a cooldown of d and reports whether it moved the deadline. It
// never shortens a cooldown that is already running.
func (t *Throttle) Trip(d time.Duration) bool {
	if t == nil || d <= 0 {
		return false
	}
	deadline := time.Now().Add(min(d, maxThrottle)).UnixNano()
	for {
		current := t.until.Load()
		if deadline <= current {
			return false
		}
		if t.until.CompareAndSwap(current, deadline) {
			return true
		}
	}
}

// Remaining returns how long the cooldown has left.
func (t *Throttle) Remaining() time.Duration {
	if t == nil {
		return 0
	}
	return max(time.Until(time.Unix(0, t.until.Load())), 0)
}

// ThrottledError means the provider asked for a pause. RetryAfter is zero when
// the provider gave no duration.
type ThrottledError struct {
	RetryAfter time.Duration
}

func (e *ThrottledError) Error() string {
	if e.RetryAfter <= 0 {
		return "provider rate limit reached"
	}
	return fmt.Sprintf("provider rate limit reached, retry in %s", e.RetryAfter.Round(time.Second))
}

// IsRetryable reports that the same request can succeed after the pause.
func (e *ThrottledError) IsRetryable() bool {
	return true
}

// RetryAfter reads the Retry-After header of resp. It returns zero when the
// header is missing or invalid.
func RetryAfter(resp *http.Response) time.Duration {
	if resp == nil {
		return 0
	}
	value := resp.Header.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		return max(time.Duration(seconds)*time.Second, 0)
	}
	if at, err := http.ParseTime(value); err == nil {
		return max(time.Until(at), 0)
	}
	return 0
}
