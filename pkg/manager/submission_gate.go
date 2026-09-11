package manager

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const torrentSubmissionDedupWindow = 5 * time.Minute

// torrentSubmissionGate coalesces concurrent submissions and suppresses
// repeated successful submissions until the caller has been quiet for window.
// A sliding window is intentional: an Arr re-grab loop must not be allowed to
// submit indefinitely just because it runs more frequently than the TTL.
type torrentSubmissionGate struct {
	mu     sync.Mutex
	recent map[string]time.Time
	group  singleflight.Group
	window time.Duration
	clock  func() time.Time
}

func newTorrentSubmissionGate(window time.Duration) *torrentSubmissionGate {
	return &torrentSubmissionGate{
		recent: make(map[string]time.Time),
		window: window,
		clock:  time.Now,
	}
}

func torrentSubmissionKey(req *ImportRequest) string {
	if req == nil || req.Magnet == nil {
		return ""
	}
	hash := strings.ToLower(strings.TrimSpace(req.Magnet.InfoHash))
	if hash == "" {
		return ""
	}
	provider := strings.ToLower(strings.TrimSpace(req.SelectedDebrid))
	if provider == "" {
		provider = "auto"
	}
	return provider + ":" + hash
}

func (g *torrentSubmissionGate) Do(ctx context.Context, key string, submit func() error) error {
	if key == "" {
		return submit()
	}
	if g.touchRecent(key, g.now()) {
		return nil
	}

	result := g.group.DoChan(key, func() (any, error) {
		if g.touchRecent(key, g.now()) {
			return nil, nil
		}
		if err := submit(); err != nil {
			return nil, err
		}
		g.mark(key, g.now())
		return nil, nil
	})

	select {
	case call := <-result:
		return call.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *torrentSubmissionGate) now() time.Time {
	if g.clock == nil {
		return time.Now()
	}
	return g.clock()
}

func (g *torrentSubmissionGate) touchRecent(key string, now time.Time) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	last, ok := g.recent[key]
	if !ok || !now.Before(last.Add(g.window)) {
		return false
	}
	g.recent[key] = now
	return true
}

func (g *torrentSubmissionGate) mark(key string, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.recent == nil {
		g.recent = make(map[string]time.Time)
	}
	maps.DeleteFunc(g.recent, func(_ string, seen time.Time) bool {
		return !now.Before(seen.Add(g.window))
	})
	g.recent[key] = now
}
