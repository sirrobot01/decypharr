package storage

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/appendstore"
	"github.com/sirrobot01/decypharr/internal/logger"
	"google.golang.org/protobuf/proto"
)

var storeNames = []string{"entries", "queue", "items", "repair_state", "repair_runs"}

// legacyStoreNames are buckets from the v1 repair system. They are removed
// on startup so they don't accumulate dead data.
var legacyStoreNames = []string{"repair_jobs", "repair_keys"}

// Storage handles application persistence using appendstore.
type Storage struct {
	entries     *appendstore.Store
	queue       *appendstore.Store
	entryItems  *appendstore.Store
	repairState *appendstore.Store
	repairRuns  *appendstore.Store
	dir         string
	logger      zerolog.Logger

	healthCountsMu      sync.Mutex
	healthCounts        map[HealthStatus]int
	healthCountsBuiltAt time.Time
}

func createItemStores(baseDir string, baseOptions appendstore.Options) (map[string]*appendstore.Store, error) {
	items := make(map[string]*appendstore.Store)
	for _, name := range storeNames {
		path := filepath.Join(baseDir, name+".db")
		store, err := appendstore.Open(path, baseOptions)
		if err != nil {
			for _, it := range items {
				_ = it.Close()
			}
			if errors.Is(err, appendstore.ErrUnsupportedVersion) {
				return nil, fmt.Errorf("the %s database was written by a newer version of Decypharr and this build cannot read it. "+
					"Upgrade Decypharr, or restore the copy saved before the upgrade (a .bak file beside %s): %w", name, path, err)
			}
			return nil, fmt.Errorf("failed to create %s store: %w", name, err)
		}
		items[name] = store
	}
	return items, nil
}

func dropLegacyStores(baseDir string, log zerolog.Logger) {
	for _, name := range legacyStoreNames {
		path := filepath.Join(baseDir, name+".db")
		if _, err := os.Stat(path); err == nil {
			if err := os.RemoveAll(path); err != nil {
				log.Warn().Err(err).Str("path", path).Msg("Failed to remove legacy repair bucket")
			} else {
				log.Info().Str("path", path).Msg("Removed legacy repair bucket")
			}
		}
	}
}

func NewStorage(dbPath string) (*Storage, error) {
	dbPath = filepath.Clean(dbPath)
	if err := os.MkdirAll(dbPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create db directory: %w", err)
	}

	log := logger.New("storage")

	dropLegacyStores(dbPath, log)

	baseOptions := appendstore.Options{
		CacheSize:           5000,
		SyncInterval:        time.Second,
		CompactionThreshold: 0.5,
		AutoCompact:         true,
		IndexedFields:       []string{attributeCategory, attributeProvider, attributeStatus},
		OnError: func(err error) {
			log.Warn().Err(err).Msg("Storage background operation failed")
		},
		// appendstore keeps the pre-upgrade copy; surface where it landed,
		// because that file is the only way back to an older Decypharr.
		OnMigrate: func(info appendstore.MigrationInfo) error {
			log.Warn().
				Str("database", info.Path).
				Uint32("from_version", info.FromVersion).
				Uint32("to_version", info.ToVersion).
				Str("backup", info.Backup).
				Msg("Upgrading database format. Older Decypharr versions cannot read it — restore the backup to downgrade")
			return nil
		},
	}

	itemStores, err := createItemStores(dbPath, baseOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create item stores: %w", err)
	}

	s := &Storage{
		entries:     itemStores["entries"],
		queue:       itemStores["queue"],
		entryItems:  itemStores["items"],
		repairState: itemStores["repair_state"],
		repairRuns:  itemStores["repair_runs"],
		dir:         dbPath,
		logger:      log,
	}

	if count, err := s.MigrateMetadata(); err != nil {
		log.Warn().Err(err).Msg("Metadata migration failed")
	} else if count > 0 {
		log.Info().Int("count", count).Msg("Migrated entry metadata to new format")
	}

	return s, nil
}

func (s *Storage) Close() error {
	var errs []error
	stores := []*appendstore.Store{s.entries, s.queue, s.entryItems, s.repairState, s.repairRuns}
	for _, store := range stores {
		if store == nil {
			continue
		}
		if err := store.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("errors closing storage: %v", errs)
	}
	return nil
}

// DiskSize returns the total on-disk size of all stores (O(1), no filesystem walk).
func (s *Storage) DiskSize() int64 {
	var size int64
	for _, store := range []*appendstore.Store{s.entries, s.queue, s.entryItems, s.repairState, s.repairRuns} {
		if store != nil {
			size += store.DiskSize()
		}
	}
	return size
}

// SaveMigrationStatus saves the system migration status
func (s *Storage) SaveMigrationStatus(status *SystemMigrationStatus) error {
	pb := SystemMigrationStatusToProto(status)
	data, err := proto.Marshal(pb)
	if err != nil {
		return err
	}
	return s.entries.Put("__migration_status__", data, nil)
}

// GetMigrationStatus retrieves the system migration status
func (s *Storage) GetMigrationStatus() (*SystemMigrationStatus, error) {
	data, err := s.entries.Get("__migration_status__")
	if err != nil {
		return nil, err
	}
	var pb SystemMigrationStatusProto
	if err := proto.Unmarshal(data, &pb); err != nil {
		return nil, err
	}
	return ProtoToSystemMigrationStatus(&pb), nil
}

func (s *Storage) copyFrom(other *Storage) error {
	pairs := []struct {
		name string
		from *appendstore.Store
		to   *appendstore.Store
	}{
		{"entries", other.entries, s.entries},
		{"queue", other.queue, s.queue},
		{"items", other.entryItems, s.entryItems},
		{"repair_state", other.repairState, s.repairState},
		{"repair_runs", other.repairRuns, s.repairRuns},
	}

	for _, p := range pairs {
		if p.from == nil || p.to == nil {
			continue
		}
		if err := p.from.ForEach(func(key string, value []byte) error {
			return p.to.Put(key, value, nil)
		}); err != nil {
			return fmt.Errorf("failed to copy %s: %w", p.name, err)
		}
	}
	return nil
}
