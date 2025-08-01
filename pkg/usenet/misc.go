package usenet

import (
	"io"
	"strings"
)

func (s *Streamer) isSkippableError(err error) bool {
	if err == nil {
		return false
	}

	// EOF is usually expected/skippable
	if err == io.EOF {
		return true
	}

	errMsg := strings.ToLower(err.Error())

	// Client disconnection errors
	if strings.Contains(errMsg, "client disconnected") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "write failed") ||
		strings.Contains(errMsg, "writer is nil") ||
		strings.Contains(errMsg, "closed pipe") ||
		strings.Contains(errMsg, "context canceled") ||
		strings.Contains(errMsg, "operation timed out") ||
		strings.Contains(errMsg, "eof") {
		return true
	}

	return false
}

func RecalculateSegmentBoundaries(
	segments []NZBSegment,
	actualSizes map[string]int64,
) []NZBSegment {
	if len(segments) == 0 {
		return segments
	}

	result := make([]NZBSegment, len(segments))
	var currentOffset int64

	for i, seg := range segments {
		// Copy original segment metadata
		result[i] = seg
		result[i].StartOffset = currentOffset

		// Determine which size to use: actual decoded size, or fall back
		var size int64
		if actual, ok := actualSizes[seg.MessageID]; ok {
			size = actual
		} else {
			// decoded size as computed by parser (EndOffset-StartOffset)
			size = seg.EndOffset - seg.StartOffset
		}

		result[i].EndOffset = currentOffset + size
		currentOffset += size
	}

	return result
}

// GetSegmentActualSizes extracts actual decoded sizes from cache
func GetSegmentActualSizes(segments []NZBSegment, cache *SegmentCache) map[string]int64 {
	actualSizes := make(map[string]int64)

	if cache == nil {
		return actualSizes
	}

	for _, segment := range segments {
		if cached, found := cache.Get(segment.MessageID); found {
			actualSizes[segment.MessageID] = int64(len(cached.Data))
		}
	}

	return actualSizes
}
