package usenet

import (
	"sync/atomic"
	"testing"

	usenetfs "github.com/sirrobot01/decypharr/pkg/usenet/fs"
)

type idleReleaseReader struct {
	usenetfs.PrefetchableReaderAt
	releases atomic.Int64
}

func (r *idleReleaseReader) ReleaseIdleDelivery() {
	r.releases.Add(1)
}

func TestDeliveryEntryReleasesResidentsAtFinalHandle(t *testing.T) {
	reader := &idleReleaseReader{}
	entry := &fsEntry{reader: reader, retention: RetentionDelivery}
	entry.refCount.Store(2)

	entry.release()
	if got := reader.releases.Load(); got != 0 {
		t.Fatalf("released with another downstream handle active: %d", got)
	}
	entry.release()
	if got := reader.releases.Load(); got != 1 {
		t.Fatalf("final downstream handle triggered %d idle releases, want 1", got)
	}

	// A warm reopen is still allowed: the reader metadata was retained, only
	// its disposable delivery extents were shed.
	if !entry.acquire() || entry.refCount.Load() != 1 {
		t.Fatal("delivery entry could not be reacquired after releasing memory")
	}
}

func TestWindowEntryKeepsWarmResidentsAtFinalHandle(t *testing.T) {
	reader := &idleReleaseReader{}
	entry := &fsEntry{reader: reader, retention: RetentionWindow}
	entry.refCount.Store(1)
	entry.release()
	if got := reader.releases.Load(); got != 0 {
		t.Fatalf("ordinary window reader unexpectedly released residents: %d", got)
	}
}
