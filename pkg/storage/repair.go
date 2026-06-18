package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	json "github.com/bytedance/sonic"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage/hybrid"
)

// RepairStrategy controls how the probe groups files for a single entry.
type RepairStrategy string

const (
	RepairStrategyPerEntry RepairStrategy = "per_entry"
	RepairStrategyPerFile  RepairStrategy = "per_file"
)

type RepairRunStatus string
type RepairRunStage string
type RepairRunTrigger string

const (
	RepairRunRunning   RepairRunStatus = "running"
	RepairRunCompleted RepairRunStatus = "completed"
	RepairRunFailed    RepairRunStatus = "failed"
	RepairRunCancelled RepairRunStatus = "cancelled"
)

const (
	RepairStageSelecting RepairRunStage = "selecting"
	RepairStageProbing   RepairRunStage = "probing"
	RepairStageRepairing RepairRunStage = "repairing"
	RepairStageDone      RepairRunStage = "done"
)

const (
	RepairTriggerScheduled RepairRunTrigger = "scheduled"
	RepairTriggerManual    RepairRunTrigger = "manual"
)

type RepairRunStats struct {
	Candidates   int `json:"candidates"`
	SkippedFresh int `json:"skipped_fresh"`
	Probed       int `json:"probed"`
	Healthy      int `json:"healthy"`
	Broken       int `json:"broken"`
	Unknown      int `json:"unknown"`
	Repaired     int `json:"repaired"`
	Cleared      int `json:"cleared,omitempty"`
	RepairFailed int `json:"repair_failed"`
}

// RepairRun is the append-only history record produced by a single sweep.
// Counters and stage are mutated live during the run so the status endpoint
// can report progress without holding any in-memory state.
type RepairRun struct {
	ID           string           `json:"id"`
	Trigger      RepairRunTrigger `json:"trigger"`
	Status       RepairRunStatus  `json:"status"`
	Stage        RepairRunStage   `json:"stage,omitempty"`
	StartedAt    time.Time        `json:"started_at"`
	UpdatedAt    time.Time        `json:"updated_at"`
	CompletedAt  time.Time        `json:"completed_at,omitempty"`
	Stats        RepairRunStats   `json:"stats"`
	Error        string           `json:"error,omitempty"`
	CancelReason string           `json:"cancel_reason,omitempty"`
	Source       string           `json:"source,omitempty"`
}

// NormalizeRepairStrategy maps user-supplied values to a known strategy.
// Unknown / empty input falls back to per_entry.
func NormalizeRepairStrategy(strategy RepairStrategy) RepairStrategy {
	switch strategy {
	case RepairStrategyPerFile:
		return RepairStrategyPerFile
	default:
		return RepairStrategyPerEntry
	}
}

func (s *Storage) SaveRepairRun(run *RepairRun) error {
	if run == nil || run.ID == "" {
		return fmt.Errorf("repair run is missing id")
	}
	run.UpdatedAt = time.Now()
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return s.repairRuns.Put(run.ID, data, nil)
}

func (s *Storage) GetRepairRun(id string) (*RepairRun, error) {
	if id == "" {
		return nil, fmt.Errorf("repair run id is empty")
	}
	data, err := s.repairRuns.Get(id)
	if err != nil {
		return nil, err
	}
	var run RepairRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	if run.ID == "" {
		run.ID = id
	}
	return &run, nil
}

// ListRepairRuns returns runs sorted newest-first.
func (s *Storage) ListRepairRuns() ([]*RepairRun, error) {
	runs := make([]*RepairRun, 0)
	err := s.repairRuns.ForEach(func(key string, value []byte) error {
		var run RepairRun
		if err := json.Unmarshal(value, &run); err != nil {
			return nil
		}
		if run.ID == "" {
			run.ID = key
		}
		runs = append(runs, &run)
		return nil
	})
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.After(runs[j].StartedAt)
	})
	return runs, err
}

