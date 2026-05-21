package parser

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/Tensai75/nzbparser"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

// getRARVolumeOrder returns a sort key for RAR volume ordering.
// .rar or .part01.rar = 0 (first volume)
// .r00 = 1, .r01 = 2, etc.
// .part02.rar = 2, .part03.rar = 3, etc.
func getRARVolumeOrder(filename string) int {
	lower := strings.ToLower(filename)
	ext := filepath.Ext(lower)
	base := strings.TrimSuffix(lower, ext)

	// Old-style naming: .rar, .r00, .r01, ...
	if ext == ".rar" {
		// Check for .partXX.rar pattern (new style)
		partPattern := regexp.MustCompile(`\.part(\d+)$`)
		if matches := partPattern.FindStringSubmatch(base); len(matches) == 2 {
			num, _ := strconv.Atoi(matches[1])
			return num // .part01.rar = 1, .part02.rar = 2
		}
		// Plain .rar is the first volume
		return 0
	}

	// .rXX pattern (old style continuation)
	if len(ext) == 4 && ext[0:2] == ".r" {
		numStr := ext[2:]
		if num, err := strconv.Atoi(numStr); err == nil {
			return num + 1 // .r00 = 1, .r01 = 2, etc.
		}
	}

	// .001, .002 etc (sometimes used by RAR, often by 7z)
	if regexp.MustCompile(`^\.\d+$`).MatchString(ext) {
		numStr := ext[1:]
		if num, err := strconv.Atoi(numStr); err == nil {
			return num
		}
	}

	// Unknown pattern, put at end
	return 999999
}

func get7zVolumeOrder(filename string) int {
	lower := strings.ToLower(filename)
	ext := filepath.Ext(lower)

	// .001, .002 etc
	if regexp.MustCompile(`^\.\d+$`).MatchString(ext) {
		numStr := ext[1:]
		if num, err := strconv.Atoi(numStr); err == nil {
			return num
		}
	}

	if ext == ".7z" {
		return 0
	}

	return 999999
}

func getZIPVolumeOrder(filename string) int {
	lower := strings.ToLower(filename)
	ext := filepath.Ext(lower)

	if ext == ".zip" {
		return 0
	}

	// .z01, .z02 etc
	if len(ext) == 4 && ext[0:2] == ".z" {
		numStr := ext[2:]
		if num, err := strconv.Atoi(numStr); err == nil {
			return num
		}
	}

	// .001, .002 etc
	if regexp.MustCompile(`^\.\d+$`).MatchString(ext) {
		numStr := ext[1:]
		if num, err := strconv.Atoi(numStr); err == nil {
			return num
		}
	}

	return 999999
}

func wrapNZBFile(f *storage.NZBFile) ([]*storage.NZBFile, error) {
	if f == nil {
		return nil, fmt.Errorf("nzb file is nil")
	}
	return []*storage.NZBFile{f}, nil
}

// fileMetaKey returns a stable key for associating per-file metadata.
func fileMetaKey(file nzbparser.NzbFile) string {
	if file.Number > 0 {
		return fmt.Sprintf("n:%d", file.Number)
	}
	if file.Subject != "" {
		return "s:" + file.Subject
	}
	if len(file.Segments) > 0 {
		return "m:" + file.Segments[0].Id
	}
	return ""
}

func getGroupsList(groups map[string]struct{}) []string {
	result := make([]string, 0, len(groups))
	for g := range groups {
		result = append(result, g)
	}
	return result
}

func determineNZBName(filename string, meta map[string]string) string {
	// Prefer filename if it exists
	if filename != "" {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename))
	} else if name := meta["Name"]; name != "" {
		filename = name
	} else if title := meta["title"]; title != "" {
		filename = title
	}
	return utils.RemoveInvalidChars(filename)
}

