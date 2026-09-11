package parser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

// ZIP format constants
const (
	ZIPLocalFileHeaderSig             = 0x04034b50
	ZIPCentralDirectoryHeaderSig      = 0x02014b50
	ZIPEndOfCentralDirSig             = 0x06054b50
	ZIPZIP64EndOfCentralDirSig        = 0x06064b50
	ZIPZIP64EndOfCentralDirLocatorSig = 0x07064b50

	ZIPStoreMethod   = 0  // No compression
	ZIPDeflateMethod = 8  // DEFLATE compression
	ZIPBzip2Method   = 12 // BZIP2 compression
	ZIPLzmaMethod    = 14 // LZMA compression

	// Default snippet sizes
	defaultZIPEndSnippetSize   = 256 * 1024 // 256KB from end for central directory
	defaultZIPStartSnippetSize = 64 * 1024  // 64KB from start (optional)
)

// ZIPFileEntry represents a file in a ZIP archive
type ZIPFileEntry struct {
	Name              string
	UncompressedSize  int64
	CompressedSize    int64
	Method            uint16
	IsStored          bool
	IsDirectory       bool
	LocalHeaderOffset int64
	DiskNumberStart   int64
	CRC32             uint32
}

// ZIPArchiveInfo contains ZIP archive metadata
type ZIPArchiveInfo struct {
	Files       []*ZIPFileEntry
	TotalFiles  int
	TotalSize   int64
	IsMultiPart bool
}

// ZIPParser parses ZIP archives from NNTP segments
type ZIPParser struct {
	source ArticleSource
	logger zerolog.Logger
}

// NewZIPParser creates a new ZIP parser
func NewZIPParser(source ArticleSource, _ int, logger zerolog.Logger) *ZIPParser {
	return &ZIPParser{
		source: source,
		logger: logger.With().Str("component", "zip_parser").Logger(),
	}
}

func (p *ZIPParser) Process(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	sort.Slice(group.Files, func(i, j int) bool {
		oi := getZIPVolumeOrder(group.Files[i].Filename)
		oj := getZIPVolumeOrder(group.Files[j].Filename)
		if oi != oj {
			return oi < oj
		}
		return group.Files[i].Filename < group.Files[j].Filename
	})

	volumes, err := buildArchiveVolumeDescriptors(group)
	if err != nil {
		return nil, err
	}
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes built from group")
	}
	readerAt, archiveSize, err := newArticleReaderAt(ctx, p.source, volumes)
	if err != nil {
		return nil, fmt.Errorf("failed to create ZIP reader: %w", err)
	}
	volumeStarts := make([]int64, len(volumes))
	var volumePosition int64
	for index, volume := range volumes {
		volumeStarts[index] = volumePosition
		volumePosition += volume.Size
	}

	baseSegments, volumeInfos, _, err := buildBaseSegments(group)
	if err != nil {
		return nil, err
	}
	if len(baseSegments) == 0 {
		return nil, fmt.Errorf("no base segments built from group")
	}

	archiveInfo, err := p.parseArchiveReader(readerAt, archiveSize, len(volumes) > 1)
	if err != nil {
		return nil, fmt.Errorf("failed to parse ZIP archive: %w", err)
	}

	var extracted []*storage.ExtractedFileInfo
	for _, file := range archiveInfo.Files {
		if file.IsDirectory {
			continue
		}

		// Only process stored (uncompressed) files for streaming
		if !file.IsStored {
			continue
		}

		internal := NormalizeArchivePath(file.Name)
		if internal == "" {
			continue
		}

		name := utils.RemoveInvalidChars(filepath.Base(internal))
		if name == "" {
			name = path.Base(internal)
		}

		if file.UncompressedSize == 0 {
			continue
		}

		// LocalHeaderOffset points at the local file header, NOT the payload.
		// The payload starts after a 30-byte fixed header + filename + the
		// LOCAL extra field, whose length frequently differs from the central
		// directory's. Read the local header to get the exact data offset;
		// without this the stream is shifted by the header length (garbage
		// prefix + truncated tail) and the file won't play.
		headerOffset, err := absoluteZIPHeaderOffset(file, volumeStarts)
		if err != nil {
			return nil, fmt.Errorf("resolve local header for %q: %w", internal, err)
		}
		dataOffset, err := p.calculateZIPDataOffset(readerAt, headerOffset)
		if err != nil {
			// Best effort: assume no local extra field (common for archives
			// that only store extra data in the central directory).
			dataOffset = headerOffset + 30 + int64(len(file.Name))
		}

		extracted = append(extracted, &storage.ExtractedFileInfo{
			FileName:     name,
			InternalPath: internal,
			FileSize:     file.UncompressedSize,
			DataOffset:   dataOffset,
			IsStored:     file.IsStored,
		})
	}

	if len(extracted) == 0 {
		return nil, fmt.Errorf("no stored files found in ZIP archive")
	}

	return buildExtractedArchiveFiles(group, password, storage.NZBFileTypeZip, baseSegments, volumeInfos, extracted)
}