func (s *Storage) DeleteRepairRun(id string) error {
	if id == "" {
		return nil
	}
	return s.repairRuns.Delete(id)
}

// PruneRepairRuns keeps the newest `keep` runs and deletes the rest. Runs in
// status running are always retained.
func (s *Storage) PruneRepairRuns(keep int) error {
	if keep <= 0 {
		keep = 100
	}
	runs, err := s.ListRepairRuns()
	if err != nil {
		return err
	}
	if len(runs) <= keep {
		return nil
	}
	for _, run := range runs[keep:] {
		if run.Status == RepairRunRunning {
			continue
		}
		_ = s.repairRuns.Delete(run.ID)
	}
	return nil
}

// ClearRepairRuns deletes every non-running run.
func (s *Storage) ClearRepairRuns() error {
	runs, err := s.ListRepairRuns()
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.Status == RepairRunRunning {
			continue
		}
		_ = s.repairRuns.Delete(run.ID)
	}
	return nil
}

// HealthStatus is the rolled-up state of an entry as seen by the repair system.
type HealthStatus string

const (
	HealthUnknown     HealthStatus = "unknown"
	HealthHealthy     HealthStatus = "healthy"
	HealthBroken      HealthStatus = "broken"
	HealthRepairing   HealthStatus = "repairing"
	HealthUnsupported HealthStatus = "unsupported"
	HealthStale       HealthStatus = "stale"
)

// ArrKind narrows BrokenFile.ArrName to a typed Arr (sonarr / radarr / ...).
type ArrKind string

const (
	ArrKindSonarr  ArrKind = "sonarr"
	ArrKindRadarr  ArrKind = "radarr"
	ArrKindLidarr  ArrKind = "lidarr"
	ArrKindReadarr ArrKind = "readarr"
	ArrKindOther   ArrKind = "other"
)

// BrokenFile carries everything the repair pipeline needs to act on a single
// broken file: where it lives in storage, which infohash it belongs to, and —
// when an Arr knows about it — the Arr-side identifiers needed to delete and
// re-search without another lookup.
type BrokenFile struct {
	EntryName string          `json:"entry_name"`
	FileName  string          `json:"file_name"`
	InfoHash  string          `json:"info_hash,omitempty"`
	Protocol  config.Protocol `json:"protocol,omitempty"`
	Reason    string          `json:"reason,omitempty"`
	Size      int64           `json:"size,omitempty"`

	// Arr re-acquire payload. Empty when source=managed or when no Arr owns
	// the file.
	ArrName    string  `json:"arr_name,omitempty"`
	ArrKind    ArrKind `json:"arr_kind,omitempty"`
	MediaID    int     `json:"media_id,omitempty"`
	EpisodeID  int     `json:"episode_id,omitempty"`
	ArrFileID  int     `json:"arr_file_id,omitempty"`
	TargetPath string  `json:"target_path,omitempty"`
	SourcePath string  `json:"source_path,omitempty"`
}

// EntryHealth is the source of truth for repair decisions. It is keyed by
// EntryName (the folder-name shared across files of the same release) and is
// updated live during a sweep — once when probing starts, once when it
// finishes.
type EntryHealth struct {
	EntryName     string          `json:"entry_name"`
	Protocol      config.Protocol `json:"protocol,omitempty"`
	Status        HealthStatus    `json:"status"`
	Fingerprint   string          `json:"fingerprint,omitempty"`
	FileCount     int             `json:"file_count"`
	BrokenCount   int             `json:"broken_count"`
	BrokenFiles   []BrokenFile    `json:"broken_files,omitempty"`
	FailureReason string          `json:"failure_reason,omitempty"`

	Dirty       bool   `json:"dirty"`
	DirtyReason string `json:"dirty_reason,omitempty"`

	LastCheckedAt  time.Time    `json:"last_checked_at,omitempty"`
	LastOKAt       time.Time    `json:"last_ok_at,omitempty"`
	LastFailedAt   time.Time    `json:"last_failed_at,omitempty"`
	LastRepairAt   time.Time    `json:"last_repair_at,omitempty"`
	NextCheckDueAt time.Time    `json:"next_check_due_at,omitempty"`
	ActiveRunID    string       `json:"active_run_id,omitempty"`
	PreviousStatus HealthStatus `json:"previous_status,omitempty"`

	UpdatedAt time.Time `json:"updated_at"`
}

