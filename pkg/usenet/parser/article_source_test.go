package parser

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"github.com/sirrobot01/decypharr/internal/nntp"
)

type fakeArticleBackend struct {
	fetches   atomic.Int64
	stats     atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64
	started   chan struct{}
	release   chan struct{}
	bodySize  int
}

func (b *fakeArticleBackend) Fetch(ctx context.Context, messageID string) ([]byte, *nntp.YencMetadata, error) {
	b.fetches.Add(1)
	active := b.active.Add(1)
	defer b.active.Add(-1)
	for {
		maximum := b.maxActive.Load()
		if active <= maximum || b.maxActive.CompareAndSwap(maximum, active) {
			break
		}
	}
	if b.started != nil {
		select {
		case b.started <- struct{}{}:
		default:
		}
	}
	if b.release != nil {
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
	size := b.bodySize
	if size == 0 {
		size = 16
	}
	body := make([]byte, size)
	copy(body, messageID)
	return body, &nntp.YencMetadata{Name: messageID, Size: int64(size), PartSize: int64(size)}, nil
}

func (b *fakeArticleBackend) Stat(context.Context, string) error {
	b.stats.Add(1)
	return nil
}

func TestArticleBrokerDeduplicatesConcurrentBodyAndHeader(t *testing.T) {
	t.Parallel()

	backend := &fakeArticleBackend{started: make(chan struct{}, 1), release: make(chan struct{})}
	broker := newArticleBroker(backend, 4, 1<<20)

	var wg sync.WaitGroup
	errors := make(chan error, 12)
	wg.Go(func() {
		_, err := broker.Body(t.Context(), "same@example")
		errors <- err
	})
	<-backend.started
	for index := range 11 {
		wg.Go(func() {
			if index%2 == 0 {
				_, err := broker.Header(t.Context(), "same@example", 8)
				errors <- err
				return
			}
			_, err := broker.Body(t.Context(), "same@example")
			errors <- err
		})
	}
	close(backend.release)
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := backend.fetches.Load(); got != 1 {
		t.Fatalf("network fetches = %d, want 1", got)
	}
	if err := broker.Stat(t.Context(), "same@example"); err != nil {
		t.Fatal(err)
	}
	if got := backend.stats.Load(); got != 0 {
		t.Fatalf("network stats = %d, want 0", got)
	}
}

func TestArticleBrokerBoundsNetworkConcurrency(t *testing.T) {
	t.Parallel()

	backend := &fakeArticleBackend{release: make(chan struct{})}
	broker := newArticleBroker(backend, 3, 1<<20)

	var wg sync.WaitGroup
	for index := range 12 {
		wg.Go(func() {
			_, _ = broker.Body(t.Context(), fmt.Sprintf("article-%d@example", index))
		})
	}
	deadline := time.Now().Add(time.Second)
	for backend.maxActive.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := backend.maxActive.Load(); got != 3 {
		close(backend.release)
		wg.Wait()
		t.Fatalf("maximum network concurrency = %d, want 3", got)
	}
	close(backend.release)
	wg.Wait()
	if got := backend.maxActive.Load(); got != 3 {
		t.Fatalf("maximum network concurrency = %d, want 3", got)
	}
}

func TestArticleBrokerRetainsMetadataAfterBodyEviction(t *testing.T) {
	t.Parallel()

	backend := &fakeArticleBackend{bodySize: 4}
	broker := newArticleBroker(backend, 1, 4)
	if _, err := broker.Body(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := broker.Body(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("network fetches = %d, want 2", got)
	}
	header, err := broker.Header(t.Context(), "one", 4)
	if err != nil {
		t.Fatal(err)
	}
	if string(header.Snippet) != "one\x00" {
		t.Fatalf("cached snippet = %q", header.Snippet)
	}
	if got := backend.fetches.Load(); got != 2 {
		t.Fatalf("header caused network refetch: %d", got)
	}
	if _, err := broker.Body(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if got := backend.fetches.Load(); got != 3 {
		t.Fatalf("evicted body fetches = %d, want 3", got)
	}
}

func TestArticleBrokerBoundsMetadataEntries(t *testing.T) {
	t.Parallel()

	backend := &fakeArticleBackend{bodySize: 4}
	broker := newArticleBroker(backend, 1, 1<<20)
	broker.entryLimit = 2
	for _, messageID := range []string{"one", "two"} {
		if _, err := broker.Body(t.Context(), messageID); err != nil {
			t.Fatal(err)
		}
	}
	if !broker.IsAvailable("one") {
		t.Fatal("first entry was not recorded")
	}
	if _, err := broker.Body(t.Context(), "three"); err != nil {
		t.Fatal(err)
	}
	if broker.IsAvailable("two") {
		t.Fatal("least-recently-used metadata entry was not evicted")
	}
	if !broker.IsAvailable("one") || !broker.IsAvailable("three") {
		t.Fatal("recent metadata entries were evicted")
	}
	metrics := broker.Metrics()
	if metrics.CachedEntries != 2 || metrics.CachedBodies != 2 {
		t.Fatalf("bounded cache metrics = %+v", metrics)
	}
}

// Message ids decoded from a v2 .meta blob are views into one large
// decompressed buffer, so a retained id pins that NZB's whole segment map. The
// broker outlives any single parse, so every key it keeps must be its own copy.
func TestArticleBrokerDoesNotRetainAliasedMessageIDs(t *testing.T) {
	const id = "aliased-segment@example"
	backing := make([]byte, 1<<20)
	for index := range backing {
		backing[index] = 'x'
	}
	copy(backing[4096:], id)
	aliased := unsafe.String(&backing[4096], len(id))
	if aliased != id {
		t.Fatalf("aliased id = %q", aliased)
	}

	broker := newArticleBroker(&fakeArticleBackend{bodySize: 8}, 1, 1<<20)
	if _, err := broker.Body(t.Context(), aliased); err != nil {
		t.Fatal(err)
	}
	if err := broker.Stat(t.Context(), aliased); err != nil {
		t.Fatal(err)
	}

	low := uintptr(unsafe.Pointer(&backing[0]))
	high := low + uintptr(len(backing))
	aliases := func(value string) bool {
		if value == "" {
			return false
		}
		pointer := uintptr(unsafe.Pointer(unsafe.StringData(value)))
		return pointer >= low && pointer < high
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(broker.entries))
	}
	for key := range broker.entries {
		if aliases(key) {
			t.Fatalf("entries key %q aliases the caller's buffer", key)
		}
	}
	for element := broker.entryLRU.Front(); element != nil; element = element.Next() {
		if aliases(element.Value.(string)) {
			t.Fatal("entry LRU retains a key aliasing the caller's buffer")
		}
	}
	for element := broker.lru.Front(); element != nil; element = element.Next() {
		if aliases(element.Value.(string)) {
			t.Fatal("body LRU retains a key aliasing the caller's buffer")
		}
	}
}
