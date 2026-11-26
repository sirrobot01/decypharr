package qbit

import (
	"strings"
	"testing"
)

// qBittorrent's `filter` parameter defaults to "all", meaning unfiltered. It is
// not a storage.TorrentState, so forwarding it as one matched no entry and
// returned an empty list to any client that sent it explicitly.
//
// This exercises the function handleTorrentsInfo actually calls, not a copy of
// its logic.
func TestNormalizeStateFilter(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"all is the default sentinel", "all", ""},
		{"sentinel is case-insensitive", "ALL", ""},
		{"sentinel tolerates surrounding space", "  all  ", ""},
		{"absent filter is unfiltered", "", ""},
		{"whitespace-only is unfiltered", "   ", ""},
		{"a real state is preserved", "downloading", "downloading"},
		{"an unrecognised value is preserved verbatim", "seeding", "seeding"},
		{"a real state is trimmed", "  downloading  ", "downloading"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeStateFilter(tc.raw); got != tc.want {
				t.Fatalf("normalizeStateFilter(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// strings.Trim with an empty cutset trims nothing — including the whitespace it
// looks like it should remove. Pinned so the replacement is not quietly
// reverted to it.
func TestTrimWithEmptyCutsetIsANoOp(t *testing.T) {
	const padded = "  all  "
	if strings.Trim(padded, "") != padded {
		t.Fatal("strings.Trim with an empty cutset unexpectedly trimmed something")
	}
}
