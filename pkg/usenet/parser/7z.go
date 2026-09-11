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
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

// SevenZParser parses 7z archives from NNTP segments
type SevenZParser struct {
	source    ArticleSource
	logger    zerolog.Logger
	rarParser *RARParser
}

// NewSevenZParser creates a new 7z parser
func NewSevenZParser(source ArticleSource, maxConcurrent int, logger zerolog.Logger) *SevenZParser {
	return &SevenZParser{
		source:    source,
		logger:    logger.With().Str("component", "7z_parser").Logger(),
		rarParser: NewRARParser(source, maxConcurrent, logger.With().Str("component", "rar_parser_embedded").Logger()),
	}
}

func (p *SevenZParser) Process(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	sort.Slice(group.Files, func(i, j int) bool {
		return group.Files[i].Filename < group.Files[j].Filename
	})

	volumes, err := buildArchiveVolumeDescriptors(group)
	if err != nil {
		return nil, err
	}
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes built from group")
	}

	baseSegments, volumeInfos, _, err := buildBaseSegments(group)
	if err != nil {
		return nil, err
	}
	if len(baseSegments) == 0 {
		return nil, fmt.Errorf("no base segments built from group")
	}
	segmentIndex, err := newSegmentLayout(baseSegments)
	if err != nil {
		return nil, fmt.Errorf("index 7z source segments: %w", err)
	}
	if err := segmentIndex.validateVolumes(volumeInfos); err != nil {
		return nil, fmt.Errorf("validate 7z volume layout: %w", err)
	}

	readerAt, size, err := newArticleReaderAt(ctx, p.source, volumes)
	if err != nil {
		return nil, fmt.Errorf("failed to create archive reader: %w", err)
	}

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
		rarNZBFiles, err := p.processRARFilesFromPositions(ctx, rarFiles, group, readerAt, segmentIndex, password)
		if err != nil {
			return nil, fmt.Errorf("process RAR files embedded in 7z: %w", err)
		}
		files = append(files, rarNZBFiles...)
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
			sliced, err := segmentIndex.slice(file.Offset, int64(file.Size), true)
			if err != nil || len(sliced) == 0 {
				if err == nil {
					err = fmt.Errorf("no source segments overlap the file range")
				}
				return nil, fmt.Errorf("map 7z file %q to raw source: %w", internal, err)
			} else {
				segments = sliced
			}
		} else {
			return nil, fmt.Errorf("7z file %q has no usable source offset", internal)
		}

		files = append(files, &storage.NZBFile{
			Name:         name,
			InternalPath: internal,
			Size:         int64(file.Size),
			IsStored:     !file.Compressed,
			Groups:       getGroupsList(group.Groups),
			Segments:     segments,
			Password:     password,
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
	segmentIndex *segmentLayout,
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
		headerSize := min(int64(64*1024), int64(rarFile.Size))

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
		fileSegments, err := p.buildSegmentsForRARFile(rarEntry, rarFileOffsets, segmentIndex)
		if err != nil {
			return nil, fmt.Errorf("map RAR file %q embedded in 7z: %w", rarEntry.Name, err)
		}

		if len(fileSegments) == 0 {
			return nil, fmt.Errorf("RAR file %q embedded in 7z has no source segments", rarEntry.Name)
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
			FileType:     storage.NZBFileTypeRar,
		})
	}

	return files, nil
}

// buildSegmentsForRARFile builds the segment list for a file across all RAR volume parts
func (p *SevenZParser) buildSegmentsForRARFile(
	rarEntry *RARFileEntry,
	rarFileOffsets map[string]int64,
	segmentIndex *segmentLayout,
) ([]storage.NZBSegment, error) {
	if len(rarEntry.VolumeParts) == 0 {
		return nil, fmt.Errorf("no volume parts for file %s", rarEntry.Name)
	}

	var fileSegments []storage.NZBSegment
	var currentFileOffset int64 // Offset within the final extracted file

	// Parse each volume part of this file
	for partIdx, part := range rarEntry.VolumeParts {
		if part.PackedSize <= 0 {
			continue
		}

		// get the RAR volume file's offset within the 7z
		rarVolumeName := filepath.Base(part.Name)
		rarVolumeOffset, ok := rarFileOffsets[rarVolumeName]
		if !ok {
			return nil, fmt.Errorf("RAR part %q for %s is absent from the 7z file list", part.Name, rarEntry.Name)
		}

		// The file data starts at: rarVolumeOffset (in 7z) + part.DataOffset (in RAR volume)
		absoluteDataOffset := rarVolumeOffset + part.DataOffset

		// Slice segments from the base 7z segments for this part's data range
		partSegments, err := segmentIndex.slice(absoluteDataOffset, part.PackedSize, false)
		if err != nil {
			return nil, fmt.Errorf("failed to slice segments for part %d of %s: %w", partIdx, rarEntry.Name, err)
		}

		if len(partSegments) == 0 {
			return nil, fmt.Errorf("RAR part %d of %s has no source segments", partIdx, rarEntry.Name)
		}

		// Assign output-file positions cumulatively across parts (same
		// pattern as RARParser.buildSegmentsForFile), then append.
		for i := range partSegments {
			partSegments[i].StartOffset = currentFileOffset
			partSegments[i].EndOffset = currentFileOffset + partSegments[i].Bytes - 1
			currentFileOffset += partSegments[i].Bytes
		}
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
	layout, err := newSegmentLayout(baseSegments)
	if err != nil {
		return nil, err
	}
	if err := layout.validateVolumes(volumeInfos); err != nil {
		return nil, err
	}
	return layout.slice(offset, length, false)
}

// isRARFile checks if a filename is a RAR file
func isRARFile(filename string) bool {
	lower := strings.ToLower(filename)
	return rarMainPattern.MatchString(lower) ||
		rarPartPattern.MatchString(lower) ||
		rarVolumePattern.MatchString(lower)
}
