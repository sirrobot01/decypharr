package storage

import (
	"errors"
	"fmt"
	"github.com/sirrobot01/appendstore"
	"google.golang.org/protobuf/proto"
)

// GetEntryItems returns all entry item names
func (s *Storage) GetEntryItems() map[string]struct{} {
	items := make(map[string]struct{})
	_ = s.entryItems.ForEachMetadata(func(key string, meta *appendstore.Metadata) error {
		items[key] = struct{}{}
		return nil
	})
	return items
}

// UpdateEntryItem updates an entry item from an entry
func (s *Storage) UpdateEntryItem(entry *Entry) error {
	return s.updateEntryItem(entry)
}

func (s *Storage) UpdateItem(item *EntryItem) error {
	var oldFingerprint string
	existing, err := s.GetEntryItem(item.Name)
	if err != nil && !errors.Is(err, appendstore.ErrKeyNotFound) {
		return fmt.Errorf("read name index %q: %w", item.Name, err)
	}
	oldFingerprint = EntryItemRepairFingerprint(existing)

	pb := EntryItemToProto(item)
	data, err := proto.Marshal(pb)
	if err != nil {
		return fmt.Errorf("encode name index %q: %w", item.Name, err)
	}
	if oldFingerprint != EntryItemRepairFingerprint(item) {
		if err := s.MarkEntryDirty(item.Name, "", "entry_item_changed"); err != nil {
			return err
		}
	}
	if err := s.entryItems.Put(item.Name, data, nil); err != nil {
		return fmt.Errorf("save name index %q: %w", item.Name, err)
	}
	return nil
}

// GetEntryItem retrieves an entry item by name
func (s *Storage) GetEntryItem(name string) (*EntryItem, error) {
	data, err := s.entryItems.Get(name)
	if err != nil {
		return nil, err
	}

	var pb EntryItemProto
	if err := proto.Unmarshal(data, &pb); err != nil {
		return nil, err
	}
	return ProtoToEntryItem(&pb), nil
}

// ForEachEntryItem iterates over entry items
func (s *Storage) ForEachEntryItem(fn func(*EntryItem) error) error {
	return s.entryItems.ForEach(func(key string, value []byte) error {
		var pb EntryItemProto
		if proto.Unmarshal(value, &pb) != nil {
			return nil
		}
		return fn(ProtoToEntryItem(&pb))
	})
}

// updateEntryItem updates the name index.
func (s *Storage) updateEntryItem(entry *Entry) error {
	name := entry.GetFolder()
	if name == "" {
		return nil
	}
	item, err := s.GetEntryItem(name)
	if err != nil && !errors.Is(err, appendstore.ErrKeyNotFound) {
		return fmt.Errorf("read name index %q: %w", name, err)
	}
	oldFingerprint := EntryItemRepairFingerprint(item)
	if item == nil {
		item = &EntryItem{Name: name, Files: make(map[string]*File)}
	}
	for fileName, file := range entry.Files {
		mergeFileIntoItem(item, fileName, file)
	}
	item.Size = item.GetSize()
	data, err := proto.Marshal(EntryItemToProto(item))
	if err != nil {
		return fmt.Errorf("encode name index %q: %w", name, err)
	}
	if oldFingerprint != EntryItemRepairFingerprint(item) {
		if err := s.MarkEntryDirty(name, entry.Protocol, "entry_item_changed"); err != nil {
			return err
		}
	}
	if err := s.entryItems.Put(name, data, nil); err != nil {
		return fmt.Errorf("save name index %q: %w", name, err)
	}
	return nil
}

