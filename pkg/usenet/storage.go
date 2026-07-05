package usenet

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sourcegraph/conc/pool"
	"google.golang.org/protobuf/proto"
)

const (
	metaFileExtension = ".meta"
	metaDirName       = "meta"
	// metaMigrationMarker is written to the meta dir once all legacy proto
	// files have been upgraded to the v2 codec, so migration runs at most once.
	metaMigrationMarker = ".codec-v2.done"
)

const (
	NZBStatusPending     = "pending"
	NZBStatusParsing     = "parsing"
	NZBStatusDownloading = "downloading"
	NZBStatusCompleted   = "completed"
	NZBStatusFailed      = "failed"
)

// NZBStorage handles file-based persistence of NZB metadata using protobuf
type NZBStorage struct {
	metaDir string
	logger  zerolog.Logger
	mu      sync.RWMutex // Protects file operations and cached stats

	// Cached stats for fast Stats() reads without filesystem scans.
	metaCount      int
	metaTotalBytes int64
}

// NewNZBStorage creates a new file-based NZB storage
func NewNZBStorage() (*NZBStorage, error) {
	metaDir := filepath.Join(config.GetMainPath(), "usenet", metaDirName)
	if err := os.MkdirAll(metaDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create meta directory: %w", err)
	}

	s := &NZBStorage{
		metaDir: metaDir,
		logger:  logger.New("nzb-storage"),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.recalculateStatsLocked(); err != nil {
		return nil, fmt.Errorf("failed to initialize NZB stats cache: %w", err)
	}

	return s, nil
}

// metaFilePath returns the path for a given NZB ID
func (s *NZBStorage) metaFilePath(id string) string {
	return filepath.Join(s.metaDir, id+metaFileExtension)
}

// recalculateStatsLocked rebuilds cached stats by scanning metadata files.
// Caller must hold s.mu.
func (s *NZBStorage) recalculateStatsLocked() error {
	entries, err := os.ReadDir(s.metaDir)
	if err != nil {
		return fmt.Errorf("failed to read meta directory: %w", err)
	}

	count := 0
	var totalSize int64
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != metaFileExtension {
			continue
		}
		count++
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("failed to stat meta file %s: %w", entry.Name(), err)
		}
		totalSize += info.Size()
	}

	s.metaCount = count
	s.metaTotalBytes = totalSize
	return nil
}

// AddNZB saves an NZB to file storage
func (s *NZBStorage) AddNZB(nzb *storage.NZB) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := encodeNZBV2(nzb)
	if err != nil {
		return fmt.Errorf("failed to encode NZB: %w", err)
	}

	path := s.metaFilePath(nzb.ID)
	var oldSize int64
	alreadyExists := false
	if info, statErr := os.Stat(path); statErr == nil {
		alreadyExists = true
		oldSize = info.Size()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to stat existing NZB meta file: %w", statErr)
	}

	// Write atomically using temp file
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write NZB meta file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("failed to rename NZB meta file: %w", err)
	}

	newSize := int64(len(data))
	if alreadyExists {
		s.metaTotalBytes += newSize - oldSize
	} else {
		s.metaCount++
		s.metaTotalBytes += newSize
	}

	return nil
}

// GetNZB retrieves an NZB from file storage
func (s *NZBStorage) GetNZB(id string) (*storage.NZB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.metaFilePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nzb not found: %s", id)
		}
		return nil, fmt.Errorf("failed to read NZB meta file: %w", err)
	}

	return decodeNZB(data)
}

// GetNZBHeader retrieves an NZB without its segment map. It is far cheaper than
// GetNZB for the common case of only needing scalar/file metadata (status,
// path, file list). For legacy proto files it falls back to a full decode.
func (s *NZBStorage) GetNZBHeader(id string) (*storage.NZB, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.metaFilePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nzb not found: %s", id)
		}
		return nil, fmt.Errorf("failed to read NZB meta file: %w", err)
	}

	if isCodecV2(data) {
		return decodeNZBV2Header(data)
	}
	return decodeNZB(data)
}

