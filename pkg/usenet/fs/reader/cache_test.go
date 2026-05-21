package reader

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// TestSegmentCachePinUnpin tests the pin/unpin mechanism that prevents eviction
func TestSegmentCachePinUnpin(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.Nop()

	// Create test segments
	segments := make([]SegmentMeta, 10)
	for i := range segments {
		segments[i] = SegmentMeta{
			MessageID:   "<test" + string(rune('0'+i)) + "@example.com>",
			Number:      i + 1,
			Bytes:       1000,
			StartOffset: int64(i * 1000),
			EndOffset:   int64((i+1)*1000 - 1),
		}
	}

	config := Config{
		MaxDisk: 10000,
	}

	stats := &ReaderStats{}

	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer cache.Close()

	// Put 10 segments
	for i := 0; i < 10; i++ {
		data := make([]byte, 1000)
		if err := cache.Put(i, data); err != nil {
			t.Fatalf("failed to put segment %d: %v", i, err)
		}
	}

	// Test pinning prevents eviction
	cache.PinRange(0, 2)
	if !cache.IsPinned(0) {
		t.Error("segment 0 should be pinned")
	}
	if !cache.IsPinned(1) {
		t.Error("segment 1 should be pinned")
	}
	if !cache.IsPinned(2) {
		t.Error("segment 2 should be pinned")
	}
	if cache.IsPinned(3) {
		t.Error("segment 3 should not be pinned")
	}

	// Unpin
	cache.UnpinRange(0, 2)
	if cache.IsPinned(0) {
		t.Error("segment 0 should not be pinned after unpin")
	}
}

// TestSegmentCacheGetPut tests basic put and get operations
func TestSegmentCacheGetPut(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.Nop()

	segments := []SegmentMeta{
		{MessageID: "<test1@example.com>", Number: 1, Bytes: 100, StartOffset: 0, EndOffset: 99},
		{MessageID: "<test2@example.com>", Number: 2, Bytes: 100, StartOffset: 100, EndOffset: 199},
	}

	config := DefaultConfig()
	stats := &ReaderStats{}

	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer cache.Close()

	// Put data
	testData := []byte("hello world")
	if err := cache.Put(0, testData); err != nil {
		t.Fatalf("failed to put: %v", err)
	}

	// Get data
	data, ok := cache.Get(0)
	if !ok {
		t.Fatal("expected to get data")
	}
	if string(data) != string(testData) {
		t.Errorf("expected %q, got %q", testData, data)
	}

	// Check stats
	if stats.CacheHits.Load() != 1 {
		t.Errorf("expected 1 cache hit, got %d", stats.CacheHits.Load())
	}
}

// TestSegmentCacheWaitForSegment tests the blocking wait mechanism
func TestSegmentCacheWaitForSegment(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.Nop()

	segments := []SegmentMeta{
		{MessageID: "<test1@example.com>", Number: 1, Bytes: 100, StartOffset: 0, EndOffset: 99},
	}

	config := DefaultConfig()
	stats := &ReaderStats{}

	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer cache.Close()

	// Start waiting goroutine
	var wg sync.WaitGroup
	wg.Add(1)

	var waitErr error
	go func() {
		defer wg.Done()
		waitErr = cache.WaitForSegment(ctx, 0)
	}()

	// Give it a moment to start waiting
	time.Sleep(10 * time.Millisecond)

	// Put data (should wake waiter)
	if err := cache.Put(0, []byte("data")); err != nil {
		t.Fatalf("failed to put: %v", err)
	}

	wg.Wait()

	if waitErr != nil {
		t.Errorf("wait returned error: %v", waitErr)
	}
}

// TestSegmentCacheContextCancellation tests that wait respects context cancellation
func TestSegmentCacheContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	logger := zerolog.Nop()

	segments := []SegmentMeta{
		{MessageID: "<test1@example.com>", Number: 1, Bytes: 100, StartOffset: 0, EndOffset: 99},
	}

	config := DefaultConfig()
	stats := &ReaderStats{}

	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer cache.Close()

	// Create a context with timeout
	waitCtx, waitCancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer waitCancel()

	var wg sync.WaitGroup
	wg.Add(1)

	var waitErr error
	go func() {
		defer wg.Done()
		waitErr = cache.WaitForSegment(waitCtx, 0)
	}()

	// Cancel the cache context
	cancel()

	wg.Wait()

	// Should get context error
	if waitErr == nil {
		t.Error("expected error from cancelled context")
	}
}

// TestSegmentCacheSegmentsForRange tests segment range calculation
func TestSegmentCacheSegmentsForRange(t *testing.T) {
	ctx := context.Background()
	logger := zerolog.Nop()

	segments := []SegmentMeta{
		{MessageID: "<test1@example.com>", Number: 1, Bytes: 100, StartOffset: 0, EndOffset: 99},
		{MessageID: "<test2@example.com>", Number: 2, Bytes: 100, StartOffset: 100, EndOffset: 199},
		{MessageID: "<test3@example.com>", Number: 3, Bytes: 100, StartOffset: 200, EndOffset: 299},
		{MessageID: "<test4@example.com>", Number: 4, Bytes: 100, StartOffset: 300, EndOffset: 399},
	}

	config := DefaultConfig()
	stats := &ReaderStats{}

	cache, err := NewSegmentCache(ctx, segments, config, stats, logger)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	defer cache.Close()

	tests := []struct {
		offset    int64
		length    int64
		wantStart int
		wantEnd   int
	}{
		{0, 50, 0, 0},    // First half of first segment
		{50, 100, 0, 1},  // Second half of first + first half of second
		{100, 100, 1, 1}, // Exactly second segment
		{150, 200, 1, 3}, // Cross multiple segments
		{0, 400, 0, 3},   // All segments
	}

	for _, tc := range tests {
		start, end := cache.SegmentsForRange(tc.offset, tc.length)
		if start != tc.wantStart || end != tc.wantEnd {
			t.Errorf("SegmentsForRange(%d, %d): got (%d, %d), want (%d, %d)",
				tc.offset, tc.length, start, end, tc.wantStart, tc.wantEnd)
		}
	}
}
