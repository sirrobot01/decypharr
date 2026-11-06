package manager

import (
	"testing"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// qBittorrent uses "all" as a sentinel meaning "no filtering" for both the
// `filter` and `hashes` parameters. Treated as a literal it matches nothing:
// no entry has the state "all", and no torrent has the infohash "all". A client
// that sends either explicitly then receives an empty list.
func TestListFilterFuncTreatsHashesAllAsUnfiltered(t *testing.T) {
	q := &Queue{}

	entries := []*storage.Entry{
		{InfoHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Category: "radarr", Protocol: config.ProtocolTorrent},
		{InfoHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Category: "sonarr", Protocol: config.ProtocolTorrent},
	}

	count := func(filter func(*storage.Entry) bool) int {
		if filter == nil {
			return len(entries)
		}
		n := 0
		for _, e := range entries {
			if filter(e) {
				n++
			}
		}
		return n
	}

	t.Run("hashes=all matches everything", func(t *testing.T) {
		got := count(q.ListFilterFunc("", config.ProtocolAll, "", []string{"all"}))
		if got != len(entries) {
			t.Fatalf("hashes=all matched %d of %d entries; the sentinel was treated as a literal infohash", got, len(entries))
		}
	})

	t.Run("hashes=ALL is case-insensitive", func(t *testing.T) {
		if got := count(q.ListFilterFunc("", config.ProtocolAll, "", []string{"ALL"})); got != len(entries) {
			t.Fatalf("hashes=ALL matched %d of %d entries", got, len(entries))
		}
	})

	t.Run("a real infohash still filters", func(t *testing.T) {
		got := count(q.ListFilterFunc("", config.ProtocolAll, "", []string{entries[0].InfoHash}))
		if got != 1 {
			t.Fatalf("explicit infohash matched %d entries, want 1", got)
		}
	})

	t.Run("hashes=all still honours other filters", func(t *testing.T) {
		got := count(q.ListFilterFunc("radarr", config.ProtocolAll, "", []string{"all"}))
		if got != 1 {
			t.Fatalf("category=radarr with hashes=all matched %d entries, want 1", got)
		}
	})

	t.Run("multiple hashes including all are treated literally", func(t *testing.T) {
		// Only a lone "all" is the sentinel; a list is a genuine selection.
		got := count(q.ListFilterFunc("", config.ProtocolAll, "", []string{"all", entries[0].InfoHash}))
		if got != 1 {
			t.Fatalf("mixed hash list matched %d entries, want 1", got)
		}
	})
}