// SampleFileMessageIDs returns the sampled message ids for a single file,
// used by availability/repair probes. For v2 blobs it decodes only that file's
// sampled ids (no numeric columns, no NZBSegment allocation, no other files),
// which keeps repair sweeps from holding full segment maps in memory. Legacy
// proto files fall back to a full decode. A nil slice with nil error means the
// file was not found or has no segments.
func (s *NZBStorage) SampleFileMessageIDs(id, filename string, percent int) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.metaFilePath(id)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("nzb not found: %s", id)
		}
		return nil, fmt.Errorf("failed to read NZB meta file: %w", err)
	}

	if isCodecV2(data) {
		ids, _, err := decodeFileMessageIDsSampled(data, filename, percent)
		return ids, err
	}

	// Legacy proto: full decode then sample in memory.
	nzb, err := decodeNZB(data)
	if err != nil {
		return nil, err
	}
	f := nzb.GetFileByName(filename)
	if f == nil || len(f.Segments) == 0 {
		return nil, nil
	}
	want := sampleIndices(len(f.Segments), percent)
	ids := make([]string, 0, len(want))
	for _, idx := range want {
		ids = append(ids, f.Segments[idx].MessageID)
	}
	return ids, nil
}

// decodeNZB decodes a meta blob, supporting both the v2 codec and legacy
// protobuf files (which migrate to v2 on their next write).
func decodeNZB(data []byte) (*storage.NZB, error) {
	if isCodecV2(data) {
		return decodeNZBV2(data)
	}

	var pb NZBProto
	if err := proto.Unmarshal(data, &pb); err != nil {
		return nil, fmt.Errorf("failed to unmarshal NZB: %w", err)
	}
	return protoToNZB(&pb), nil
}

// DeleteNZB removes an NZB from file storage
func (s *NZBStorage) DeleteNZB(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.metaFilePath(id)
	var oldSize int64
	alreadyExists := false
	if info, statErr := os.Stat(path); statErr == nil {
		alreadyExists = true
		oldSize = info.Size()
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("failed to stat NZB meta file before delete: %w", statErr)
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to delete NZB meta file: %w", err)
	}

	if alreadyExists {
		if s.metaCount > 0 {
			s.metaCount--
		}
		s.metaTotalBytes -= oldSize
		if s.metaTotalBytes < 0 {
			s.metaTotalBytes = 0
		}
	}

	return nil
}

// ForEachNZB iterates over all NZBs in storage
func (s *NZBStorage) ForEachNZB(fn func(*storage.NZB) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.metaDir)
	if err != nil {
		return fmt.Errorf("failed to read meta directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != metaFileExtension {
			continue
		}

		path := filepath.Join(s.metaDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			s.logger.Warn().Err(err).Str("file", entry.Name()).Msg("Failed to read NZB meta file")
			continue
		}

		nzb, err := decodeNZB(data)
		if err != nil {
			s.logger.Warn().Err(err).Str("file", entry.Name()).Msg("Failed to decode NZB")
			continue
		}

		if err := fn(nzb); err != nil {
			return err
		}
	}

	return nil
}

// GetAllNZBIDs returns all NZB IDs in storage
func (s *NZBStorage) GetAllNZBIDs() ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.metaDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read meta directory: %w", err)
	}

	var ids []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != metaFileExtension {
			continue
		}
		// Extract ID from filename (remove .meta extension)
		id := entry.Name()[:len(entry.Name())-len(metaFileExtension)]
		ids = append(ids, id)
	}

	return ids, nil
}

// Exists checks if an NZB exists in storage
func (s *NZBStorage) Exists(id string) bool {
	path := s.metaFilePath(id)
	_, err := os.Stat(path)
	return err == nil
}

// Count returns the number of NZBs in storage
func (s *NZBStorage) Count() (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.metaCount, nil
}

// Stats returns storage statistics
func (s *NZBStorage) Stats() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return map[string]any{
		"count":       s.metaCount,
		"total_bytes": s.metaTotalBytes,
		"meta_dir":    s.metaDir,
	}
}