// ParseArchive parses ZIP archive from volumes
func (p *ZIPParser) parseArchive(ctx context.Context, volumes []*types.Volume) (*ZIPArchiveInfo, error) {
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes provided")
	}
	readerAt, archiveSize, err := newArticleReaderAt(ctx, p.source, volumes)
	if err != nil {
		return nil, err
	}
	return p.parseArchiveReader(readerAt, archiveSize, len(volumes) > 1)
}

func (p *ZIPParser) parseArchiveReader(readerAt io.ReaderAt, archiveSize int64, multiPart bool) (*ZIPArchiveInfo, error) {
	if archiveSize < 22 {
		return nil, fmt.Errorf("ZIP archive is too small: %d bytes", archiveSize)
	}
	tailSize := min(archiveSize, int64(defaultZIPEndSnippetSize))
	tailStart := archiveSize - tailSize
	endSnippet := make([]byte, tailSize)
	if _, err := readerAt.ReadAt(endSnippet, tailStart); err != nil {
		return nil, fmt.Errorf("failed to read ZIP tail: %w", err)
	}

	// Find and parse End of Central Directory record
	endOfCentralDir, eocdPos, err := p.findEndOfCentralDirectory(endSnippet)
	if err != nil {
		return nil, fmt.Errorf("failed to find central directory: %w", err)
	}

	totalEntries, centralDirSize, dirEnd, err := zipCentralDirectoryMetadata(endSnippet, endOfCentralDir, eocdPos)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve central directory: %w", err)
	}
	dirEnd += tailStart
	dirStart := dirEnd - centralDirSize
	if centralDirSize < 0 || dirStart < 0 || dirEnd > archiveSize {
		return nil, fmt.Errorf("invalid central directory range [%d, %d) for %d-byte archive", dirStart, dirEnd, archiveSize)
	}
	section := io.NewSectionReader(readerAt, dirStart, centralDirSize)
	files, err := p.parseCentralDirectoryReader(bufio.NewReaderSize(section, 64<<10), totalEntries)
	if err != nil {
		return nil, fmt.Errorf("failed to parse central directory: %w", err)
	}

	archiveInfo := &ZIPArchiveInfo{
		Files:       files,
		TotalFiles:  len(files),
		IsMultiPart: multiPart,
	}

	for _, file := range files {
		archiveInfo.TotalSize += file.UncompressedSize
	}

	p.logger.Debug().
		Int("total_files", len(files)).
		Int("stored_files", countStoredZIPFiles(files)).
		Msg("ZIP parsing complete")

	return archiveInfo, nil
}

// endOfCentralDirRecord represents the End of Central Directory record
type endOfCentralDirRecord struct {
	diskNumber       uint16
	centralDirDisk   uint16
	diskEntries      uint16
	totalEntries     uint16
	centralDirSize   uint32
	centralDirOffset uint32
	commentLength    uint16
}