// IsDue reports whether this entry should be visited by the next sweep, given a
// recheck interval. Entries that have never been checked, that are dirty, or
// whose last status was anything other than healthy/unsupported are always due.
func (h *EntryHealth) IsDue(now time.Time, recheck time.Duration) bool {
	if h == nil {
		return true
	}
	if h.Dirty {
		return true
	}
	if h.LastCheckedAt.IsZero() {
		return true
	}
	switch h.Status {
	case HealthHealthy, HealthUnsupported:
		// fall through to staleness check
	default:
		return true
	}
	if recheck <= 0 {
		return false
	}
	return now.Sub(h.LastCheckedAt) >= recheck
}

func (s *Storage) SaveEntryHealth(state *EntryHealth) error {
	if state == nil || state.EntryName == "" {
		return fmt.Errorf("entry health is missing entry name")
	}
	state.UpdatedAt = time.Now()
	state.BrokenCount = len(state.BrokenFiles)
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	// Index the status so CountEntryHealthByStatus can build its histogram
	// straight from the in-memory index without decoding every record.
	return s.repairState.Put(state.EntryName, data, &hybrid.EntryMeta{Status: string(state.Status)})
}

func (s *Storage) GetEntryHealth(entryName string) (*EntryHealth, error) {
	if entryName == "" {
		return nil, fmt.Errorf("entry name is empty")
	}
	data, err := s.repairState.Get(entryName)
	if err != nil {
		return nil, err
	}
	var state EntryHealth
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	if state.EntryName == "" {
		state.EntryName = entryName
	}
	return &state, nil
}

func (s *Storage) ForEachEntryHealth(fn func(*EntryHealth) error) error {
	return s.repairState.ForEach(func(key string, value []byte) error {
		var state EntryHealth
		if err := json.Unmarshal(value, &state); err != nil {
			return nil
		}
		if state.EntryName == "" {
			state.EntryName = key
		}
		return fn(&state)
	})
}

func (s *Storage) DeleteEntryHealth(entryName string) error {
	if entryName == "" || !s.repairState.Exists(entryName) {
		return nil
	}
	return s.repairState.Delete(entryName)
}

