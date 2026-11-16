package bandwidth

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
)

func TestBodyHoldsReadsToTheCap(t *testing.T) {
	const speed = 1 << 20
	SetLimit(speed)
	t.Cleanup(func() { SetLimit(0) })

	// 1.5MB at 1MB/s: the burst covers 256KB, so the rest takes about 1.25s.
	r := Body(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", 3<<19))))
	start := time.Now()
	n, err := io.Copy(io.Discard, r)
	if err != nil || n != 3<<19 {
		t.Fatalf("copy = %d, %v", n, err)
	}
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Fatalf("1.5MB at a 1MB/s cap took %s, want at least 1s", elapsed)
	}
}

func TestWaitWithoutCapReturnsAtOnce(t *testing.T) {
	SetLimit(0)
	start := time.Now()
	if err := Wait(context.Background(), 1<<30); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("uncapped wait took %s", elapsed)
	}
}

func TestWaitStopsWithContext(t *testing.T) {
	SetLimit(1 << 20)
	t.Cleanup(func() { SetLimit(0) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := Wait(ctx, 64<<20); err == nil {
		t.Fatal("wait for 64MB at 1MB/s returned without error after the context ended")
	}
}

func TestSpeedCountsUncappedReads(t *testing.T) {
	SetLimit(0)
	meter.mu.Lock()
	clear(meter.bins[:])
	meter.mu.Unlock()
	// Start just after a second ends, so the bytes land in one whole second.
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))
	r := Body(context.Background(), io.NopCloser(strings.NewReader(strings.Repeat("x", 3<<20))))
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Until(time.Now().Truncate(time.Second).Add(time.Second)))

	if got, want := Speed(), int64(1<<20); got != want {
		t.Fatalf("speed = %d, want %d (3MB over %d seconds)", got, want, speedWindow)
	}
}
