package manager

import (
	"context"
	"fmt"

	debrid "github.com/dylanmazurek/decypharr/pkg/debrid/common"
	"github.com/dylanmazurek/decypharr/pkg/debrid/types"
	"github.com/dylanmazurek/decypharr/pkg/storage"
)

// GetDownloadLink fetches and validates a download link for a file in an entry.
// This is the public interface that delegates to the link service.
func (m *Manager) GetDownloadLink(ctx context.Context, entry *storage.Entry, filename string) (types.DownloadLink, error) {
	return m.linkService.GetLink(ctx, entry, filename)
}

// GetDownloadByteRange gets the byte range for a file
func (m *Manager) GetDownloadByteRange(torrentName, filename string) (*[2]int64, error) {
	entry, err := m.storage.GetEntryItem(torrentName)
	if err != nil {
		return nil, fmt.Errorf("torrent not found: %w", err)
	}

	file, ok := entry.Files[filename]
	if !ok {
		return nil, fmt.Errorf("file %s not found in torrent", filename)
	}

	return file.ByteRange, nil
}

// GetTotalActiveDownloadLinks returns the total number of active download links across all debrids
func (m *Manager) GetTotalActiveDownloadLinks() int {
	total := 0

	m.clients.Range(func(name string, client debrid.Client) bool {
		if client == nil {
			return true
		}

		allAccounts := client.AccountManager().Active()
		for _, acc := range allAccounts {
			total += acc.DownloadLinksCount()
		}

		return true
	})

	return total
}
