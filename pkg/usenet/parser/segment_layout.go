package parser

import (
	"fmt"
	"sort"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

// segmentLayout is an immutable prefix index over decoded article segments.
// Archive parsers construct it once and use binary search for every file range
// instead of rescanning the entire NZB segment list per extracted file.
type segmentLayout struct {
	segments []storage.NZBSegment
	ends     []int64 // exclusive absolute end for each segment
	size     int64
}

func newSegmentLayout(segments []storage.NZBSegment) (*segmentLayout, error) {
	layout := &segmentLayout{
		segments: segments,
		ends:     make([]int64, len(segments)),
	}
	for index, segment := range segments {
		if segment.MessageID == "" {
			return nil, fmt.Errorf("segment %d has no message ID", index)
		}
		if segment.Bytes <= 0 {
			return nil, fmt.Errorf("segment %s has non-positive size %d", segment.MessageID, segment.Bytes)
		}
		end := layout.size + segment.Bytes
		if end < layout.size {
			return nil, fmt.Errorf("segment layout size overflows int64 at segment %s", segment.MessageID)
		}
		layout.ends[index] = end
		layout.size = end
	}
	return layout, nil
}

// validateVolumes proves that volume metadata describes the same contiguous
// byte stream as the indexed segments. This catches shifted archive offsets
// before a parser can emit a plausible-looking but corrupt logical file.
func (l *segmentLayout) validateVolumes(volumes []storage.ArchiveVolumeInfo) error {
	if len(volumes) == 0 {
		return fmt.Errorf("archive has no volume layout")
	}
	nextSegment := 0
	var total int64
	for index, volume := range volumes {
		if volume.SegmentStart != nextSegment || volume.SegmentStart < 0 || volume.SegmentEnd > len(l.segments) || volume.SegmentStart >= volume.SegmentEnd {
			return fmt.Errorf("invalid segment bounds [%d, %d) for archive volume %d", volume.SegmentStart, volume.SegmentEnd, index)
		}
		start := int64(0)
		if volume.SegmentStart > 0 {
			start = l.ends[volume.SegmentStart-1]
		}
		end := l.ends[volume.SegmentEnd-1]
		if got := end - start; got != volume.Size {
			return fmt.Errorf("archive volume %q segment size %d does not match declared size %d", volume.Name, got, volume.Size)
		}
		total += volume.Size
		if total < 0 {
			return fmt.Errorf("archive volume layout size overflows int64")
		}
		nextSegment = volume.SegmentEnd
	}
	if nextSegment != len(l.segments) || total != l.size {
		return fmt.Errorf("archive volume layout covers %d of %d segments and %d of %d bytes", nextSegment, len(l.segments), total, l.size)
	}
	return nil
}

// slice returns descriptors covering [offset, offset+length). When
// outputOffsets is true, StartOffset/EndOffset describe the extracted file;
// otherwise they are left zero for a caller that concatenates multiple parts.
func (l *segmentLayout) slice(offset, length int64, outputOffsets bool) ([]storage.NZBSegment, error) {
	if length <= 0 {
		return nil, nil
	}
	if offset < 0 {
		return nil, fmt.Errorf("negative offset: %d", offset)
	}
	end := offset + length
	if end < offset {
		return nil, fmt.Errorf("range overflows int64: offset=%d length=%d", offset, length)
	}
	if offset >= l.size || end > l.size {
		covered := max(int64(0), l.size-offset)
		covered = min(covered, length)
		return nil, fmt.Errorf("range [%d, %d] is only partially covered (%d of %d bytes)", offset, end-1, covered, length)
	}

	first := sort.Search(len(l.ends), func(index int) bool {
		return l.ends[index] > offset
	})
	last := sort.Search(len(l.ends), func(index int) bool {
		return l.ends[index] >= end
	})
	if first == len(l.ends) || last == len(l.ends) {
		return nil, fmt.Errorf("range [%d, %d] has no source segments", offset, end-1)
	}

	result := make([]storage.NZBSegment, 0, last-first+1)
	var outputPosition int64
	for index := first; index <= last; index++ {
		segment := l.segments[index]
		segmentStart := int64(0)
		if index > 0 {
			segmentStart = l.ends[index-1]
		}
		segmentEnd := l.ends[index]
		overlapStart := max(segmentStart, offset)
		overlapEnd := min(segmentEnd, end)
		bytesToRead := overlapEnd - overlapStart
		segment.SegmentDataStart += overlapStart - segmentStart
		segment.Bytes = bytesToRead
		segment.StartOffset = 0
		segment.EndOffset = 0
		if outputOffsets {
			segment.StartOffset = outputPosition
			segment.EndOffset = outputPosition + bytesToRead - 1
		}
		result = append(result, segment)
		outputPosition += bytesToRead
	}
	if outputPosition != length {
		return nil, fmt.Errorf("range [%d, %d] has a source gap (%d of %d bytes covered)", offset, end-1, outputPosition, length)
	}
	return result, nil
}
