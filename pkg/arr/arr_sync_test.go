package arr

import (
	"testing"

	"github.com/puzpuzpuz/xsync/v4"
	"github.com/sirrobot01/decypharr/internal/config"
)

func newSyncTestStorage(arrs ...*Arr) *Storage {
	storage := &Storage{arrs: xsync.NewMap[string, *Arr]()}
	for _, configured := range arrs {
		storage.arrs.Store(configured.Name, configured)
	}
	return storage
}

func TestSyncFromConfigUsesValidEditedHostAndPolicies(t *testing.T) {
	storage := newSyncTestStorage(NewWithOptions("radarr", "http://old.example", "old-token", Options{
		Cleanup: true,
		Source:  SourceManual,
	}))

	storage.SyncFromConfig([]config.Arr{{
		Name:    "radarr",
		Host:    "http://new.example",
		Token:   "new-token",
		Cleanup: false,
		Source:  string(SourceManual),
	}})

	got := storage.Get("radarr")
	if got == nil {
		t.Fatal("edited Arr disappeared")
	}
	if got.Host != "http://new.example" || got.Token != "new-token" {
		t.Fatalf("edited connection not applied: host=%q token=%q", got.Host, got.Token)
	}
	if got.Cleanup {
		t.Fatal("edited cleanup policy was not applied")
	}
}

func TestSyncFromConfigRemovesDeletedManualArrButKeepsAutoDetected(t *testing.T) {
	manual := NewWithOptions("manual", "http://manual.example", "token", Options{
		Cleanup: true,
		Source:  SourceManual,
	})
	auto := NewWithOptions("auto", "http://auto.example", "token", Options{
		Cleanup: true,
		Source:  SourceAuto,
	})
	storage := newSyncTestStorage(manual, auto)
	originalMap := storage.arrs

	storage.SyncFromConfig(nil)

	if storage.arrs != originalMap {
		t.Fatal("SyncFromConfig replaced the concurrent map")
	}
	if got := storage.Get("manual"); got != nil {
		t.Fatalf("deleted manual Arr was retained: %+v", got)
	}
	if got := storage.Get("auto"); got != auto {
		t.Fatalf("auto-detected Arr was not preserved: %+v", got)
	}
}
