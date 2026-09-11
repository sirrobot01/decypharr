package vfs

import (
	"context"

	"github.com/sirrobot01/decypharr/pkg/manager"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// Backend is the slice of the manager the streaming path uses. It is an
// interface so the read path can be driven end to end in tests against a fake
// origin — measuring time to first byte needs the whole chain, not its parts.
type Backend interface {
	GetEntryByName(entryName, filename string) (*storage.Entry, error)
	TrackStream(entry *storage.Entry, filename, client string) string
	UntrackStream(streamID string)
	OpenStreamUntrackedForCache(ctx context.Context, entry *storage.Entry, filename string, offset int64) (manager.StreamReader, error)
}

var _ Backend = (*manager.Manager)(nil)
