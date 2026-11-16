// Package bandwidth enforces the download speed cap that all transfers share:
// debrid and usenet, streams and imports.
package bandwidth

import (
	"context"
	"io"
	"net/http"
	"sync/atomic"

	"golang.org/x/time/rate"
)

// minBurst lets one network read of a typical size through without a wait.
const minBurst = 256 << 10

var limiter atomic.Pointer[rate.Limiter]

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

// Wait blocks until n received bytes fit under the cap, or until ctx ends.
func Wait(ctx context.Context, n int) error {
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