// MigrateLegacy rewrites any legacy protobuf .meta files to the v2 codec,
// reclaiming the ~4x size difference for NZBs that aren't otherwise re-saved.
//
// It runs at most once: a marker file is written after a clean pass, so
// subsequent calls (e.g. every restart) return immediately without scanning the
// directory. The heavy decode/encode work runs lock-free across a small worker
// pool; the storage lock is taken only briefly per file for a re-check + atomic
// rename, so a multi-thousand-file migration neither blocks startup nor starves
// concurrent readers. Each rewrite uses temp-file + atomic rename, so readers
// always observe a fully-decodable file (old proto or new v2). Decode failures
// are logged and skipped rather than aborting. Returns the number migrated.
func (s *NZBStorage) MigrateLegacy() (int, error) {
	if s.migrationMarkerExists() {
		return 0, nil
	}

	s.mu.RLock()
	entries, err := os.ReadDir(s.metaDir)
	s.mu.RUnlock()
	if err != nil {
		return 0, fmt.Errorf("failed to read meta directory: %w", err)
	}

	// Cheap first-byte probe (lock-free) to collect only the legacy files.
	var legacy []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != metaFileExtension {
			continue
		}
		path := filepath.Join(s.metaDir, entry.Name())
		v2, err := fileIsCodecV2(path)
		if err != nil {
			s.logger.Warn().Err(err).Str("file", entry.Name()).Msg("Migration: failed to probe file")
			continue
		}
		if !v2 {
			legacy = append(legacy, path)
		}
	}

	if len(legacy) == 0 {
		s.writeMigrationMarker()
		return 0, nil
	}

	s.logger.Info().Int("legacy", len(legacy)).Msg("Migration: upgrading legacy NZB meta to v2")

	var migrated, failed atomic.Int64
	pl := pool.New().WithMaxGoroutines(min(runtime.NumCPU(), 6))

	for _, path := range legacy {
		pl.Go(func() {
			ok, err := s.migrateFile(path)
			if err != nil {
				s.logger.Warn().Err(err).Str("file", filepath.Base(path)).Msg("Migration: failed to migrate file")
				failed.Add(1)
				return
			}
			if ok {
				if n := migrated.Add(1); n%1000 == 0 {
					s.logger.Info().Int64("migrated", n).Int("total", len(legacy)).Msg("Migration: progress")
				}
			}
		})
	}

	pl.Wait()

	// Recompute cached stats once from disk rather than racing per-file deltas.
	s.mu.Lock()
	_ = s.recalculateStatsLocked()
	s.mu.Unlock()

	if failed.Load() == 0 {
		s.writeMigrationMarker()
	}
	s.logger.Info().Int64("migrated", migrated.Load()).Int64("failed", failed.Load()).Msg("Migration: completed legacy NZB meta upgrade")
	return int(migrated.Load()), nil
}

