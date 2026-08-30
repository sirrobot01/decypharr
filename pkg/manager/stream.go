package manager

import (
	"sync/atomic"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// ActiveStream represents a currently active streaming file
type ActiveStream struct {
	ID         string `json:"id"`
	EntryName  string `json:"entry_name"`
	FileName   string `json:"file_name"`
	FileSize   int64  `json:"file_size"`
	Source     string `json:"source"` // "torrent" or "nzb"
	StartedAt  int64  `json:"started_at"`
	LastActive int64  `json:"last_active"` // Last activity timestamp; written atomically, see touchStream
	Resumes    int64  `json:"resumes"`     // Mid-stream recoveries; written atomically, see touchStream
	Debrid     string `json:"debrid,omitempty"`
	Client     string `json:"client,omitempty"` // Client identifier (User-Agent for WebDAV, "DFS" for DFS)
}

// === Active Streams Tracking ===

// registerStream registers an active stream for observability.
// Returns the stream ID so the caller can remove it when streaming completes.
func (m *Manager) registerStream(entryName, fileName string, fileSize int64, source, debrid, client string) string {
	// Use deterministic ID to ensure a single entry per file
	streamID := entryName + ":" + fileName
	now := utils.NowUnix()

	stream := &ActiveStream{
		ID:         streamID,
		EntryName:  entryName,
		FileName:   fileName,
		FileSize:   fileSize,
		Source:     source,
		StartedAt:  now,
		LastActive: now,
		Debrid:     debrid,
		Client:     client,
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

// GetActiveStreams returns a snapshot of all currently active streams.
func (m *Manager) GetActiveStreams() []*ActiveStream {
	var streams []*ActiveStream
	m.activeStreams.Range(func(_ string, stream *ActiveStream) bool {
		streams = append(streams, &ActiveStream{
			ID:         stream.ID,
			EntryName:  stream.EntryName,
			FileName:   stream.FileName,
			FileSize:   stream.FileSize,
			Source:     stream.Source,
			StartedAt:  stream.StartedAt,
			LastActive: atomic.LoadInt64(&stream.LastActive),
			Resumes:    atomic.LoadInt64(&stream.Resumes),
			Debrid:     stream.Debrid,
			Client:     stream.Client,
		})
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

	return m.registerStream(entry.Name, filename, file.Size, source, debrid, client)
}

// UntrackStream removes a previously-registered active stream if the ID is non-empty.
func (m *Manager) UntrackStream(streamID string) {
	m.unregisterStream(streamID)
}
