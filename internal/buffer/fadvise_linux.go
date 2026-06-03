//go:build linux

package buffer

import (
	"os"

	"golang.org/x/sys/unix"
)

// fadvise hints let the kernel's page-cache machinery do work we'd
// otherwise duplicate in userland. They are Linux-only; the non-Linux
// build uses no-op stubs.

// adviseSequential tells the kernel this fd will be read mostly
// sequentially, ramping its readahead window up. One shot at New() is
// enough — the hint is sticky on the file description.
func adviseSequential(f *os.File) {
	if f == nil {
		return
	}
	fd := int(f.Fd())
	if fd < 0 {
		return
	}
	// length 0 means "to end of file" per the fadvise(2) contract.
	_ = unix.Fadvise(fd, 0, 0, unix.FADV_SEQUENTIAL)
}

// adviseDontNeed tells the kernel it can drop the given file range from
// its page cache. Paired with our hole-punch on Discard so reclaim
// happens on both sides — disk bytes via fallocate(PUNCH_HOLE), the
// kernel's RAM mirror via FADV_DONTNEED. The fadvise call is page-aligned
// outward so we never accidentally skip bytes on either edge.
func adviseDontNeed(f *os.File, offset, length int64) {
	if f == nil || length <= 0 {
		return
	}
	fd := int(f.Fd())
	if fd < 0 {
		return
	}
	page := int64(os.Getpagesize())
	start := offset - (offset % page)
	end := offset + length
	if rem := end % page; rem != 0 {
		end += page - rem
	}
	if end <= start {
		return
	}
	_ = unix.Fadvise(fd, start, end-start, unix.FADV_DONTNEED)
}

// adviseDontNeedAll tells the kernel to drop the entire file's page cache.
// Length 0 in fadvise(2) means "to end of file" — convenient on the Close
// path when the file is about to be removed: any pages still cached are
// dead weight, and a single hint releases them all at once instead of
// waiting for kernel reclaim pressure to do it lazily.
func adviseDontNeedAll(f *os.File) {
	if f == nil {
		return
	}
	fd := int(f.Fd())
	if fd < 0 {
		return
	}
	_ = unix.Fadvise(fd, 0, 0, unix.FADV_DONTNEED)
}

// adviseWillNeed asks the kernel to start populating the given range in
// its page cache asynchronously. Callers use this when they've queued a
// download that will land at this offset shortly — the kernel can prefetch
// the on-disk bytes (if any), and after the write completes the same
// pages stay hot for the reader that will arrive moments later.
func adviseWillNeed(f *os.File, offset, length int64) {
	if f == nil || length <= 0 {
		return
	}
	fd := int(f.Fd())
	if fd < 0 {
		return
	}
	page := int64(os.Getpagesize())
	start := offset - (offset % page)
	end := offset + length
	if rem := end % page; rem != 0 {
		end += page - rem
	}
	if end <= start {
		return
	}
	_ = unix.Fadvise(fd, start, end-start, unix.FADV_WILLNEED)
}
