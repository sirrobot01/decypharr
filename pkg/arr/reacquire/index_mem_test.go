package reacquire

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/sirrobot01/decypharr/pkg/arr"
)

// maxIndexBytesPerBinding is the budget one indexed binding may retain. A
// library of 500,000 files is realistic, so every byte here costs half a
// megabyte of resident memory. The measured cost is ~1,070 bytes; the budget
// leaves room for a field without hiding a structural regression, such as a
// reverse index going back to one Go map per download or episode.
const maxIndexBytesPerBinding = 1300

// freshString copies s, so each binding arrives with its own allocation the
// way a JSON decode delivers it rather than sharing one literal.
func freshString(s string) string {
	return string([]byte(s))
}

// libraryBindings builds bindings shaped like a real Sonarr library.
func libraryBindings(count int, fingerprint string) []Binding {
	bindings := make([]Binding, count)
	for i := range bindings {
		entryID := fmt.Sprintf("%040x", i)
		bindings[i] = Binding{
			ArrName:                freshString("sonarr"),
			ArrType:                arr.Type(freshString(string(arr.Sonarr))),
			ArrInstanceFingerprint: freshString(fingerprint),
			EntryID:                entryID,
			EntryName:              fmt.Sprintf("Some.Show.Name.S%02dE%02d.1080p.WEB-DL.DDP5.1.H.264-GROUP", i%40, i%24),
			EntryFileID:            fmt.Sprintf("%032x", i),
			EntryFileName:          fmt.Sprintf("Some.Show.Name.S%02dE%02d.1080p.WEB-DL.DDP5.1.H.264-GROUP.mkv", i%40, i%24),
			DownloadID:             entryID,
			ArrFileID:              i + 1,
			LibraryPath:            fmt.Sprintf("/media/tv/Some Show Name (2019)/Season %02d/Some.Show.Name.S%02dE%02d.1080p.WEB-DL.DDP5.1.H.264-GROUP.mkv", i%40, i%40, i%24),
			SeriesID:               i/200 + 1,
			SeasonNumber:           i % 40,
			EpisodeIDs:             []int{i + 1},
			Confidence:             Confidence(freshString(string(ConfidenceExactPath))),
			Generation:             1,
			UpdatedAt:              time.Now().UTC(),
		}
	}
	return bindings
}

func heapAlloc() uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	return stats.HeapAlloc
}

// TestIndexRetainedBytesPerBinding measures what the index holds once the
// decoded source is released, which is what a start-up scan leaves behind.
func TestIndexRetainedBytesPerBinding(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates a library-sized index")
	}
	const count = 100_000
	fingerprint := (arr.Arr{Type: arr.Sonarr, Host: "http://sonarr.example:8989"}).Fingerprint()

	baseline := heapAlloc()
	bindings := libraryBindings(count, fingerprint)
	index := NewIndex()
	if err := index.ReplaceArrGeneration("sonarr", 1, bindings); err != nil {
		t.Fatal(err)
	}
	bindings = nil
	retained := heapAlloc() - baseline
	runtime.KeepAlive(index)

	perBinding := retained / count
	t.Logf("retained %.1fMB for %d bindings (%dB each)", float64(retained)/(1<<20), count, perBinding)
	if perBinding > maxIndexBytesPerBinding {
		t.Fatalf("index retains %dB per binding, over the %dB budget", perBinding, maxIndexBytesPerBinding)
	}
}
