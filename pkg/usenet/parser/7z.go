package parser

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/javi11/sevenzip"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/fs"
)

// SevenZParser parses 7z archives from NNTP segments
type SevenZParser struct {
	manager       *nntp.Client
	maxConcurrent int
	logger        zerolog.Logger
	rarParser     *RARParser
}

// NewSevenZParser creates a new 7z parser
func NewSevenZParser(manager *nntp.Client, maxConcurrent int, logger zerolog.Logger) *SevenZParser {
	return &SevenZParser{
		manager:       manager,
		maxConcurrent: maxConcurrent,
		logger:        logger.With().Str("component", "7z_parser").Logger(),
		rarParser:     NewRARParser(manager, maxConcurrent, logger.With().Str("component", "rar_parser_embedded").Logger()),
	}
}

func (p *SevenZParser) Process(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	sort.Slice(group.Files, func(i, j int) bool {
		oi := get7zVolumeOrder(group.Files[i].Filename)
		oj := get7zVolumeOrder(group.Files[j].Filename)
		if oi != oj {
			return oi < oj
		}
		return group.Files[i].Number < group.Files[j].Number
	})

	volumes := buildArchiveVolumeDescriptors(group)
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes built from group")
	}

	baseSegments, volumeInfos, _ := buildBaseSegments(group)
	if len(baseSegments) == 0 {
		return nil, fmt.Errorf("no base segments built from group")
	}

	usenetFS, err := fs.NewFS(ctx, p.manager, p.maxConcurrent, 0, volumes, p.logger) // 0 prefetch for parsing
	if err != nil {
		return nil, fmt.Errorf("failed to create usenet FS: %w", err)
	}

	readerAt, size, cleanup, err := usenetFS.CreateReaderAt()
	if err != nil {
		return nil, err
	}
	defer cleanup()

	reader, err := sevenzip.NewReaderWithPassword(readerAt, size, password)
	if err != nil {
		return nil, fmt.Errorf("failed to open sevenzip reader: %w", err)
	}

	fileList, err := reader.ListFilesWithOffsets()
	if err != nil {
		return nil, fmt.Errorf("failed to list files with offsets: %w", err)
	}

	// Separate RAR files from non-RAR files
	var rarFiles []sevenzip.FileInfo
	var nonRARFiles []sevenzip.FileInfo

	for _, file := range fileList {
		if isRARFile(file.Name) {
			rarFiles = append(rarFiles, file)
		} else {
			nonRARFiles = append(nonRARFiles, file)
		}
	}

	var files []*storage.NZBFile

	// Parse RAR files by reading their headers directly from readerAt
	if len(rarFiles) > 0 {
		rarNZBFiles, err := p.processRARFilesFromPositions(ctx, rarFiles, group, readerAt, baseSegments, volumeInfos, password)
		if err != nil {
			p.logger.Warn().
				Err(err).
				Msg("Failed to process RAR files from 7z, continuing with non-RAR files")
		} else {
			files = append(files, rarNZBFiles...)
		}
	}

	// Parse non-RAR files as regular files
	for _, file := range nonRARFiles {
		internal := NormalizeArchivePath(file.Name)
		if internal == "" {
			continue
		}

		name := utils.RemoveInvalidChars(filepath.Base(internal))
		if name == "" {
			name = path.Base(internal)
		}

		// Slice segments for this file's byte range using offset from sevenzip
		var segments []storage.NZBSegment
		if file.Offset >= 0 && file.Size > 0 {
			sliced, err := sliceSegmentsForRangeSimple(baseSegments, file.Offset, int64(file.Size))
			if err != nil || len(sliced) == 0 {
				// Fallback to all segments
				segments = make([]storage.NZBSegment, len(baseSegments))
				copy(segments, baseSegments)
			} else {
				segments = sliced
			}
		} else {
			segments = make([]storage.NZBSegment, len(baseSegments))
			copy(segments, baseSegments)
		}

		files = append(files, &storage.NZBFile{
			Name:         name,
			InternalPath: internal,
			Size:         int64(file.Size),
			IsStored:     !file.Compressed,
			Groups:       getGroupsList(group.Groups),
			Segments:     segments,
			Password:     password,
			Number:       group.Files[0].Number,
			FileType:     storage.NZBFileTypeSevenZip,
		})
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in 7z archive")
	}

	p.logger.Info().
		Int("total_extracted", len(files)).
		Msg("7z archive processing complete")

	return files, nil
}

