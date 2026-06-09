package storage

import (
	"fmt"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/storage/hybrid"
	"google.golang.org/protobuf/proto"
)

// AddOrUpdate adds or updates an entry
func (s *Storage) AddOrUpdate(entry *Entry) error {
	entry.UpdatedAt = time.Now()

	// Handle name index
	s.updateEntryItem(entry)

	// Serialize
	pb := EntryToProto(entry)
	data, err := proto.Marshal(pb)
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}

	meta := &hybrid.EntryMeta{
		Category:  entry.Category,
		Provider:  entry.ActiveProvider,
		Status:    string(entry.Status),
		Name:      entry.GetFolder(), // Store computed folder name for fast listings
		TotalSize: entry.Size,
		Protocol:  string(entry.Protocol),
		Bad:       entry.Bad,
		AddedOn:   entry.AddedOn.Unix(),
	}

	return s.entries.Put(entry.InfoHash, data, meta)
}

// BatchAddOrUpdate adds or updates multiple entries
func (s *Storage) BatchAddOrUpdate(entries []*Entry) error {
	for _, entry := range entries {
		if err := s.AddOrUpdate(entry); err != nil {
			return err
		}
	}
	return nil
}

// Exists checks if an entry exists
func (s *Storage) Exists(infohash string) (bool, error) {
	return s.entries.Exists(infohash), nil
}

// Get retrieves an entry by InfoHash
func (s *Storage) Get(infohash string) (*Entry, error) {
	data, err := s.entries.Get(infohash)
	if err != nil {
		return nil, err
	}

	var pb EntryProto
	if err := proto.Unmarshal(data, &pb); err != nil {
		return nil, err
	}

	return ProtoToEntry(&pb), nil
}

// List retrieves all cached entries with optional filtering
func (s *Storage) List(filter func(*Entry) bool) ([]*Entry, error) {
	var entries []*Entry

	err := s.entries.ForEach(func(key string, value []byte) error {
		var pb EntryProto
		if err := proto.Unmarshal(value, &pb); err != nil {
			return nil
		}
		entry := ProtoToEntry(&pb)
		if filter == nil || filter(entry) {
			entries = append(entries, entry)
		}
		return nil
	})

	return entries, err
}

// ForEach iterates over entries
func (s *Storage) ForEach(fn func(*Entry) error) error {
	return s.entries.ForEach(func(key string, value []byte) error {
		var pb EntryProto
		if err := proto.Unmarshal(value, &pb); err != nil {
			return nil
		}
		return fn(ProtoToEntry(&pb))
	})
}

