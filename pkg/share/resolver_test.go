package share

import (
	"crypto/sha256"
	"strings"
	"testing"
	"time"
)

func TestResolverAnswersLongPathsFromCatalog(t *testing.T) {
	mgr := testManager(t)
	// A release-style name pushes every file path well past the long-handle
	// threshold.
	release := "Some.Release.2023.2160p.UHD.BluRay.x265-" + strings.Repeat("GROUP", 16)
	addEntry(t, mgr, release, map[string]int64{
		"Season 01/Episode 01.mkv": 100,
	})

	r := newResolver(mgr)

	filePath := "/__all__/" + release + "/Season 01/Episode 01.mkv"
	if len(filePath) < longPathMin {
		t.Fatalf("test path too short to exercise the long form: %d bytes", len(filePath))
	}
	got, ok := r.resolve(sha256.Sum256([]byte(filePath)))
	if !ok || got != filePath {
		t.Fatalf("resolve file = %q, %v", got, ok)
	}

	// Intermediate directories inside the torrent must resolve too — clients
	// hold handles to them.
	dirPath := "/__all__/" + release + "/Season 01"
	got, ok = r.resolve(sha256.Sum256([]byte(dirPath)))
	if !ok || got != dirPath {
		t.Fatalf("resolve dir = %q, %v", got, ok)
	}

	if _, ok := r.resolve(sha256.Sum256([]byte("/not/in/the/catalog"))); ok {
		t.Fatal("resolved a path that does not exist")
	}
}

func TestResolverRateLimitsRebuilds(t *testing.T) {
	mgr := testManager(t)
	r := newResolver(mgr)

	if _, ok := r.resolve(sha256.Sum256([]byte("/miss"))); ok {
		t.Fatal("unexpected hit")
	}
	first := r.lastBuild

	// A second miss inside the interval must not trigger another walk.
	if _, ok := r.resolve(sha256.Sum256([]byte("/miss-again"))); ok {
		t.Fatal("unexpected hit")
	}
	if !r.lastBuild.Equal(first) {
		t.Fatal("rebuild ran inside the rate-limit interval")
	}

	// Once the interval passes, a miss rebuilds again.
	r.mu.Lock()
	r.lastBuild = time.Now().Add(-2 * resolverRebuildInterval)
	r.mu.Unlock()
	_, _ = r.resolve(sha256.Sum256([]byte("/miss-later")))
	if r.lastBuild.Equal(first) && !first.IsZero() {
		t.Fatal("rebuild did not run after the interval")
	}
}
