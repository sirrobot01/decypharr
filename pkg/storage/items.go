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
		if existing, ok := item.Files[fileName]; !ok || file.AddedOn.After(existing.AddedOn) || (file.AddedOn.Equal(existing.AddedOn) && file.Size != existing.Size) {
			item.Files[fileName] = file
		}
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