// processRARFilesFromPositions creates volume descriptors for RAR files based on their positions
// within the 7z archive and passes them to the RAR parser
func (p *SevenZParser) processRARFilesFromPositions(
	ctx context.Context,
	rarFiles []sevenzip.FileInfo,
	group *FileGroup,
	readerAt io.ReaderAt,
	baseSegments []storage.NZBSegment,
	volumeInfos []storage.ArchiveVolumeInfo,
	password string,
) ([]*storage.NZBFile, error) {
	if len(rarFiles) == 0 {
		return nil, nil
	}

	// Sort RAR files by their offset within the 7z archive
	// This is the PHYSICAL order of the data, which for 7z-embedded RAR is typically:
	// .r00, .r01, ..., .r51, .rar (opposite of RAR's logical naming!)
	sort.Slice(rarFiles, func(i, j int) bool {
		return rarFiles[i].Offset < rarFiles[j].Offset
	})
	// Detect RAR version from first volume (by logical order)
	firstRAR := rarFiles[0]
	versionBuf := make([]byte, 8)
	if _, err := readerAt.ReadAt(versionBuf, firstRAR.Offset); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("failed to read RAR signature: %w", err)
	}
	version := detectRARVersion(versionBuf)
	if version == RARVersionUnknown {
		return nil, fmt.Errorf("unknown RAR format in 7z")
	}

	// Parse headers from volumes to find file info
	// Strategy: Files may have their primary header in either the logical first volume
	// (.rar by naming convention) OR the physical first volume (lowest offset)
	// We'll check both approaches and aggregate
	var allRawFiles []*RARFileEntry

	// Find logical first volume (.rar)
	logicalFirst := -1
	for i, rf := range rarFiles {
		if strings.HasSuffix(strings.ToLower(rf.Name), ".rar") {
			logicalFirst = i
			break
		}
	}

	// Scan order: logical first (.rar), then physical first (.r00), then next few
	volumesToScan := make([]int, 0, 6)
	if logicalFirst >= 0 {
		volumesToScan = append(volumesToScan, logicalFirst)
	}
	// Add first 3 by physical order if not already added
	for i := 0; i < min(3, len(rarFiles)); i++ {
		if i != logicalFirst {
			volumesToScan = append(volumesToScan, i)
		}
	}

	for _, volIndex := range volumesToScan {
		rarFile := rarFiles[volIndex]

		// Optimization: RAR headers are small - 64KB is usually enough
		headerSize := int64(64 * 1024)
		if headerSize > int64(rarFile.Size) {
			headerSize = int64(rarFile.Size)
		}

		headerData := make([]byte, headerSize)
		n, err := readerAt.ReadAt(headerData, rarFile.Offset)
		if err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		headerData = headerData[:n]

		// Parse headers from this volume
		var volumeFiles []*RARFileEntry
		switch version {
		case RARVersion5:
			volumeFiles, _ = p.rarParser.parseRAR5Headers(headerData, volIndex, filepath.Base(rarFile.Name), password)
		case RARVersion4:
			volumeFiles, _ = p.rarParser.parseRAR4Headers(headerData, volIndex, filepath.Base(rarFile.Name))
		}

		allRawFiles = append(allRawFiles, volumeFiles...)

		// Optimization: If we found files with names, we can stop scanning
		// (first volume should have all file headers)
		hasNamedFiles := false
		for _, f := range allRawFiles {
			if f.Name != "" {
				hasNamedFiles = true
				break
			}
		}
		if hasNamedFiles {
			break
		}
	}

	if len(allRawFiles) == 0 {
		return nil, fmt.Errorf("no files found in RAR volumes")
	}

	// Aggregate file parts across volumes (files spanning multiple volumes will have multiple entries)
	rarFileEntries := p.rarParser.aggregateFileParts(allRawFiles)

	// Build a map of RAR filename -> offset in 7z
	rarFileOffsets := make(map[string]int64)
	for _, rarFile := range rarFiles {
		rarFileOffsets[filepath.Base(rarFile.Name)] = rarFile.Offset
	}

	// Build NZBFile list
	var files []*storage.NZBFile
	for _, rarEntry := range rarFileEntries {
		if rarEntry.IsDirectory {
			continue
		}

		// Only support stored RAR files for streaming
		if !rarEntry.IsStored {
			continue
		}

		filename := utils.RemoveInvalidChars(filepath.Base(rarEntry.Name))
		if filename == "" {
			filename = path.Base(rarEntry.Name)
		}

		// get segments for this file by processing all its volume parts
		fileSegments, err := p.buildSegmentsForRARFile(rarEntry, rarFileOffsets, baseSegments, volumeInfos)
		if err != nil {
			p.logger.Warn().
				Err(err).
				Str("file", rarEntry.Name).
				Msg("Failed to build segments for RAR file")
			continue
		}

		if len(fileSegments) == 0 {
			p.logger.Warn().
				Str("file", rarEntry.Name).
				Msg("No segments found for RAR file")
			continue
		}

		p.logger.Debug().
			Str("file", rarEntry.Name).
			Int("segment_count", len(fileSegments)).
			Int64("file_size", rarEntry.UncompressedSize).
			Msg("Built segments for RAR file in 7z")

		files = append(files, &storage.NZBFile{
			Name:         filename,
			InternalPath: rarEntry.Name,
			Size:         rarEntry.UncompressedSize,
			IsStored:     true,
			Segments:     fileSegments,
			Groups:       getGroupsList(group.Groups),
			Password:     password,
			Number:       group.Files[0].Number,
			FileType:     storage.NZBFileTypeRar,
		})
	}

	return files, nil
}

