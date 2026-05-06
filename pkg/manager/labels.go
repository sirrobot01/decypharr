package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

var ErrInvalidLabel = errors.New("invalid label")

// UpdateEntryLabel updates an entry category and moves any local output that
// was written under the old category folder.
func (m *Manager) UpdateEntryLabel(infohash, label string) (*storage.Entry, error) {
	label = strings.TrimSpace(label)
	if err := validateLabel(label); err != nil {
		return nil, err
	}

	entry, err := m.queue.GetTorrent(infohash)
	if err != nil {
		entry, err = m.storage.Get(infohash)
		if err != nil {
			return nil, fmt.Errorf("entry not found: %w", err)
		}
	}

	oldPath := entry.DownloadPath()
	oldContentPath := entry.ContentPath
	newSavePath := m.savePathForLabel(entry, label)
	updated := *entry
	updated.Category = label
	updated.SavePath = newSavePath
	newPath := updated.DownloadPath()

	if err := moveEntryOutput(oldPath, newPath); err != nil {
		return nil, err
	}

	applyLabel := func(e *storage.Entry) {
		e.Category = label
		e.SavePath = newSavePath
		if e.ContentPath == "" || e.ContentPath == oldContentPath || e.ContentPath == oldPath {
			e.ContentPath = newPath
		}
	}

	var result *storage.Entry
	if queued, err := m.queue.GetTorrent(infohash); err == nil {
		applyLabel(queued)
		if err := m.queue.Update(queued); err != nil {
			return nil, fmt.Errorf("update queued entry: %w", err)
		}
		result = queued
	}

	if stored, err := m.storage.Get(infohash); err == nil {
		applyLabel(stored)
		if err := m.storage.AddOrUpdate(stored); err != nil {
			return nil, fmt.Errorf("update stored entry: %w", err)
		}
		if result == nil {
			result = stored
		}
	}

	if result == nil {
		return nil, fmt.Errorf("entry not found")
	}

	m.RefreshEntries(true)
	return result, nil
}

func validateLabel(label string) error {
	if label == "" || label == "." || label == ".." {
		return ErrInvalidLabel
	}
	if strings.ContainsAny(label, `/\`) {
		return ErrInvalidLabel
	}
	return nil
}

func (m *Manager) savePathForLabel(entry *storage.Entry, label string) string {
	root := m.config.DownloadFolder
	if entry.SavePath != "" {
		root = filepath.Dir(entry.SavePath)
	}
	return filepath.Join(root, label)
}

func moveEntryOutput(oldPath, newPath string) error {
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return nil
	}

	if _, err := os.Lstat(oldPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat old output path: %w", err)
	}

	if _, err := os.Lstat(newPath); err == nil {
		return fmt.Errorf("destination output path already exists: %s", newPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat new output path: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(newPath), 0o755); err != nil {
		return fmt.Errorf("create destination folder: %w", err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("move output from %s to %s: %w", oldPath, newPath, err)
	}

	return nil
}