func determineExtension(group *FileGroup) string {
	// Try to determine extension from filenames
	for _, file := range group.Files {
		ext := filepath.Ext(file.Filename)
		if ext != "" {
			return ext
		}
	}
	return ""
}

func resolveFilePartMeta(index int, file nzbparser.NzbFile, group *FileGroup) filePartMeta {
	meta := filePartMeta{}

	if group != nil {
		fallback := group.getMetadata()
		meta.segmentSize = fallback.segmentSize
		meta.fileSize = fallback.fileSize
		if index == len(group.Files)-1 && fallback.lastFileSize > 0 {
			meta.fileSize = fallback.lastFileSize
		}

		if group.fileMeta != nil {
			if specific, ok := group.fileMeta[fileMetaKey(file)]; ok {
				if specific.segmentSize > 0 {
					meta.segmentSize = specific.segmentSize
				}
				if specific.fileSize > 0 {
					meta.fileSize = specific.fileSize
				}
				meta.partNumber = specific.partNumber
				meta.partBegin = specific.partBegin
			}
		}
	}

	if meta.segmentSize <= 0 && len(file.Segments) > 0 {
		reportedBytes := int64(file.Segments[0].Bytes)
		if reportedBytes <= 0 {
			reportedBytes = 750000
		}
		meta.segmentSize = int64(float64(reportedBytes) * 0.97)
		if meta.segmentSize <= 0 {
			meta.segmentSize = reportedBytes
		}
	}

	if meta.fileSize <= 0 && meta.segmentSize > 0 {
		meta.fileSize = meta.segmentSize * int64(len(file.Segments))
	}

	return meta
}

func getNZBSegments(index int, file nzbparser.NzbFile, group *FileGroup) (int64, []storage.NZBSegment) {
	if len(file.Segments) == 0 {
		return 0, nil
	}

	sort.Slice(file.Segments, func(i, j int) bool {
		return file.Segments[i].Number < file.Segments[j].Number
	})

	// Find the max segment number to properly size the array
	maxSegNum := 0
	for _, seg := range file.Segments {
		if seg.Number > maxSegNum {
			maxSegNum = seg.Number
		}
	}

	// Handle case where segment numbers start at 0 or 1
	nzbSegments := make([]storage.NZBSegment, maxSegNum)

	currentOffset := int64(0)
	metadata := resolveFilePartMeta(index, file, group)
	fileSize := metadata.fileSize
	segmentSize := metadata.segmentSize

	for idx, segment := range file.Segments {
		segSize := segmentSize
		if idx == len(file.Segments)-1 {
			// Last segment may be smaller.
			fullSegsSize := segmentSize * int64(len(file.Segments)-1)
			isSizeMismatch := false
			expectedTotal := fullSegsSize + segmentSize
			diff := fileSize - expectedTotal
			if diff < 0 {
				diff = -diff
			}
			if diff > (segmentSize*3)/2 {
				isSizeMismatch = true
			}

			if isSizeMismatch {
				segSize = int64(float64(segment.Bytes) * 0.97)
				if segSize <= 0 {
					segSize = int64(segment.Bytes)
				}
			} else {
				segSize = fileSize - fullSegsSize
			}
		}
		seg := storage.NZBSegment{
			Number:      segment.Number,
			MessageID:   segment.Id,
			Bytes:       segSize,
			StartOffset: currentOffset,
			EndOffset:   currentOffset + segSize - 1,
			Group:       group.BaseName,
		}

		// Bounds check: segment.Number is 1-indexed, array is 0-indexed
		segIdx := segment.Number - 1
		if segIdx >= 0 && segIdx < len(nzbSegments) {
			nzbSegments[segIdx] = seg
		}
		currentOffset += segSize
	}
	return currentOffset, nzbSegments
}

