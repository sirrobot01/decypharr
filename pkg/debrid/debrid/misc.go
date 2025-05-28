package debrid

import (
	"github.com/sirrobot01/decypharr/pkg/debrid/types"
	"sort"
)

// MergeFiles merges the files from multiple torrents into a single map.
// It uses the file name as the key and the file object as the value.
// This is useful for deduplicating files across multiple torrents.
// The order of the torrents is determined by the AddedOn time, with the earliest added torrent first.
// If a file with the same name exists in multiple torrents, the last one will be used.
func mergeFiles(torrents ...CachedTorrent) map[string]types.File {
	merged := make(map[string]types.File)

	// order torrents by added time
	sort.Slice(torrents, func(i, j int) bool {
		return torrents[i].AddedOn.Before(torrents[j].AddedOn)
	})

	for _, torrent := range torrents {
		for _, file := range torrent.GetFiles() {
			merged[file.Name] = file
		}
	}
	return merged
}