// buildSegmentsForRARFile builds the segment list for a file across all RAR volume parts
func (p *SevenZParser) buildSegmentsForRARFile(
	rarEntry *RARFileEntry,
	rarFileOffsets map[string]int64,
	baseSegments []storage.NZBSegment,
	volumeInfos []storage.ArchiveVolumeInfo,
) ([]storage.NZBSegment, error) {
	if len(rarEntry.VolumeParts) == 0 {
		return nil, fmt.Errorf("no volume parts for file %s", rarEntry.Name)
	}

	var fileSegments []storage.NZBSegment

	// Parse each volume part of this file
	for partIdx, part := range rarEntry.VolumeParts {
		if part.PackedSize <= 0 {
			continue
		}

		// get the RAR volume file's offset within the 7z
		rarVolumeName := filepath.Base(part.Name)
		rarVolumeOffset, ok := rarFileOffsets[rarVolumeName]
		if !ok {
			p.logger.Warn().
				Str("part_name", part.Name).
				Str("file", rarEntry.Name).
				Msg("RAR part not found in 7z file list")
			continue
		}

		// The file data starts at: rarVolumeOffset (in 7z) + part.DataOffset (in RAR volume)
		absoluteDataOffset := rarVolumeOffset + part.DataOffset

		// Slice segments from the base 7z segments for this part's data range
		partSegments, err := sliceSegmentsForRange(
			baseSegments,
			volumeInfos,
			absoluteDataOffset,
			part.PackedSize,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to slice segments for part %d of %s: %w", partIdx, rarEntry.Name, err)
		}

		if len(partSegments) == 0 {
			p.logger.Warn().
				Str("file", rarEntry.Name).
				Int("part_index", partIdx).
				Int64("offset", absoluteDataOffset).
				Int64("size", part.PackedSize).
				Msg("No segments found for RAR part")
			continue
		}

		// Append this part's segments to the file's segment list
		fileSegments = append(fileSegments, partSegments...)
	}

	return fileSegments, nil
}