func buildBaseSegments(group *FileGroup) ([]storage.NZBSegment, []storage.ArchiveVolumeInfo, int64) {
	if len(group.Files) == 0 {
		return nil, nil, 0
	}

	baseSegments := make([]storage.NZBSegment, 0)
	volumeInfos := make([]storage.ArchiveVolumeInfo, 0, len(group.Files))
	currentOffset := int64(0)

	for idx, nzbFile := range group.Files {
		totalSize, segments := getNZBSegments(idx, nzbFile, group)
		if totalSize == 0 || len(segments) == 0 {
			continue
		}
		start := len(baseSegments)
		baseSegments = append(baseSegments, segments...)
		volumeInfos = append(volumeInfos, storage.ArchiveVolumeInfo{
			Name:         nzbFile.Filename,
			Size:         totalSize,
			SegmentStart: start,
			SegmentEnd:   len(baseSegments),
		})
		currentOffset += totalSize
	}

	return baseSegments, volumeInfos, currentOffset
}

func buildArchiveVolumeDescriptors(group *FileGroup) []*types.Volume {
	var volumes []*types.Volume

	if len(group.Files) == 0 {
		return volumes
	}

	for idx, nzbFile := range group.Files {
		if len(nzbFile.Segments) == 0 {
			continue
		}

		totalSize, volumeSegments := getNZBSegments(idx, nzbFile, group)
		if totalSize == 0 || len(volumeSegments) == 0 {
			continue
		}

		volumeName := nzbFile.Filename
		if volumeName == "" {
			volumeName = fmt.Sprintf("%s.part%03d", group.BaseName, idx+1)
		}

		volumes = append(volumes, &types.Volume{
			Index:    idx,
			Name:     volumeName,
			Size:     totalSize,
			Segments: volumeSegments,
		})
	}

	return volumes
}

func buildExtractedArchiveFiles(
	group *FileGroup,
	password string,
	fileType storage.NZBFileType,
	baseSegments []storage.NZBSegment,
	volumeInfos []storage.ArchiveVolumeInfo,
	infos []*storage.ExtractedFileInfo,
) []*storage.NZBFile {
	if len(baseSegments) == 0 {
		return nil
	}
	files := make([]*storage.NZBFile, 0, len(infos))

	for _, info := range infos {
		if info == nil || info.FileSize <= 0 {
			continue
		}
		if info.InternalPath == "" {
			info.InternalPath = NormalizeArchivePath(info.FileName)
		}
		name := info.FileName
		if name == "" {
			name = group.BaseName
		}
		name = utils.RemoveInvalidChars(name)

		// Use pre-sliced segments if available, otherwise slice based on offset
		var segments []storage.NZBSegment
		if len(info.Segments) > 0 {
			// Use pre-computed segments (from RAR parser, etc.)
			segments = info.Segments
		} else if info.DataOffset > 0 || info.FileSize > 0 {
			// Slice segments for this file's byte range
			sliced, err := sliceSegmentsForRangeSimple(baseSegments, info.DataOffset, info.FileSize)
			if err != nil || len(sliced) == 0 {
				// Fallback to all segments if slicing fails
				segments = make([]storage.NZBSegment, len(baseSegments))
				copy(segments, baseSegments)
			} else {
				segments = sliced
			}
		} else {
			// No offset info, use all segments
			segments = make([]storage.NZBSegment, len(baseSegments))
			copy(segments, baseSegments)
		}

		files = append(files, &storage.NZBFile{
			Name:         name,
			InternalPath: info.InternalPath,
			Groups:       getGroupsList(group.Groups),
			Segments:     segments,
			Password:     password,
			FileType:     fileType,
			Number:       group.Files[0].Number,
			Size:         info.FileSize,
			IsStored:     info.IsStored,
		})
	}

	return files
}

func NormalizeArchivePath(name string) string {
	trimmed := strings.TrimSpace(name)
	trimmed = strings.TrimLeft(trimmed, "./")
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "\\", "/")
	trimmed = path.Clean(trimmed)
	trimmed = strings.Trim(trimmed, "/")
	return trimmed
}
