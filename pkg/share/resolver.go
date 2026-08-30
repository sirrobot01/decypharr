package share

import (
	"crypto/sha256"
	"sync"
	"time"
)

// longPathMin is the shortest path length worth indexing. facetfs embeds
// paths up to 111 bytes directly in an NFSv4 filehandle; only longer paths
// round-trip as a SHA-256 the server must resolve. The margin below the real
// threshold costs a few spurious entries and tracks upstream changes safely.
const longPathMin = 100

// resolverRebuildInterval rate-limits full catalog walks. Misses arrive in a
// burst after a restart (every handle a client still holds resolves through
// here once); one walk serves the whole burst.
const resolverRebuildInterval = 10 * time.Second

// resolver answers facetfs's long-filehandle misses from the catalog, so
// handles for long paths survive server restarts and facetfs's bounded
// handle-table evictions. facetfs verifies every answer hashes back to the
// requested sum, so a stale index entry is harmless.
type resolver struct {
	catalog catalog

	mu        sync.Mutex
	index     map[[32]byte]string
	lastBuild time.Time
}

func newResolver(c catalog) *resolver {
	return &resolver{catalog: c, index: map[[32]byte]string{}}
}

func (r *resolver) resolve(sum [32]byte) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p, ok := r.index[sum]; ok {
		return p, true
	}
	if time.Since(r.lastBuild) < resolverRebuildInterval {
		return "", false
	}
	r.rebuildLocked()
	p, ok := r.index[sum]
	return p, ok
}

// rebuildLocked walks the whole catalog and indexes every virtual path long
// enough to need the long handle form, including intermediate directories
// inside torrents. Caller holds r.mu.
func (r *resolver) rebuildLocked() {
	index := make(map[[32]byte]string, len(r.index))
	add := func(p string) {
		if len(p) >= longPathMin {
			index[sha256.Sum256([]byte(p))] = p
		}
	}

	for _, entry := range r.catalog.GetEntries() {
		entryPath := "/" + entry.Name()
		add(entryPath)
		if !entry.IsDir() {
			continue
		}
		_, torrents := r.catalog.GetEntryChildren(entry.Name())
		for i := range torrents {
			torrentPath := entryPath + "/" + torrents[i].Name()
			add(torrentPath)
			if !torrents[i].IsDir() {
				continue
			}
			_, files := r.catalog.GetTorrentChildren(torrents[i].Name())
			for j := range files {
				// File names may nest ("a/b/c.mkv"); every prefix is a
				// directory clients can hold a handle to.
				name := files[j].Name()
				add(torrentPath + "/" + name)
				for k := len(name) - 1; k > 0; k-- {
					if name[k] == '/' {
						add(torrentPath + "/" + name[:k])
					}
				}
			}
		}
	}

	r.index = index
	r.lastBuild = time.Now()
}
