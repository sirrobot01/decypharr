package parser

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/sirrobot01/decypharr/pkg/storage"
)

// ErrPrefixReadUnsupported asks the caller to use the full serving reader for
// transformations the analyzer deliberately does not duplicate.
var ErrPrefixReadUnsupported = errors.New("analyzer prefix read is unsupported")

// ReadFilePrefix reads a logical file head through the analyzer's shared body
// broker. Direct media and stored archive entries need no second NNTP fetch;
// encrypted or compressed entries remain on the serving reader's transform
// path.
func (p *NZBParser) ReadFilePrefix(ctx context.Context, file *storage.NZBFile, maxBytes int) ([]byte, error) {
	if p == nil || p.source == nil {
		return nil, fmt.Errorf("article source is nil")
	}
	if file == nil {
		return nil, fmt.Errorf("NZB file is nil")
	}
	if maxBytes <= 0 {
		return nil, nil
	}
	if file.IsEncrypted || (file.FileType != storage.NZBFileTypeMedia && !file.IsStored) {
		return nil, fmt.Errorf("%w for %q", ErrPrefixReadUnsupported, file.Name)
	}
	if len(file.Segments) == 0 {
		return nil, fmt.Errorf("file %q has no segments", file.Name)
	}

	wanted := int64(maxBytes)
	if file.Size > 0 {
		wanted = min(wanted, file.Size)
	}
	// Stored segments are already in ascending StartOffset order, and a head
	// read only touches the first few. Cloning and sorting first cost a full
	// copy of the segment list (72 bytes each, tens of thousands for a remux)
	// plus a sort, per 16KB head - and the repair sweep does this for every
	// media file. Only pay it when the input really is out of order.
	byOffset := func(left, right storage.NZBSegment) int {
		if order := cmp.Compare(left.StartOffset, right.StartOffset); order != 0 {
			return order
		}
		return cmp.Compare(left.Number, right.Number)
	}
	segments := file.Segments
	if !slices.IsSortedFunc(segments, byOffset) {
		segments = slices.Clone(segments)
		slices.SortFunc(segments, byOffset)
	}

	result := make([]byte, wanted)
	var position int64
	for _, segment := range segments {
		if position == wanted {
			break
		}
		if segment.Bytes <= 0 {
			return nil, fmt.Errorf("file %q has non-positive segment size %d", file.Name, segment.Bytes)
		}
		segmentEnd := segment.StartOffset + segment.Bytes
		if segmentEnd < segment.StartOffset {
			return nil, fmt.Errorf("file %q segment range overflows int64", file.Name)
		}
		if segmentEnd <= position {
			continue
		}
		if segment.StartOffset > position {
			return nil, fmt.Errorf("file %q has a source gap at offset %d", file.Name, position)
		}

		data, err := fetchSegmentData(ctx, p.source, segment)
		if err != nil {
			return nil, fmt.Errorf("read head segment %s for %q: %w", segment.MessageID, file.Name, err)
		}
		within := position - segment.StartOffset
		count := min(wanted-position, int64(len(data))-within)
		if count <= 0 {
			return nil, fmt.Errorf("file %q segment %s does not cover offset %d", file.Name, segment.MessageID, position)
		}
		copy(result[position:position+count], data[within:within+count])
		position += count
	}
	if position != wanted {
		return nil, fmt.Errorf("file %q head is only partially covered (%d of %d bytes)", file.Name, position, wanted)
	}
	return result, nil
}