// sliceSegmentsForRange extracts segments covering [offset, offset+length) within the 7z archive
func sliceSegmentsForRange(
	baseSegments []storage.NZBSegment,
	volumeInfos []storage.ArchiveVolumeInfo,
	offset int64,
	length int64,
) ([]storage.NZBSegment, error) {
	if length <= 0 {
		return nil, nil
	}
	if offset < 0 {
		return nil, fmt.Errorf("negative offset: %d", offset)
	}

	targetStart := offset
	targetEnd := offset + length - 1

	// Build cumulative offset map for volumes
	var absPos int64
	volumeOffsets := make([]struct {
		startOffset int64
		endOffset   int64
		info        storage.ArchiveVolumeInfo
	}, len(volumeInfos))

	for i, info := range volumeInfos {
		volumeOffsets[i].startOffset = absPos
		volumeOffsets[i].endOffset = absPos + info.Size
		volumeOffsets[i].info = info
		absPos += info.Size
	}

	var result []storage.NZBSegment

	// Parse each volume
	for _, volOffset := range volumeOffsets {
		// Check if this volume overlaps with target range
		if volOffset.endOffset <= targetStart || volOffset.startOffset > targetEnd {
			continue
		}

		// get segments for this volume
		segStart := volOffset.info.SegmentStart
		segEnd := volOffset.info.SegmentEnd

		if segStart < 0 || segEnd > len(baseSegments) || segStart >= segEnd {
			continue
		}

		// Parse each segment in this volume
		segAbsPos := volOffset.startOffset
		for idx := segStart; idx < segEnd; idx++ {
			seg := baseSegments[idx]
			segSize := seg.Bytes

			segAbsStart := segAbsPos
			segAbsEnd := segAbsPos + segSize - 1

			// Check if segment overlaps with target range
			if segAbsEnd < targetStart {
				segAbsPos += segSize
				continue
			}
			if segAbsStart > targetEnd {
				break
			}

			// Calculate overlap
			overlapStart := segAbsStart
			if overlapStart < targetStart {
				overlapStart = targetStart
			}

			overlapEnd := segAbsEnd
			if overlapEnd > targetEnd {
				overlapEnd = targetEnd
			}

			// Calculate segment-relative offsets
			relStart := overlapStart - segAbsStart
			relEnd := overlapEnd - segAbsStart

			// Create sliced segment
			slicedSeg := storage.NZBSegment{
				Number:      seg.Number,
				MessageID:   seg.MessageID,
				Bytes:       relEnd - relStart + 1,
				StartOffset: relStart,
				EndOffset:   relEnd,
				Group:       seg.Group,
			}

			result = append(result, slicedSeg)

			if overlapEnd == targetEnd {
				// We've covered the entire range
				return result, nil
			}

			segAbsPos += segSize
		}
	}

	return result, nil
}

// isRARFile checks if a filename is a RAR file
func isRARFile(filename string) bool {
	lower := strings.ToLower(filename)
	return strings.HasSuffix(lower, ".rar") ||
		strings.HasSuffix(lower, ".r00") ||
		strings.HasSuffix(lower, ".r01") ||
		strings.HasSuffix(lower, ".r02") ||
		strings.HasSuffix(lower, ".r03") ||
		strings.HasSuffix(lower, ".r04") ||
		strings.HasSuffix(lower, ".r05") ||
		strings.HasSuffix(lower, ".r06") ||
		strings.HasSuffix(lower, ".r07") ||
		strings.HasSuffix(lower, ".r08") ||
		strings.HasSuffix(lower, ".r09") ||
		(len(lower) > 3 && lower[len(lower)-4] == '.' && lower[len(lower)-3] == 'r' &&
			lower[len(lower)-2] >= '0' && lower[len(lower)-2] <= '9' &&
			lower[len(lower)-1] >= '0' && lower[len(lower)-1] <= '9')
}