// migrateFile re-encodes one legacy proto meta file to v2. The expensive
// read/decode/encode runs lock-free; the storage lock is held only for the
// final re-check + atomic rename so a concurrent AddNZB can't be clobbered
// (AddNZB always writes v2, so a file that became v2 meanwhile is skipped).
func (s *NZBStorage) migrateFile(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Errorf("read: %w", err)
	}
	if isCodecV2(data) {
		return false, nil
	}

	nzb, err := decodeNZB(data)
	if err != nil {
		return false, fmt.Errorf("decode: %w", err)
	}
	out, err := encodeNZBV2(nzb)
	if err != nil {
		return false, fmt.Errorf("encode: %w", err)
	}

	// Unique temp name so it can't collide with AddNZB's "<path>.tmp".
	tmpPath := path + ".v2tmp"
	if err := os.WriteFile(tmpPath, out, 0644); err != nil {
		return false, fmt.Errorf("write temp: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// If AddNZB rewrote this file as v2 while we were encoding, its content is
	// newer — don't overwrite it with our re-encoded older copy.
	if cur, cerr := fileIsCodecV2(path); cerr == nil && cur {
		_ = os.Remove(tmpPath)
		return false, nil
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return false, fmt.Errorf("rename: %w", err)
	}
	return true, nil
}

func (s *NZBStorage) migrationMarkerPath() string {
	return filepath.Join(s.metaDir, metaMigrationMarker)
}

func (s *NZBStorage) migrationMarkerExists() bool {
	_, err := os.Stat(s.migrationMarkerPath())
	return err == nil
}

func (s *NZBStorage) writeMigrationMarker() {
	if err := os.WriteFile(s.migrationMarkerPath(), []byte("v2\n"), 0644); err != nil {
		s.logger.Warn().Err(err).Msg("Migration: failed to write completion marker")
	}
}

// fileIsCodecV2 cheaply reports whether a meta file already uses the v2 codec
// by reading only its first byte.
func fileIsCodecV2(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	var b [1]byte
	n, err := f.Read(b[:])
	if err != nil && err != io.EOF {
		return false, err
	}
	return n == 1 && b[0] == codecMagicV2, nil
}

// ============================================================================
// Conversion functions between storage.NZB and NZBProto
// ============================================================================

func nzbToProto(nzb *storage.NZB) *NZBProto {
	pb := &NZBProto{
		Id:               nzb.ID,
		Name:             nzb.Name,
		Title:            nzb.Title,
		Path:             nzb.Path,
		TotalSize:        nzb.TotalSize,
		DatePostedUnix:   nzb.DatePosted.Unix(),
		Category:         nzb.Category,
		Groups:           nzb.Groups,
		Downloaded:       nzb.Downloaded,
		AddedOnUnix:      nzb.AddedOn.Unix(),
		LastActivityUnix: nzb.LastActivity.Unix(),
		Status:           nzb.Status,
		Progress:         nzb.Progress,
		Percentage:       nzb.Percentage,
		SizeDownloaded:   nzb.SizeDownloaded,
		Eta:              nzb.ETA,
		Speed:            nzb.Speed,
		CompletedOnUnix:  nzb.CompletedOn.Unix(),
		IsBad:            nzb.IsBad,
		Storage:          nzb.Storage,
		FailMessage:      nzb.FailMessage,
		Password:         nzb.Password,
	}

	pb.Files = make([]*NZBFileProto, len(nzb.Files))
	for i, f := range nzb.Files {
		pb.Files[i] = nzbFileToProto(&f)
	}

	return pb
}

func nzbFileToProto(f *storage.NZBFile) *NZBFileProto {
	pb := &NZBFileProto{
		NzbId:         f.NzbID,
		Name:          f.Name,
		InternalPath:  f.InternalPath,
		Size:          f.Size,
		StartOffset:   f.StartOffset,
		Groups:        f.Groups,
		FileType:      string(f.FileType),
		Password:      f.Password,
		IsDeleted:     f.IsDeleted,
		IsStored:      f.IsStored,
		SegmentSize:   f.SegmentSize,
		EncryptionKey: f.EncryptionKey,
		EncryptionIv:  f.EncryptionIV,
		IsEncrypted:   f.IsEncrypted,
	}

	pb.Segments = make([]*NZBSegmentProto, len(f.Segments))
	for i, s := range f.Segments {
		pb.Segments[i] = &NZBSegmentProto{
			Number:           int32(s.Number),
			MessageId:        s.MessageID,
			Bytes:            s.Bytes,
			StartOffset:      s.StartOffset,
			EndOffset:        s.EndOffset,
			Group:            s.Group,
			SegmentDataStart: s.SegmentDataStart,
		}
	}

	return pb
}

func protoToNZB(pb *NZBProto) *storage.NZB {
	nzb := &storage.NZB{
		ID:             pb.Id,
		Name:           pb.Name,
		Title:          pb.Title,
		Path:           pb.Path,
		TotalSize:      pb.TotalSize,
		DatePosted:     time.Unix(pb.DatePostedUnix, 0),
		Category:       pb.Category,
		Groups:         pb.Groups,
		Downloaded:     pb.Downloaded,
		AddedOn:        time.Unix(pb.AddedOnUnix, 0),
		LastActivity:   time.Unix(pb.LastActivityUnix, 0),
		Status:         pb.Status,
		Progress:       pb.Progress,
		Percentage:     pb.Percentage,
		SizeDownloaded: pb.SizeDownloaded,
		ETA:            pb.Eta,
		Speed:          pb.Speed,
		CompletedOn:    time.Unix(pb.CompletedOnUnix, 0),
		IsBad:          pb.IsBad,
		Storage:        pb.Storage,
		FailMessage:    pb.FailMessage,
		Password:       pb.Password,
	}

	nzb.Files = make([]storage.NZBFile, len(pb.Files))
	for i, f := range pb.Files {
		nzb.Files[i] = protoToNZBFile(f)
	}

	return nzb
}

func protoToNZBFile(pb *NZBFileProto) storage.NZBFile {
	f := storage.NZBFile{
		NzbID:         pb.NzbId,
		Name:          pb.Name,
		InternalPath:  pb.InternalPath,
		Size:          pb.Size,
		StartOffset:   pb.StartOffset,
		Groups:        pb.Groups,
		FileType:      storage.NZBFileType(pb.FileType),
		Password:      pb.Password,
		IsDeleted:     pb.IsDeleted,
		IsStored:      pb.IsStored,
		SegmentSize:   pb.SegmentSize,
		EncryptionKey: pb.EncryptionKey,
		EncryptionIV:  pb.EncryptionIv,
		IsEncrypted:   pb.IsEncrypted,
	}

	f.Segments = make([]storage.NZBSegment, len(pb.Segments))
	for i, s := range pb.Segments {
		f.Segments[i] = storage.NZBSegment{
			Number:           int(s.Number),
			MessageID:        s.MessageId,
			Bytes:            s.Bytes,
			StartOffset:      s.StartOffset,
			EndOffset:        s.EndOffset,
			Group:            s.Group,
			SegmentDataStart: s.SegmentDataStart,
		}
	}

	return f
}