// findEndOfCentralDirectory finds and parses the End of Central Directory
// position is what lets the caller anchor the central directory correctly
// even when the archive has a trailing comment.
func (p *ZIPParser) findEndOfCentralDirectory(data []byte) (*endOfCentralDirRecord, int, error) {
	// Prefer a record whose declared comment ends exactly at the archive end.
	// A second compatibility pass accepts trailing bytes used by some tools.
	for requireExactEnd := true; ; requireExactEnd = false {
		for i := len(data) - 22; i >= 0; i-- {
			if binary.LittleEndian.Uint32(data[i:]) != ZIPEndOfCentralDirSig {
				continue
			}
			commentLength := binary.LittleEndian.Uint16(data[i+20:])
			if requireExactEnd && i+22+int(commentLength) != len(data) {
				continue
			}
			record := &endOfCentralDirRecord{
				diskNumber:       binary.LittleEndian.Uint16(data[i+4:]),
				centralDirDisk:   binary.LittleEndian.Uint16(data[i+6:]),
				diskEntries:      binary.LittleEndian.Uint16(data[i+8:]),
				totalEntries:     binary.LittleEndian.Uint16(data[i+10:]),
				centralDirSize:   binary.LittleEndian.Uint32(data[i+12:]),
				centralDirOffset: binary.LittleEndian.Uint32(data[i+16:]),
				commentLength:    commentLength,
			}

			return record, i, nil
		}
		if !requireExactEnd {
			break
		}
	}

	return nil, 0, fmt.Errorf("end of Central Directory signature not found")
}

// parseCentralDirectory parses the central directory entries. eocdPos is the
// position of the EOCD record within data: the directory ends exactly there
// (or at the ZIP64 EOCD record for ZIP64 archives), so anchoring on it stays
// correct when the archive has a trailing comment — the previous end-of-buffer
// arithmetic was shifted by the comment length and failed on the first entry.
func (p *ZIPParser) parseCentralDirectory(data []byte, eocd *endOfCentralDirRecord, eocdPos int) ([]*ZIPFileEntry, error) {
	totalEntries, centralDirSize, dirEnd, err := zipCentralDirectoryMetadata(data, eocd, eocdPos)
	if err != nil {
		return nil, err
	}
	dirStart := dirEnd - centralDirSize
	if dirStart < 0 || dirEnd > int64(len(data)) {
		return nil, fmt.Errorf("central directory range [%d, %d) is outside the supplied %d bytes", dirStart, dirEnd, len(data))
	}
	return p.parseCentralDirectoryEntries(data[dirStart:dirEnd], totalEntries)
}

func zipCentralDirectoryMetadata(data []byte, eocd *endOfCentralDirRecord, eocdPos int) (int64, int64, int64, error) {
	if eocd == nil || eocdPos < 0 || eocdPos > len(data) {
		return 0, 0, 0, fmt.Errorf("invalid end of central directory record")
	}
	totalEntries := int64(eocd.totalEntries)
	centralDirSize := int64(eocd.centralDirSize)
	dirEnd := int64(eocdPos)
	if eocd.totalEntries == 0xFFFF || eocd.centralDirSize == 0xFFFFFFFF || eocd.centralDirOffset == 0xFFFFFFFF {
		z64Entries, z64Size, z64Pos, ok := findZIP64EndOfCentralDirectory(data, eocdPos)
		if !ok {
			return 0, 0, 0, fmt.Errorf("ZIP64 archive but ZIP64 end of central directory record not found in tail")
		}
		totalEntries = z64Entries
		centralDirSize = z64Size
		dirEnd = z64Pos
	}
	if totalEntries < 0 || centralDirSize < 0 {
		return 0, 0, 0, fmt.Errorf("ZIP central directory values overflow int64")
	}
	return totalEntries, centralDirSize, dirEnd, nil
}

func (p *ZIPParser) parseCentralDirectoryEntries(data []byte, totalEntries int64) ([]*ZIPFileEntry, error) {
	return p.parseCentralDirectoryReader(bytes.NewReader(data), totalEntries)
}

func (p *ZIPParser) parseCentralDirectoryReader(reader io.Reader, totalEntries int64) ([]*ZIPFileEntry, error) {
	files := make([]*ZIPFileEntry, 0, min(totalEntries, int64(1024)))
	for i := int64(0); i < totalEntries; i++ {
		file, err := p.parseCentralDirEntry(reader)
		if err != nil {
			return nil, fmt.Errorf("parse central directory entry %d of %d: %w", i+1, totalEntries, err)
		}

		if file != nil {
			files = append(files, file)
		}
	}

	return files, nil
}

