// Package bandwidth enforces the download speed cap that all transfers share:
// debrid and usenet, streams and imports. It also measures their total speed.
package bandwidth

import (
	"context"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// minBurst lets one network read of a typical size through without a wait.
const minBurst = 256 << 10

// speedWindow is the number of whole seconds that Speed averages.
const speedWindow = 3

var limiter atomic.Pointer[rate.Limiter]

// meter holds the received bytes for each of the last seconds, in a ring
// indexed by the Unix second.
var meter struct {
	mu   sync.Mutex
	bins [speedWindow + 1]struct{ sec, bytes int64 }
}

// SetLimit sets the cap in bytes per second. Zero or less removes the cap.
func SetLimit(bytesPerSecond int64) {
	if bytesPerSecond <= 0 {
		limiter.Store(nil)
		return
	}
	limiter.Store(rate.NewLimiter(rate.Limit(bytesPerSecond), max(int(bytesPerSecond/4), minBurst)))
}

// Limit returns the cap in bytes per second, or zero when there is no cap.
func Limit() int64 {
	if l := limiter.Load(); l != nil {
		return int64(l.Limit())
	}
	return 0
}

// Speed returns the received bytes per second of all transfers, averaged over
// the last whole seconds.
func Speed() int64 {
	now := time.Now().Unix()
	var total int64
	meter.mu.Lock()
	for _, b := range meter.bins {
		if b.sec >= now-speedWindow && b.sec < now {
			total += b.bytes
		}
	}
	meter.mu.Unlock()
	return total / speedWindow
}

// Wait counts n received bytes and blocks until they fit under the cap, or
// until ctx ends.
func Wait(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	now := time.Now().Unix()
	meter.mu.Lock()
	b := &meter.bins[now%int64(len(meter.bins))]
	if b.sec != now {
		b.sec, b.bytes = now, 0
	}
	b.bytes += int64(n)
	meter.mu.Unlock()

	l := limiter.Load()
	if l == nil {
		return nil
	}
	for n > 0 {
		// WaitN refuses more than the burst at once.
		chunk := min(n, l.Burst())
		if err := l.WaitN(ctx, chunk); err != nil {
			return err
		}
		n -= chunk
	}
	return nil
}

// Body returns r with each read held to the cap. ctx ends a wait early.
func Body(ctx context.Context, r io.ReadCloser) io.ReadCloser {
	return &body{ReadCloser: r, ctx: ctx}
}

type body struct {
	io.ReadCloser
	ctx context.Context
}

func (b *body) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if werr := Wait(b.ctx, n); werr != nil && err == nil {
		err = werr
	}
	return n, err
}

// Transport returns rt with every response body held to the cap.
func Transport(rt http.RoundTripper) http.RoundTripper {
	return transport{rt}
}

type transport struct {
	http.RoundTripper
}

func (t transport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = Body(req.Context(), resp.Body)
	return resp, nil
}