// removeFromEntryItem removes an entry from the name index.
//
// Several entries can share one folder name. A provider that re-keys the same
// release produces exactly that: a grab stores the entry under the magnet
// infohash, while a later sync of the same cloud transfer stores it under a
// different key (Premiumize has no infohash in transfer/list, so sync derives a
// synthetic one). Both entries render the same folder, and the name index holds
// one file record per filename, tagged with whichever entry wrote it last.
//
// Deleting one of those entries must therefore not take the folder down with
// it: after dropping the dying entry's own file records, the index is rebuilt
// from the entries that are still live, and it is only removed when no entry
// maps to the folder any more. Without the rebuild the folder stays listed
// (listings come from entry metadata) while serving no files at all.
func (s *Storage) removeFromEntryItem(entry *Entry) error {
	name := entry.GetFolder()
	if name == "" {
		return nil
	}
	item, err := s.GetEntryItem(name)
	if errors.Is(err, appendstore.ErrKeyNotFound) {
		return s.DeleteEntryHealth(name)
	}
	if err != nil {
		return fmt.Errorf("read name index %q before deletion: %w", name, err)
	}
	for fileName := range entry.Files {
		if f, exists := item.Files[fileName]; exists && f.InfoHash == entry.InfoHash {
			delete(item.Files, fileName)
		}
	}
	if err := s.rebuildEntryItemFiles(item, name, entry.InfoHash); err != nil {
		return err
	}
	if len(item.Files) == 0 {
		if err := s.DeleteEntryHealth(name); err != nil {
			return err
		}
		if err := s.entryItems.Delete(name); err != nil {
			return fmt.Errorf("delete name index %q: %w", name, err)
		}
		return nil
	}
	item.Size = item.GetSize()
	return s.UpdateItem(item)
}

// mergeFileIntoItem adds a file record to the name index, keeping the newest
// record when several entries hold the same filename. Same rule as
// updateEntryItem, so a rebuild lands on the same result as a normal write.
func mergeFileIntoItem(item *EntryItem, fileName string, file *File) {
	if item.Files == nil {
		item.Files = make(map[string]*File)
	}
	existing, ok := item.Files[fileName]
	if !ok || file.AddedOn.After(existing.AddedOn) ||
		(file.AddedOn.Equal(existing.AddedOn) && file.Size != existing.Size) {
		item.Files[fileName] = file
	}
}

// entriesByFolder returns the infohashes of every live entry that renders the
// given folder name, skipping skipInfoHash. The scan is metadata-only, so it
// costs no disk reads; only the matches are read back.
func (s *Storage) entriesByFolder(name, skipInfoHash string) ([]string, error) {
	var hashes []string
	err := s.entries.ForEachMetadata(func(key string, meta *appendstore.Metadata) error {
		if key == skipInfoHash {
			return nil
		}
		if meta.Attribute(attributeName) == name {
			hashes = append(hashes, key)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan entries for folder %q: %w", name, err)
	}
	return hashes, nil
}

// rebuildEntryItemFiles merges back the files of every live entry that shares
// the folder name, so an index left short by a deletion still serves the
// folders its remaining entries provide.
func (s *Storage) rebuildEntryItemFiles(item *EntryItem, name, skipInfoHash string) error {
	hashes, err := s.entriesByFolder(name, skipInfoHash)
	if err != nil {
		return err
	}
	for _, infoHash := range hashes {
		entry, err := s.Get(infoHash)
		if err != nil {
			if errors.Is(err, appendstore.ErrKeyNotFound) {
				continue
			}
			return fmt.Errorf("read entry %q to rebuild name index %q: %w", infoHash, name, err)
		}
		for fileName, file := range entry.Files {
			mergeFileIntoItem(item, fileName, file)
		}
	}
	return nil
}

// ReconcileEntryItems rebuilds name index records that went missing while the
// entry behind them stayed live. Such a folder is listed but serves nothing,
// and nothing repairs it on its own, because the index is only written when an
// entry is written. Returns how many folders were rebuilt.
func (s *Storage) ReconcileEntryItems() (int, error) {
	folders := make(map[string][]string)
	err := s.entries.ForEachMetadata(func(key string, meta *appendstore.Metadata) error {
		name := meta.Attribute(attributeName)
		if name == "" {
			return nil
		}
		folders[name] = append(folders[name], key)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("scan entries to reconcile name index: %w", err)
	}

	existing := s.GetEntryItems()
	rebuilt := 0
	for name, hashes := range folders {
		if _, ok := existing[name]; ok {
			continue
		}
		item := &EntryItem{Name: name, Files: make(map[string]*File)}
		for _, infoHash := range hashes {
			entry, err := s.Get(infoHash)
			if err != nil {
				continue
			}
			for fileName, file := range entry.Files {
				mergeFileIntoItem(item, fileName, file)
			}
		}
		if len(item.Files) == 0 {
			continue
		}
		item.Size = item.GetSize()
		if err := s.UpdateItem(item); err != nil {
			return rebuilt, fmt.Errorf("rebuild name index %q: %w", name, err)
		}
		rebuilt++
	}
	return rebuilt, nil
}
