package vfs

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/mount/dfs/vfs/ranges"
)

// TestMetadataFlushDebounce hammers markMetadataDirty the way the download
// path does (per ~32KB write) and asserts the signal path no longer rewrites
// the JSON per signal: distinct on-disk versions stay bounded by the debounce,
// and the final state still lands on stop.
func TestMetadataFlushDebounce(t *testing.T) {
	item, _ := newBenchItem(t, 64<<20)

	stop := make(chan struct{})
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		var off int64
		for {
			select {
			case <-stop:
				return
			default:
			}
			item.metaMu.Lock()
			item.info.Rs.Insert(ranges.Range{Pos: off, Size: 32 << 10})
			item.metaMu.Unlock()
			item.markMetadataDirty()
			off += 64 << 10 // discontiguous so Rs keeps growing (content changes)
			time.Sleep(200 * time.Microsecond)
		}
	}()

	// Watch the metadata file for distinct content versions over a window
	// shorter than the debounce+ticker cadence.
	var versions int
	var last []byte
	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(item.metaPath)
		if err == nil && !bytes.Equal(data, last) {
			versions++
			last = data
		}
		time.Sleep(2 * time.Millisecond)
	}
	close(stop)
	<-writerDone

	// At most the initial flush plus one debounced flush, with slack for a
	// racing ticker tick.
	if versions > 3 {
		t.Fatalf("metadata rewritten %d times in 400ms; debounce is not effective", versions)
	}

	// The final mutation must be durable after the writer stops.
	item.metaMu.RLock()
	want := len(item.info.Rs)
	item.metaMu.RUnlock()
	item.stopMetaWriter()
	data, err := os.ReadFile(item.metaPath)
	if err != nil {
		t.Fatal(err)
	}
	var info ItemInfo
	if err := json.Unmarshal(data, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Rs) != want {
		t.Fatalf("final flush lost state: %d ranges on disk, want %d", len(info.Rs), want)
	}
}

// TestSetMaxOffsetKickOnlyOnAdvance asserts a no-advance setMaxOffset (the
// per-read keepalive) doesn't wake the downloader every call.
func TestSetMaxOffsetKickOnlyOnAdvance(t *testing.T) {
	dl := &downloader{kick: make(chan struct{}, 1)}

	drain := func() bool {
		select {
		case <-dl.kick:
			return true
		default:
			return false
		}
	}

	dl.setMaxOffset(1 << 20)
	if !drain() {
		t.Fatal("advance must kick")
	}
	// First no-advance call may kick (rate-limit window opens), later ones
	// within the window must not.
	dl.setMaxOffset(1 << 20)
	drain()
	kicks := 0
	for i := 0; i < 10; i++ {
		dl.setMaxOffset(1 << 20)
		if drain() {
			kicks++
		}
	}
	if kicks != 0 {
		t.Fatalf("no-advance setMaxOffset kicked %d times within the rate-limit window", kicks)
	}
	// A real advance still always kicks.
	dl.setMaxOffset(2 << 20)
	if !drain() {
		t.Fatal("advance after keepalives must kick")
	}
}

// TestDownloadWithPriorityReportsHit asserts the hit flag: true when the
// range was already cached, false when the read had to wait for a download.
func TestDownloadWithPriorityReportsHit(t *testing.T) {
	item, dls := newBenchItem(t, 64<<20)
	prefill := make([]byte, 4<<20)
	if _, _, err := item.WriteAtNoOverwrite(prefill, 0); err != nil {
		t.Fatal(err)
	}

	// A fake downloader whose window covers every offset the test touches:
	// the cached fast path extends by readahead and would otherwise spawn a
	// real downloader (nil manager) for the uncovered tail.
	fakeCtx, fakeCancel := context.WithCancel(context.Background())
	defer fakeCancel()
	dls.mu.Lock()
	dls.dls = append(dls.dls, &downloader{
		dls:       dls,
		quit:      make(chan struct{}),
		kick:      make(chan struct{}, 1),
		ctx:       fakeCtx,
		cancel:    fakeCancel,
		start:     0,
		offset:    32 << 20,
		maxOffset: 64 << 20,
	})
	dls.mu.Unlock()

	hit, err := dls.DownloadWithPriority(context.Background(), ranges.Range{Pos: 0, Size: 1 << 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !hit {
		t.Fatal("expected hit=true for fully cached range")
	}

	// Missing range: park as a waiter, then fulfil it and assert hit=false.
	missing := ranges.Range{Pos: 16 << 20, Size: 1 << 20}

	type result struct {
		hit bool
		err error
	}
	resCh := make(chan result, 1)
	go func() {
		h, e := dls.DownloadWithPriority(context.Background(), missing, false)
		resCh <- result{h, e}
	}()

	// Wait for the waiter to park, then fulfil it.
	deadline := time.Now().Add(2 * time.Second)
	for dls.waiterCount.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("waiter never parked")
		}
		time.Sleep(time.Millisecond)
	}
	if _, _, err := item.WriteAtNoOverwrite(make([]byte, missing.Size), missing.Pos); err != nil {
		t.Fatal(err)
	}
	dls.kickWaiters()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatal(res.err)
		}
		if res.hit {
			t.Fatal("expected hit=false for a range that had to be downloaded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waiter never woken")
	}
}
