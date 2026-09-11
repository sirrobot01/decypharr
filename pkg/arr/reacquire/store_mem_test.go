package reacquire

import (
	"path/filepath"
	"runtime"
	"testing"
)

// maxGenerationRowBytes bounds one stored row. A generation is written page by
// page so a large library never encodes, holds, and writes its whole snapshot
// at once; at 200,000 bindings a single row was 135MB.
const maxGenerationRowBytes = 8 << 20

func TestGenerationWritesBoundedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("writes a library-sized generation")
	}
	const count = 200_000
	fingerprint := "v1:0000000000000000000000000000000000000000000000000000000000000000"
	bindings := libraryBindings(count, fingerprint)

	path := filepath.Join(t.TempDir(), "bindings.db")
	repository := openTestBindingRepository(t, path)
	store := &observedBindingStore{bindingRepositoryStore: repository.store}
	repository.store = store

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	if err := repository.ReplaceArrGeneration("sonarr", 1, bindings); err != nil {
		t.Fatal(err)
	}
	runtime.ReadMemStats(&after)
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}

	t.Logf("bindings=%d rows=%d largest-row=%.1fMB churn=%.1fMB",
		count, len(store.puts),
		float64(store.largestPut)/(1<<20),
		float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
	if store.largestPut > maxGenerationRowBytes {
		t.Fatalf("largest row is %dB, over the %dB budget", store.largestPut, maxGenerationRowBytes)
	}
}
