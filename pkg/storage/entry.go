package storage

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/sirrobot01/appendstore"
	"google.golang.org/protobuf/proto"
)

// AddOrUpdate adds or updates an entry
func (s *Storage) AddOrUpdate(entry *Entry) error {
	entry.UpdatedAt = time.Now()

	if err := s.assignFileIDs(entry); err != nil {
		return err
	}

	// Serialize
	pb := EntryToProto(entry)
	data, err := proto.Marshal(pb)
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}

	if err := s.entries.Put(entry.InfoHash, data, entryPutOptions(entry)); err != nil {
		return fmt.Errorf("save entry %q: %w", entry.InfoHash, err)
	}
	return s.updateEntryItem(entry)
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

// assignFileIDs gives every file a stable ID before it is persisted. Callers
// often rebuild entries from provider responses, so IDs already persisted for
// this infohash are carried over by filename; only genuinely new files get a
// fresh ID.
func (s *Storage) assignFileIDs(entry *Entry) error {
	missing := false
	for _, f := range entry.Files {
		if f.ID == "" {
			missing = true
			break
		}
	}
	if !missing {
		return nil
	}
	existing, err := s.Get(entry.InfoHash)
	if err != nil && !errors.Is(err, appendstore.ErrKeyNotFound) {
		return fmt.Errorf("read entry %q to preserve file IDs: %w", entry.InfoHash, err)
	}
	if existing != nil {
		for name, f := range entry.Files {
			if f.ID == "" {
				if old, ok := existing.Files[name]; ok {
					f.ID = old.ID
				}
			}
		}
	}
	for _, f := range entry.Files {
		if f.ID == "" {
			f.ID = NewFileID()
		}
	}
	return nil
}

// NewFileID returns a random stable file identifier.
func NewFileID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
	Category string
	Bad      bool
}

// ForEachMeta iterates over entry metadata without reading full entries from disk.
// This is O(n) in-memory only - no disk reads, no protobuf deserialization.
func (s *Storage) ForEachMeta(fn func(*EntryMetaInfo) error) error {
	return s.entries.ForEachMetadata(func(key string, meta *appendstore.Metadata) error {
		return fn(&EntryMetaInfo{
			InfoHash: key,
			Name:     meta.Attribute(attributeName),
			Size:     metadataInt64(meta, attributeTotalSize),
			AddedOn:  time.Unix(metadataInt64(meta, attributeAddedOn), 0),
			Provider: meta.Attribute(attributeProvider),
			Protocol: meta.Attribute(attributeProtocol),
			Category: meta.Attribute(attributeCategory),
			Bad:      metadataBool(meta, attributeBad),
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
	_ = s.entries.ForEachMetadata(func(key string, meta *appendstore.Metadata) error {
		// Skip special keys
		if strings.HasPrefix(key, "__") {
			return nil
		}
		// Check if metadata needs migration (Protocol empty = old format)
		if meta.Attribute(attributeProtocol) == "" {
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

// Delete removes an entry.
func (s *Storage) Delete(infohash string) error {
	entry, err := s.Get(infohash)
	if err != nil {
		return fmt.Errorf("read entry %q before deletion: %w", infohash, err)
	}
	if err := s.removeFromEntryItem(entry); err != nil {
		return err
	}
	if err := s.entries.Delete(infohash); err != nil {
		return fmt.Errorf("delete entry %q: %w", infohash, err)
	}
	return nil
}

// Count returns the number of entries
func (s *Storage) Count() (int, error) {
	return s.entries.Len(), nil
}