// ForEachBatch iterates over entries in batches
func (s *Storage) ForEachBatch(batchSize int, fn func([]*Entry) error) error {
	batch := make([]*Entry, 0, batchSize)

	// Reuse a single proto message across the scan. proto.Reset zeroes it
	// between records, so Unmarshal reuses the message's backing storage
	// instead of allocating a fresh EntryProto (and its nested message/slice
	// fields) per entry. Safe because ProtoToEntry copies values out into a
	// fresh Entry (the one aliased field, Tags, is replaced by Reset->nil
	// before the next Unmarshal, leaving the prior entry's slice untouched).
	var pb EntryProto
	err := s.entries.ForEach(func(key string, value []byte) error {
		proto.Reset(&pb)
		if err := proto.Unmarshal(value, &pb); err != nil {
			return nil
		}
		batch = append(batch, ProtoToEntry(&pb))

		if len(batch) >= batchSize {
			if err := fn(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
		return nil
	})

	if err == nil && len(batch) > 0 {
		err = fn(batch)
	}
	return err
}

// EntryMetaInfo is a lightweight struct for folder listings (no disk reads)
type EntryMetaInfo struct {
	InfoHash string
	Name     string
	Size     int64
	AddedOn  time.Time
	Provider string
	Protocol string
	Bad      bool
}

// ForEachMeta iterates over entry metadata without reading full entries from disk.
// This is O(n) in-memory only - no disk reads, no protobuf deserialization.
func (s *Storage) ForEachMeta(fn func(*EntryMetaInfo) error) error {
	return s.entries.ForEachMeta(func(key string, meta *hybrid.IndexEntry) error {
		return fn(&EntryMetaInfo{
			InfoHash: key,
			Name:     meta.Name,
			Size:     meta.TotalSize,
			AddedOn:  time.Unix(meta.AddedOn, 0),
			Provider: meta.Provider,
			Protocol: meta.Protocol,
			Bad:      meta.Bad,
		})
	})
}

// MigrateMetadata re-saves all entries to populate the new metadata fields
// (Protocol, Bad, AddedOn, computed folder Name) in the index.
// This is a one-time migration for existing data.
// Returns the number of entries migrated and any error.
func (s *Storage) MigrateMetadata() (int, error) {
	// First, collect all keys that need migration
	// We check if Protocol is empty as indicator of unmigrated data
	var keysToMigrate []string
	_ = s.entries.ForEachMeta(func(key string, meta *hybrid.IndexEntry) error {
		// Skip special keys
		if strings.HasPrefix(key, "__") {
			return nil
		}
		// Check if metadata needs migration (Protocol empty = old format)
		if meta.Protocol == "" {
			keysToMigrate = append(keysToMigrate, key)
		}
		return nil
	})

	if len(keysToMigrate) == 0 {
		return 0, nil
	}

	// Migrate each entry by reading and re-saving
	migrated := 0
	for _, key := range keysToMigrate {
		entry, err := s.Get(key)
		if err != nil {
			continue // Skip entries that can't be read
		}

		// Re-save to update metadata
		if err := s.AddOrUpdate(entry); err != nil {
			continue
		}
		migrated++
	}

	return migrated, nil
}

// Delete removes an entry
func (s *Storage) Delete(infohash string) error {
	// get entry for cleanup
	entry, err := s.Get(infohash)
	if err == nil && entry != nil {
		s.removeFromEntryItem(entry)
	}
	return s.entries.Delete(infohash)
}

// Count returns the number of entries
func (s *Storage) Count() (int, error) {
	return s.entries.Len(), nil
}

// updateEntryItem updates the name index
func (s *Storage) updateEntryItem(entry *Entry) {
	name := entry.GetFolder()
	if name == "" {
		return
	}

	var item *EntryItem
	if data, err := s.entryItems.Get(name); err == nil {
		var pb EntryItemProto
		if proto.Unmarshal(data, &pb) == nil {
			item = ProtoToEntryItem(&pb)
		}
	}
	oldFingerprint := EntryItemRepairFingerprint(item)

	if item == nil {
		item = &EntryItem{Name: name, Files: make(map[string]*File)}
	}

	for fileName, file := range entry.Files {
		if existing, ok := item.Files[fileName]; ok {
			if file.AddedOn.After(existing.AddedOn) || (file.AddedOn.Equal(existing.AddedOn) && file.Size != existing.Size) {
				item.Files[fileName] = file
			}
		} else {
			item.Files[fileName] = file
		}
	}

	item.Size = item.GetSize()
	newFingerprint := EntryItemRepairFingerprint(item)
	pb := EntryItemToProto(item)
	if data, err := proto.Marshal(pb); err == nil {
		_ = s.entryItems.Put(name, data, nil)
	}
	if oldFingerprint != newFingerprint {
		s.MarkEntryDirty(name, entry.Protocol, "entry_item_changed")
	}
}

// removeFromEntryItem removes an entry from the name index
func (s *Storage) removeFromEntryItem(entry *Entry) {
	name := entry.GetFolder()
	if name == "" {
		return
	}

	data, err := s.entryItems.Get(name)
	if err != nil {
		return
	}

	var pb EntryItemProto
	if proto.Unmarshal(data, &pb) != nil {
		return
	}
	item := ProtoToEntryItem(&pb)

	for fileName := range entry.Files {
		if f, exists := item.Files[fileName]; exists && f.InfoHash == entry.InfoHash {
			delete(item.Files, fileName)
		}
	}

	if len(item.Files) == 0 {
		_ = s.entryItems.Delete(name)
		_ = s.DeleteEntryHealth(name)
		return
	}

	item.Size = item.GetSize()
	updatedPb := EntryItemToProto(item)
	if updatedData, err := proto.Marshal(updatedPb); err == nil {
		_ = s.entryItems.Put(name, updatedData, nil)
	}
	s.MarkEntryDirty(name, entry.Protocol, "entry_item_changed")
}

// Queue operations

// AddQueue adds an entry to the queue
func (s *Storage) AddQueue(entry *Entry) error {
	entry.CreatedAt = time.Now()
	return s.UpdateQueue(entry)
}

// UpdateQueue updates a queued entry
func (s *Storage) UpdateQueue(entry *Entry) error {
	entry.UpdatedAt = time.Now()

	pb := EntryToProto(entry)
	data, err := proto.Marshal(pb)
	if err != nil {
		return err
	}

	meta := &hybrid.EntryMeta{
		Category:  entry.Category,
		Provider:  entry.ActiveProvider,
		Status:    string(entry.Status),
		Name:      entry.GetFolder(), // Store computed folder name for fast listings
		TotalSize: entry.Size,
		Protocol:  string(entry.Protocol),
		Bad:       entry.Bad,
		AddedOn:   entry.AddedOn.Unix(),
	}

	return s.queue.Put(strings.ToLower(entry.InfoHash), data, meta)
}

// GetQueued retrieves a queued entry
func (s *Storage) GetQueued(infohash string) (*Entry, error) {
	data, err := s.queue.Get(strings.ToLower(infohash))
	if err != nil {
		return nil, err
	}

	var pb EntryProto
	if err := proto.Unmarshal(data, &pb); err != nil {
		return nil, err
	}
	return ProtoToEntry(&pb), nil
}

// DeleteQueued removes a queued entry
func (s *Storage) DeleteQueued(infohash string, cleanup func(*Entry) error) error {
	key := strings.ToLower(infohash)
	if cleanup != nil {
		if entry, err := s.GetQueued(key); err == nil {
			_ = cleanup(entry)
		}
	}
	return s.queue.Delete(key)
}

// FilterQueued returns entries matching a filter
func (s *Storage) FilterQueued(filter func(*Entry) bool) ([]*Entry, error) {
	var entries []*Entry
	_ = s.queue.ForEach(func(key string, value []byte) error {
		var pb EntryProto
		if proto.Unmarshal(value, &pb) == nil {
			entry := ProtoToEntry(&pb)
			if filter == nil || filter(entry) {
				entries = append(entries, entry)
			}
		}
		return nil
	})
	return entries, nil
}

// DeleteWhereQueued deletes matching queued entries
func (s *Storage) DeleteWhereQueued(predicate func(*Entry) bool, cleanup func(*Entry) error) error {
	var keysToDelete []string
	_ = s.queue.ForEach(func(key string, value []byte) error {
		var pb EntryProto
		if proto.Unmarshal(value, &pb) == nil {
			entry := ProtoToEntry(&pb)
			if predicate == nil || predicate(entry) {
				if cleanup != nil {
					_ = cleanup(entry)
				}
				keysToDelete = append(keysToDelete, key)
			}
		}
		return nil
	})

	for _, key := range keysToDelete {
		_ = s.queue.Delete(key)
	}
	return nil
}

// UpdateWhereQueued updates matching queued entries
func (s *Storage) UpdateWhereQueued(filter func(*Entry) bool, updateFunc func(*Entry) bool) error {
	type update struct {
		key   string
		entry *Entry
	}
	var updates []update

	_ = s.queue.ForEach(func(key string, value []byte) error {
		var pb EntryProto
		if proto.Unmarshal(value, &pb) == nil {
			entry := ProtoToEntry(&pb)
			if (filter == nil || filter(entry)) && updateFunc != nil && updateFunc(entry) {
				updates = append(updates, update{key, entry})
			}
		}
		return nil
	})

	for _, u := range updates {
		_ = s.UpdateQueue(u.entry)
	}
	return nil
}
