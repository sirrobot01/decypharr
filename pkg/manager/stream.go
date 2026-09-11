package manager

import (
	"slices"
	"sync"
	"sync/atomic"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// ActiveStream represents a currently active streaming file
type ActiveStream struct {
	ID             string   `json:"id"`
	EntryID        string   `json:"entry_id"`
	FileID         string   `json:"file_id"`
	EntryName      string   `json:"entry_name"`
	FileName       string   `json:"file_name"`
	FileSize       int64    `json:"file_size"`
	Source         string   `json:"source"` // "torrent" or "nzb"
	StartedAt      int64    `json:"started_at"`
	LastActive     int64    `json:"last_active"` // Last activity timestamp; written atomically, see touchStream
	Resumes        int64    `json:"resumes"`     // Mid-stream recoveries; written atomically, see touchStream
	Debrid         string   `json:"debrid,omitempty"`
	Client         string   `json:"client,omitempty"` // Client identifier (User-Agent for WebDAV, "DFS" for DFS)
	ArrName        string   `json:"arr_name,omitempty"`
	ArrType        arr.Type `json:"arr_type,omitempty"`
	SeriesID       int      `json:"series_id,omitzero"`
	SeasonNumber   int      `json:"season_number,omitzero"`
	EpisodeIDs     []int    `json:"episode_ids,omitempty"`
	MovieID        int      `json:"movie_id,omitzero"`
	ReacquireJobID string   `json:"reacquire_job_id,omitempty"`

	mu sync.RWMutex
}

// === Active Streams Tracking ===

// registerStream registers an active stream for observability.
// Returns the stream ID so the caller can remove it when streaming completes.
func (m *Manager) registerStream(entry *storage.Entry, fileName string, file *storage.File, source, debrid, client string) string {
	// Use deterministic ID to ensure a single entry per file
	streamID := entry.Name + ":" + fileName
	now := utils.NowUnix()

	stream := &ActiveStream{
		ID:         streamID,
		EntryID:    entry.InfoHash,
		FileID:     file.ID,
		EntryName:  entry.Name,
		FileName:   fileName,
		FileSize:   file.Size,
		Source:     source,
		StartedAt:  now,
		LastActive: now,
		Debrid:     debrid,
		Client:     client,
	}
	if binding, ok := m.lookupArrBinding(entry.InfoHash, file.ID); ok {
		stream.ArrName = binding.ArrName
		stream.ArrType = binding.ArrType
		stream.SeriesID = binding.SeriesID
		stream.SeasonNumber = binding.SeasonNumber
		stream.EpisodeIDs = binding.EpisodeIDs
		stream.MovieID = binding.MovieID
	}

	m.activeStreams.Store(streamID, stream)
	return streamID
}

// unregisterStream removes an active stream entry if it exists.
func (m *Manager) unregisterStream(streamID string) {
	if streamID == "" {
		return
	}
	m.activeStreams.Delete(streamID)
}

// touchStream records read activity on an active stream. LastActive and
// Resumes are the only fields mutated after registration, always atomically;
// readers go through GetActiveStreams, which snapshots them atomically.
func (m *Manager) touchStream(streamID string, resumes int64) {
	if stream, ok := m.activeStreams.Load(streamID); ok {
		atomic.StoreInt64(&stream.LastActive, utils.NowUnix())
		atomic.StoreInt64(&stream.Resumes, resumes)
	}
}

func (s *ActiveStream) setReacquireJobID(jobID string) {
	s.mu.Lock()
	s.ReacquireJobID = jobID
	s.mu.Unlock()
}

func (s *ActiveStream) snapshot() *ActiveStream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &ActiveStream{
		ID:             s.ID,
		EntryID:        s.EntryID,
		FileID:         s.FileID,
		EntryName:      s.EntryName,
		FileName:       s.FileName,
		FileSize:       s.FileSize,
		Source:         s.Source,
		StartedAt:      s.StartedAt,
		LastActive:     atomic.LoadInt64(&s.LastActive),
		Resumes:        atomic.LoadInt64(&s.Resumes),
		Debrid:         s.Debrid,
		Client:         s.Client,
		ArrName:        s.ArrName,
		ArrType:        s.ArrType,
		SeriesID:       s.SeriesID,
		SeasonNumber:   s.SeasonNumber,
		EpisodeIDs:     slices.Clone(s.EpisodeIDs),
		MovieID:        s.MovieID,
		ReacquireJobID: s.ReacquireJobID,
	}
}

// GetActiveStreams returns a snapshot of all currently active streams.
func (m *Manager) GetActiveStreams() []*ActiveStream {
	var streams []*ActiveStream
	m.activeStreams.Range(func(_ string, stream *ActiveStream) bool {
		streams = append(streams, stream.snapshot())
		return true
	})
	return streams
}

// GetActiveStreamsCount returns the number of active streams.
func (m *Manager) GetActiveStreamsCount() int {
	return m.activeStreams.Size()
}

// TrackStream registers an active stream for observability and returns the stream ID.
// Call UntrackStream with the returned ID when streaming completes. Used by
// consumers that manage their own byte transport (the vfs downloader) and so
// open sessions untracked; session-based consumers register automatically.
func (m *Manager) TrackStream(entry *storage.Entry, filename, client string) string {
	if entry == nil {
		return ""
	}
	file, ok := entry.Files[filename]
	if !ok {
		return ""
	}

	var source, debrid string
	if entry.Protocol == config.ProtocolNZB {
		source = "nzb"
	} else {
		source = "torrent"
		debrid = entry.ActiveProvider
	}

	return m.registerStream(entry, filename, file, source, debrid, client)
}

// UntrackStream removes a previously-registered active stream if the ID is non-empty.
func (m *Manager) UntrackStream(streamID string) {
	m.unregisterStream(streamID)
}
