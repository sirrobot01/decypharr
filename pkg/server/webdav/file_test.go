package webdav

import "testing"

func TestResolveRange(t *testing.T) {
	const size = int64(1 << 20)
	cases := []struct {
		name       string
		header     string
		start, end int64
	}{
		{"no header", "", 0, -1},
		{"simple range", "bytes=100-199", 100, 199},
		{"open-ended", "bytes=100-", 100, size - 1},
		{"suffix", "bytes=-100", size - 100, size - 1},
		{"full via range", "bytes=0-", 0, size - 1},
		// Structurally unusable headers are ignored (RFC 7233 allows serving
		// the full representation) — the old code served a 1-byte 206.
		{"garbage", "bytes=abc", 0, -1},
		{"not bytes unit", "items=0-5", 0, -1},
		{"multi-range", "bytes=0-99,200-299", 0, -1},
		{"inverted", "bytes=500-100", 0, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, end := resolveRange(tc.header, size)
			if start != tc.start || end != tc.end {
				t.Fatalf("resolveRange(%q) = (%d, %d), want (%d, %d)",
					tc.header, start, end, tc.start, tc.end)
			}
		})
	}
}