// ClearEntryHealthByStatuses deletes persisted repair health records whose
// status matches one of the supplied statuses. It only clears repair state;
// it does not touch entries, files, Arrs, or debrid placements.
func (s *Storage) ClearEntryHealthByStatuses(statuses []HealthStatus) (int, error) {
	wanted := make(map[HealthStatus]struct{}, len(statuses))
	for _, status := range statuses {
		if status != "" {
			wanted[status] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return 0, nil
	}

	names := make([]string, 0)
	if err := s.ForEachEntryHealth(func(state *EntryHealth) error {
		if state == nil {
			return nil
		}
		if _, ok := wanted[state.Status]; ok {
			names = append(names, state.EntryName)
		}
		return nil
	}); err != nil {
		return 0, err
	}

	cleared := 0
	for _, name := range names {
		if err := s.DeleteEntryHealth(name); err != nil {
			return cleared, err
		}
		cleared++
	}
	return cleared, nil
}

// MarkEntryDirty flags an entry's health as out-of-date so the next sweep will
// re-probe it. Called from the storage layer whenever the underlying file set
// of an entry mutates.
func (s *Storage) MarkEntryDirty(entryName string, protocol config.Protocol, reason string) {
	if entryName == "" {
		return
	}
	state, err := s.GetEntryHealth(entryName)
	if err != nil || state == nil {
		state = &EntryHealth{EntryName: entryName, Status: HealthUnknown}
	}
	if protocol != "" {
		state.Protocol = protocol
	}
	state.Dirty = true
	state.DirtyReason = reason
	state.NextCheckDueAt = time.Time{}
	_ = s.SaveEntryHealth(state)
}

// healthCountsTTL bounds how often CountEntryHealthByStatus scans the entire
// repair-state store. The histogram is consumed by the stats dashboard, so a
// few seconds of staleness is acceptable; the alternative is a full scan +
// JSON-unmarshal per call, which dominated heap churn at scale.
const healthCountsTTL = 30 * time.Second

// CountEntryHealthByStatus returns a per-status histogram without loading full
// EntryHealth payloads. The result is cached for healthCountsTTL and
// invalidated whenever repair state mutates.
func (s *Storage) CountEntryHealthByStatus() map[HealthStatus]int {
	s.healthCountsMu.Lock()
	if s.healthCounts != nil && time.Since(s.healthCountsBuiltAt) < healthCountsTTL {
		out := make(map[HealthStatus]int, len(s.healthCounts))
		for k, v := range s.healthCounts {
			out[k] = v
		}
		s.healthCountsMu.Unlock()
		return out
	}
	s.healthCountsMu.Unlock()

	counts := make(map[HealthStatus]int)
	// Fast path: read the status straight from the index (no disk read, no
	// JSON decode). Records persisted before the status was indexed have an
	// empty meta.Status; collect those and decode them after the iteration so
	// we never call Get (which RLocks) while ForEachMeta holds the read lock.
	// This self-heals: the next SaveEntryHealth (every sweep) populates the
	// index, so the fallback set shrinks to zero.
	var needDecode []string
	_ = s.repairState.ForEachMeta(func(key string, meta *hybrid.IndexEntry) error {
		if meta.Status != "" {
			counts[HealthStatus(meta.Status)]++
		} else {
			needDecode = append(needDecode, key)
		}
		return nil
	})
	for _, key := range needDecode {
		data, err := s.repairState.Get(key)
		if err != nil {
			continue
		}
		var stub struct {
			Status HealthStatus `json:"status"`
		}
		if json.Unmarshal(data, &stub) == nil && stub.Status != "" {
			counts[stub.Status]++
		}
	}

	s.healthCountsMu.Lock()
	s.healthCounts = counts
	s.healthCountsBuiltAt = time.Now()
	s.healthCountsMu.Unlock()

	out := make(map[HealthStatus]int, len(counts))
	for k, v := range counts {
		out[k] = v
	}
	return out
}

// EntryItemRepairFingerprint produces a deterministic hash of the file set
// inside an EntryItem. When this hash changes between two snapshots, the
// repair system knows the underlying files changed and the entry needs to be
// re-probed even if its last status was healthy.
func EntryItemRepairFingerprint(item *EntryItem) string {
	if item == nil || len(item.Files) == 0 {
		return ""
	}

	names := make([]string, 0, len(item.Files))
	for name := range item.Files {
		names = append(names, name)
	}
	sort.Strings(names)

	h := sha256.New()
	for _, name := range names {
		file := item.Files[name]
		if file == nil {
			continue
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write([]byte(file.InfoHash))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(file.Size, 10)))
		h.Write([]byte{0})
		if file.Deleted {
			h.Write([]byte("deleted"))
		}
		if file.ByteRange != nil {
			h.Write([]byte(strconv.FormatInt(file.ByteRange[0], 10)))
			h.Write([]byte{':'})
			h.Write([]byte(strconv.FormatInt(file.ByteRange[1], 10)))
		}
		h.Write([]byte{0xff})
	}
	return hex.EncodeToString(h.Sum(nil))
}
