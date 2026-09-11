package parser

import (
	"context"
	"fmt"
	"io"
	"sort"

	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

type articleSpan struct {
	start   int64
	end     int64
	segment storage.NZBSegment
}

// articleReaderAt is a zero-prefetch random-access view over archive volumes.
// Every body read goes through ArticleSource, so archive libraries share the
// same deduplication, cache, and concurrency budget as classification.
type articleReaderAt struct {
	ctx    context.Context
	source ArticleSource
	spans  []articleSpan
	size   int64
}

func newArticleReaderAt(ctx context.Context, source ArticleSource, volumes []*types.Volume) (*articleReaderAt, int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if source == nil {
		return nil, 0, fmt.Errorf("article source is nil")
	}
	if len(volumes) == 0 {
		return nil, 0, fmt.Errorf("no archive volumes available")
	}

	ordered := append([]*types.Volume(nil), volumes...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Index < ordered[j].Index
	})

	spanCount := 0
	for _, volume := range ordered {
		if volume != nil {
			spanCount += len(volume.Segments)
		}
	}
	spans := make([]articleSpan, 0, spanCount)
	var position int64
	for _, volume := range ordered {
		if volume == nil {
			return nil, 0, fmt.Errorf("archive volume is nil")
		}
		volumeStart := position
		for _, segment := range volume.Segments {
			if segment.MessageID == "" {
				return nil, 0, fmt.Errorf("archive volume %q has a segment without a message ID", volume.Name)
			}
			if segment.Bytes <= 0 {
				return nil, 0, fmt.Errorf("archive volume %q has non-positive segment size %d", volume.Name, segment.Bytes)
			}
			end := position + segment.Bytes
			if end < position {
				return nil, 0, fmt.Errorf("archive volume %q size overflows int64", volume.Name)
			}
			spans = append(spans, articleSpan{start: position, end: end, segment: segment})
			position = end
		}
		if got := position - volumeStart; got != volume.Size {
			return nil, 0, fmt.Errorf("archive volume %q segment size %d does not match declared size %d", volume.Name, got, volume.Size)
		}
	}

	return &articleReaderAt{ctx: ctx, source: source, spans: spans, size: position}, position, nil
}

func (r *articleReaderAt) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, fmt.Errorf("negative archive read offset %d", offset)
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if offset >= r.size {
		return 0, io.EOF
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}

	spanIndex := sort.Search(len(r.spans), func(index int) bool {
		return r.spans[index].end > offset
	})
	read := 0
	position := offset
	for read < len(buffer) && spanIndex < len(r.spans) {
		if err := r.ctx.Err(); err != nil {
			return read, err
		}
		span := r.spans[spanIndex]
		if position < span.start {
			return read, fmt.Errorf("archive source has a gap at offset %d", position)
		}
		data, err := fetchSegmentData(r.ctx, r.source, span.segment)
		if err != nil {
			return read, err
		}
		within := position - span.start
		if within < 0 || within >= int64(len(data)) {
			return read, fmt.Errorf("archive segment %s offset %d exceeds %d decoded bytes", span.segment.MessageID, within, len(data))
		}
		available := min(int64(len(data))-within, span.end-position)
		count := min(int64(len(buffer)-read), available)
		copy(buffer[read:read+int(count)], data[within:within+count])
		read += int(count)
		position += count
		if position == span.end {
			spanIndex++
		}
	}
	if read != len(buffer) {
		return read, io.EOF
	}
	return read, nil
}
