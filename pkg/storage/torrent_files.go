package storage

import (
	"os"
	"path/filepath"

	"github.com/sirrobot01/decypharr/internal/config"
)

func torrentDir() string {
	return filepath.Join(config.GetMainPath(), "torrents")
}

func torrentPath(infohash string) string {
	return filepath.Join(torrentDir(), infohash+".torrent")
}

// SaveTorrentFile saves .torrent bytes to disk.
func SaveTorrentFile(infohash string, data []byte) error {
	if len(data) == 0 || infohash == "" {
		return nil
	}
	if err := os.MkdirAll(torrentDir(), 0755); err != nil {
		return err
	}
	return os.WriteFile(torrentPath(infohash), data, 0644)
}

// LoadTorrentFile loads .torrent bytes from disk.
func LoadTorrentFile(infohash string) ([]byte, error) {
	return os.ReadFile(torrentPath(infohash))
}

// DeleteTorrentFile deletes stored .torrent bytes from disk.
func DeleteTorrentFile(infohash string) error {
	if infohash == "" {
		return nil
	}
	if err := os.Remove(torrentPath(infohash)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
