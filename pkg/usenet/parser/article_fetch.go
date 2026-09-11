package parser

import (
	"context"
	"fmt"

	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
)

const maxCorruptHeaderProbes = 4

// headerProbeOrder checks likely boundary/header parts first, then every
// remaining article. A not-found BODY response carries no article payload, so
// walking past missing parts costs round trips rather than release bandwidth.
func headerProbeOrder(count int) []int {
	if count <= 0 {
		return nil
	}
	priority := []int{0, count / 2, count - 1, 1}
	result := make([]int, 0, count)
	seen := make(map[int]struct{}, count)
	add := func(index int) {
		if index < 0 || index >= count {
			return
		}
		if _, ok := seen[index]; ok {
			return
		}
		seen[index] = struct{}{}
		result = append(result, index)
	}
	for _, index := range priority {
		add(index)
	}
	for index := range count {
		add(index)
	}
	return result
}

// fetchFileHeaderPrefix finds one usable article within a posted file. Definitive
// missing responses do not consume article bodies and therefore do not count
// against the corruption-probe ceiling. A decoded-but-corrupt response does
// consume bandwidth, so at most maxCorruptHeaderProbes are attempted.
func fetchFileHeaderPrefix(ctx context.Context, source ArticleSource, file manifest.File, maxSnippet int) (*nntp.YencMetadata, error) {
	if len(file.Segments) == 0 {
		return nil, fmt.Errorf("file has no segments")
	}
	if source == nil {
		return nil, fmt.Errorf("article source is nil")
	}

	var lastErr error
	corruptProbes := 0
	for _, index := range headerProbeOrder(len(file.Segments)) {
		segment := file.Segments[index]
		if segment.MessageID == "" {
			lastErr = fmt.Errorf("segment %d has no message ID", segment.Number)
			continue
		}

		metadata, err := source.Header(ctx, segment.MessageID, maxSnippet)
		if err == nil {
			if metadata == nil {
				return nil, fmt.Errorf("segment header contained no yEnc metadata")
			}
			return metadata, nil
		}

		lastErr = err
		if nntp.IsArticleNotFoundError(err) {
			continue
		}
		if !nntp.IsYencDecodeError(err) {
			return nil, err
		}
		corruptProbes++
		if corruptProbes >= maxCorruptHeaderProbes {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("file has no fetchable segment")
	}
	return nil, lastErr
}

// fetchFileHeader obtains yEnc topology while allowing any healthy part to
// stand in for a missing first article.
func fetchFileHeader(ctx context.Context, source ArticleSource, file manifest.File) (*nntp.YencMetadata, error) {
	return fetchFileHeaderPrefix(ctx, source, file, metadataOnly)
}

// fetchSegmentData returns exactly the decoded bytes described by segment.
func fetchSegmentData(ctx context.Context, source ArticleSource, segment storage.NZBSegment) ([]byte, error) {
	body, err := source.Body(ctx, segment.MessageID)
	if err != nil {
		return nil, err
	}

	start := segment.SegmentDataStart
	if start < 0 || start > int64(len(body)) {
		return nil, fmt.Errorf("decoded segment starts at %d with only %d bytes", start, len(body))
	}
	end := int64(len(body))
	if segment.Bytes > 0 && start+segment.Bytes < end {
		end = start + segment.Bytes
	}
	if end <= start {
		return nil, fmt.Errorf("decoded segment has no usable data")
	}
	return body[start:end:end], nil
}
