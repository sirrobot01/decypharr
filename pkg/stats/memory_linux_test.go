//go:build linux

package stats

import (
	"os"
	"syscall"
	"testing"
)

func TestProcessRSSIncludesMappedPages(t *testing.T) {
	before, err := processRSS()
	if err != nil {
		t.Fatal(err)
	}
	const size = 32 << 20
	data, err := syscall.Mmap(-1, 0, size, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_ANON|syscall.MAP_PRIVATE)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Munmap(data); err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < len(data); i += os.Getpagesize() {
		data[i] = 1
	}
	after, err := processRSS()
	if err != nil {
		t.Fatal(err)
	}
	// Allow for asynchronous kernel accounting and other memory released by GC.
	if after < before+size/2 {
		t.Fatalf("RSS did not include mapped pages: before=%d after=%d", before, after)
	}
}
