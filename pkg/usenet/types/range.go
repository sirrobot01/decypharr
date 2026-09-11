package types

import (
	"io"

	"github.com/dylanmazurek/decypharr/pkg/storage"
)

// SegmentRange is the slice of segments that cover [TotalStart, TotalEnd],
// along with byte offsets relative to the first and last segment.
type SegmentRange struct {
	Segments   []*SegmentStream
	ByteStart  int64 // offset within Segments[0] where reading should begin
	ByteEnd    int64 // offset within last segment where reading should stop (not used by Reader directly)
	TotalStart int64 // absolute start byte in the file
	TotalEnd   int64 // absolute end byte in the file

	CurrentIndex int
}

// GetSegments returns the underlying NZBSegments for debugging/metrics.
func (sr *SegmentRange) GetSegments() []storage.NZBSegment {
	segments := make([]storage.NZBSegment, len(sr.Segments))
	for i, seg := range sr.Segments {
		segments[i] = seg.NZBSegment
	}
	return segments
}

// Next returns the next SegmentStream to be consumed by Reader.
func (sr *SegmentRange) Next() *SegmentStream {
	if len(sr.Segments) == 0 {
		return nil
	}
	if sr.CurrentIndex >= len(sr.Segments) {
		return nil
	}
	seg := sr.Segments[sr.CurrentIndex]
	sr.CurrentIndex++
	return seg
}

type SegmentStream struct {
	storage.NZBSegment

	// Hybrid streaming/caching approach:
	// Option 1: Zero-copy reference to cached data (fast path)
	dataRef []byte
	// Option 2: Streaming reader from NNTP (memory efficient path)
	stream io.ReadCloser
	// Current offset for dataRef reads
	Off int64
}

func NewSegmentStream(seg storage.NZBSegment) *SegmentStream {
	return &SegmentStream{
		NZBSegment: seg,
	}
}

// SetDataRef sets a zero-copy reference to the cached segment data.
// This does NOT copy the data, just stores a pointer to it.
// Used when data is already in cache (fast path).
func (ss *SegmentStream) SetDataRef(data []byte) {
	ss.dataRef = data
	ss.stream = nil // Clear stream if we have cached data
	ss.Off = 0
}

// SetStream sets a streaming reader for this segment.
// Used when streaming directly from NNTP without caching (memory efficient path).
func (ss *SegmentStream) SetStream(reader io.ReadCloser) {
	ss.stream = reader
	ss.dataRef = nil // Clear cached data if we have stream
	ss.Off = 0
}

// Read implements io.Reader with hybrid streaming/caching support.
// Reads from cached data if available, otherwise streams from NNTP.
func (ss *SegmentStream) Read(p []byte) (int, error) {
	if ss == nil {
		return 0, io.EOF
	}

	// Fast path: read from cached data reference
	if ss.dataRef != nil {
		if ss.Off >= int64(len(ss.dataRef)) {
			return 0, io.EOF
		}
		n := copy(p, ss.dataRef[ss.Off:])
		ss.Off += int64(n)
		if n == 0 {
			return 0, io.EOF
		}
		return n, nil
	}

	// Streaming path: read from NNTP stream
	if ss.stream != nil {
		return ss.stream.Read(p)
	}

	// No data source available
	return 0, io.EOF
}

// Close releases resources.
// For cached data: just clears the reference (cache manages the data).
// For streams: closes the underlying stream.
func (ss *SegmentStream) Close() {
	if ss == nil {
		return
	}

	// Close stream if active
	if ss.stream != nil {
		_ = ss.stream.Close()
		ss.stream = nil
	}

	// Clear reference (don't touch cached data - cache manages that)
	ss.dataRef = nil
	ss.Off = 0
}