// findZIP64EndOfCentralDirectory locates the ZIP64 EOCD record in the tail
// snippet by signature and returns (totalEntries, centralDirSize, recordPos).
// The ZIP64 EOCD locator (which precedes the plain EOCD) stores the record's
// absolute archive offset, which is useless against a tail snippet, so the
// record is found by scanning instead.
func findZIP64EndOfCentralDirectory(data []byte, eocdPos int) (int64, int64, int64, bool) {
	// ZIP64 EOCD record layout (fixed portion, 56 bytes):
	//  0: signature (4)          12: version made by (2)   24: entries this disk (8)
	//  4: record size (8)        14: version needed (2)    32: total entries (8)
	//                            16: disk number (4)       40: central dir size (8)
	//                            20: central dir disk (4)  48: central dir offset (8)
	end := eocdPos - 20 // record ends where the 20-byte ZIP64 EOCD locator starts
	if end < 0 || end > len(data) {
		end = eocdPos
	}
	for i := end - 56; i >= 0; i-- {
		if binary.LittleEndian.Uint32(data[i:]) != ZIPZIP64EndOfCentralDirSig {
			continue
		}
		entriesValue := binary.LittleEndian.Uint64(data[i+32:])
		sizeValue := binary.LittleEndian.Uint64(data[i+40:])
		if entriesValue > uint64(1<<63-1) || sizeValue > uint64(1<<63-1) {
			return 0, 0, 0, false
		}
		totalEntries := int64(entriesValue)
		centralDirSize := int64(sizeValue)
		return totalEntries, centralDirSize, int64(i), true
	}
	return 0, 0, 0, false
}

// parseCentralDirEntry parses a single central directory entry
func (p *ZIPParser) parseCentralDirEntry(r io.Reader) (*ZIPFileEntry, error) {
	// Read signature
	var sig uint32
	if err := binary.Read(r, binary.LittleEndian, &sig); err != nil {
		return nil, err
	}

	if sig != ZIPCentralDirectoryHeaderSig {
		return nil, fmt.Errorf("invalid central directory signature: 0x%08x", sig)
	}

	// Read header fields
	var header struct {
		VersionMadeBy      uint16
		VersionNeeded      uint16
		Flags              uint16
		Method             uint16
		ModTime            uint16
		ModDate            uint16
		CRC32              uint32
		CompressedSize     uint32
		UncompressedSize   uint32
		FilenameLength     uint16
		ExtraFieldLength   uint16
		CommentLength      uint16
		DiskNumberStart    uint16
		InternalAttributes uint16
		ExternalAttributes uint32
		LocalHeaderOffset  uint32
	}

	if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
		return nil, err
	}

	// Read filename
	filenameBytes := make([]byte, header.FilenameLength)
	if _, err := io.ReadFull(r, filenameBytes); err != nil {
		return nil, err
	}
	filename := strings.ToValidUTF8(string(filenameBytes), "")

	uncompressedSize := int64(header.UncompressedSize)
	compressedSize := int64(header.CompressedSize)
	localHeaderOffset := int64(header.LocalHeaderOffset)
	diskNumberStart := int64(header.DiskNumberStart)

	// Parse the extra field: for ZIP64 archives the 32-bit size/offset fields
	// hold 0xFFFFFFFF sentinels and the real 64-bit values live in the ZIP64
	// extra record (id 0x0001), in fixed order, present only for the fields
	// that are saturated in the fixed header.
	if header.ExtraFieldLength > 0 {
		extra := make([]byte, header.ExtraFieldLength)
		if _, err := io.ReadFull(r, extra); err != nil {
			return nil, err
		}
		for len(extra) >= 4 {
			id := binary.LittleEndian.Uint16(extra)
			size := int(binary.LittleEndian.Uint16(extra[2:]))
			extra = extra[4:]
			if size > len(extra) {
				return nil, fmt.Errorf("ZIP extra field 0x%04x declares %d bytes with only %d remaining", id, size, len(extra))
			}
			if id == 0x0001 {
				f := extra[:size]
				take64 := func() (int64, error) {
					if len(f) < 8 {
						return 0, io.ErrUnexpectedEOF
					}
					value := binary.LittleEndian.Uint64(f)
					f = f[8:]
					if value > uint64(1<<63-1) {
						return 0, fmt.Errorf("ZIP64 value %d overflows int64", value)
					}
					return int64(value), nil
				}
				take32 := func() (int64, error) {
					if len(f) < 4 {
						return 0, io.ErrUnexpectedEOF
					}
					value := binary.LittleEndian.Uint32(f)
					f = f[4:]
					return int64(value), nil
				}
				if header.UncompressedSize == 0xFFFFFFFF {
					value, err := take64()
					if err != nil {
						return nil, fmt.Errorf("read ZIP64 uncompressed size: %w", err)
					}
					uncompressedSize = value
				}
				if header.CompressedSize == 0xFFFFFFFF {
					value, err := take64()
					if err != nil {
						return nil, fmt.Errorf("read ZIP64 compressed size: %w", err)
					}
					compressedSize = value
				}
				if header.LocalHeaderOffset == 0xFFFFFFFF {
					value, err := take64()
					if err != nil {
						return nil, fmt.Errorf("read ZIP64 local header offset: %w", err)
					}
					localHeaderOffset = value
				}
				if header.DiskNumberStart == 0xFFFF {
					value, err := take32()
					if err != nil {
						return nil, fmt.Errorf("read ZIP64 start disk: %w", err)
					}
					diskNumberStart = value
				}
			}
			extra = extra[size:]
		}
	}

	// Skip comment
	if _, err := io.CopyN(io.Discard, r, int64(header.CommentLength)); err != nil {
		return nil, err
	}

	// Check if directory
	isDir := strings.HasSuffix(filename, "/") || uncompressedSize == 0

	return &ZIPFileEntry{
		Name:              filename,
		UncompressedSize:  uncompressedSize,
		CompressedSize:    compressedSize,
		Method:            header.Method,
		IsStored:          header.Method == ZIPStoreMethod,
		IsDirectory:       isDir,
		LocalHeaderOffset: localHeaderOffset,
		DiskNumberStart:   diskNumberStart,
		CRC32:             header.CRC32,
	}, nil
}

