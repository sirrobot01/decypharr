package usenet

import (
	"fmt"
	"strings"
)

type SegmentRange struct {
	Segment    NZBSegment // Reference to the segment
	ByteStart  int64      // Start offset within this segment
	ByteEnd    int64      // End offset within this segment
	TotalStart int64      // Absolute start position in file
	TotalEnd   int64      // Absolute end position in file
}

func (nzb *NZB) GetFileByName(name string) *NZBFile {
	for i := range nzb.Files {
		f := nzb.Files[i]
		if f.IsDeleted {
			continue
		}
		if nzb.Files[i].Name == name {
			return &nzb.Files[i]
		}
	}
	return nil
}

func (nzb *NZB) MarkFileAsRemoved(fileName string) error {
	for i, file := range nzb.Files {
		if file.Name == fileName {
			// Mark the file as deleted
			nzb.Files[i].IsDeleted = true
			return nil
		}
	}
	return fmt.Errorf("file %s not found in NZB %s", fileName, nzb.ID)
}

func (nf *NZBFile) GetSegmentsInRange(segmentSize int64, start, end int64) []SegmentRange {
	if end == -1 {
		end = nf.Size - 1
	}

	var segmentRanges []SegmentRange
	var cumulativeSize int64

	for i, segment := range nf.Segments {
		// Use the file's segment size (uniform)
		if segmentSize <= 0 {
			segmentSize = segment.Bytes // Fallback to actual segment size if not set
		}

		// Handle last segment which might be smaller
		if i == len(nf.Segments)-1 {
			segmentSize = segment.Bytes // Last segment uses actual size
		}

		cumulativeSize += segmentSize

		// Skip segments that end before our start position
		if cumulativeSize <= start {
			continue
		}

		// Calculate this segment's boundaries
		segmentStart := cumulativeSize - segmentSize
		segmentEnd := cumulativeSize - 1

		// Calculate intersection with requested range
		rangeStart := max(start, segmentStart)
		rangeEnd := min(end, segmentEnd)

		segmentRange := SegmentRange{
			Segment:    segment,
			ByteStart:  rangeStart - segmentStart, // Offset within segment
			ByteEnd:    rangeEnd - segmentStart,   // End offset within segment
			TotalStart: rangeStart,                // Absolute position
			TotalEnd:   rangeEnd,                  // Absolute position
		}

		segmentRanges = append(segmentRanges, segmentRange)

		// Stop if we've covered the entire requested range
		if cumulativeSize >= end+1 {
			break
		}
	}

	return segmentRanges
}

func (nf *NZBFile) ConvertToSegmentRanges(segments []NZBSegment) []SegmentRange {
	var segmentRanges []SegmentRange
	var cumulativeSize int64

	for i, segment := range segments {
		// Use the file's segment size (uniform)
		segmentSize := nf.SegmentSize

		// Handle last segment which might be smaller
		if i == len(segments)-1 {
			segmentSize = segment.Bytes // Last segment uses actual size
		}

		cumulativeSize += segmentSize

		segmentRange := SegmentRange{
			Segment:    segment,
			ByteStart:  0,                            // Always starts at 0 within the segment
			ByteEnd:    segmentSize - 1,              // Ends at segment size - 1
			TotalStart: cumulativeSize - segmentSize, // Absolute start position
			TotalEnd:   cumulativeSize - 1,           // Absolute end position
		}

		segmentRanges = append(segmentRanges, segmentRange)
	}

	return segmentRanges
}

func (nf *NZBFile) GetCacheKey() string {
	return fmt.Sprintf("rar_%s_%d", nf.Name, nf.Size)
}

func (nzb *NZB) GetFiles() []NZBFile {
	files := make([]NZBFile, 0, len(nzb.Files))
	for _, file := range nzb.Files {
		if !file.IsDeleted {
			files = append(files, file)
		}
	}
	return files[:len(files):len(files)] // Return a slice to avoid aliasing
}

// ValidateNZB performs basic validation on NZB content
func ValidateNZB(content []byte) error {
	if len(content) == 0 {
		return fmt.Errorf("empty NZB content")
	}

	// Check for basic XML structure
	if !strings.Contains(string(content), "<nzb") {
		return fmt.Errorf("invalid NZB format: missing <nzb> tag")
	}

	if !strings.Contains(string(content), "<file") {
		return fmt.Errorf("invalid NZB format: no files found")
	}

	return nil
}