func countStoredZIPFiles(files []*ZIPFileEntry) int {
	count := 0
	for _, file := range files {
		if file.IsStored && !file.IsDirectory {
			count++
		}
	}
	return count
}

func absoluteZIPHeaderOffset(file *ZIPFileEntry, volumeStarts []int64) (int64, error) {
	if file == nil {
		return 0, fmt.Errorf("ZIP file entry is nil")
	}
	if file.DiskNumberStart < 0 || file.DiskNumberStart >= int64(len(volumeStarts)) {
		return 0, fmt.Errorf("start disk %d is outside %d archive volumes", file.DiskNumberStart, len(volumeStarts))
	}
	offset := volumeStarts[file.DiskNumberStart] + file.LocalHeaderOffset
	if offset < volumeStarts[file.DiskNumberStart] {
		return 0, fmt.Errorf("local header offset overflows int64")
	}
	return offset, nil
}

// calculateZIPDataOffset calculates the actual data offset by reading the local file header.
func (p *ZIPParser) calculateZIPDataOffset(readerAt io.ReaderAt, headerOffset int64) (int64, error) {
	// We need to read the local file header at LocalHeaderOffset to get:
	// - Filename length (2 bytes at offset 26)
	// - Extra field length (2 bytes at offset 28)
	// Data starts at: LocalHeaderOffset + 30 + filename_length + extra_field_length

	if readerAt == nil {
		return 0, fmt.Errorf("ZIP archive reader is nil")
	}

	// We only need to read 30 bytes to get filename and extra field lengths
	// Local header structure:
	// 0-3: signature (0x04034b50)
	// 4-25: various fields
	// 26-27: filename length (uint16)
	// 28-29: extra field length (uint16)
	headerData := make([]byte, 30)

	if _, err := readerAt.ReadAt(headerData, headerOffset); err != nil {
		return 0, fmt.Errorf("failed to read local header: %w", err)
	}

	// Verify signature
	sig := binary.LittleEndian.Uint32(headerData[0:4])
	if sig != ZIPLocalFileHeaderSig {
		return 0, fmt.Errorf("invalid local file header signature: 0x%08x", sig)
	}

	// Extract filename and extra field lengths
	filenameLen := binary.LittleEndian.Uint16(headerData[26:28])
	extraFieldLen := binary.LittleEndian.Uint16(headerData[28:30])

	// Calculate data offset
	dataOffset := headerOffset + 30 + int64(filenameLen) + int64(extraFieldLen)

	return dataOffset, nil
}
