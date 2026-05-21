package parser

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	fs2 "io/fs"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/fs"
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
	manager       *nntp.Client
	maxConcurrent int
	logger        zerolog.Logger
}

// NewZIPParser creates a new ZIP parser
func NewZIPParser(manager *nntp.Client, maxConcurrent int, logger zerolog.Logger) *ZIPParser {
	return &ZIPParser{
		manager:       manager,
		maxConcurrent: maxConcurrent,
		logger:        logger.With().Str("component", "zip_parser").Logger(),
	}
}

func (p *ZIPParser) Process(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	sort.Slice(group.Files, func(i, j int) bool {
		oi := getZIPVolumeOrder(group.Files[i].Filename)
		oj := getZIPVolumeOrder(group.Files[j].Filename)
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

	// Use snippet-based parsing instead of downloading entire archive
	archiveInfo, err := p.parseArchive(ctx, volumes)
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
		dataOffset, err := p.calculateZIPDataOffset(ctx, volumes, file)
		if err != nil {
			// Best effort: assume no local extra field (common for archives
			// that only store extra data in the central directory).
			dataOffset = file.LocalHeaderOffset + 30 + int64(len(file.Name))
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

	return buildExtractedArchiveFiles(group, password, storage.NZBFileTypeZip, baseSegments, volumeInfos, extracted), nil
}

// ParseArchive parses ZIP archive from volumes
func (p *ZIPParser) parseArchive(ctx context.Context, volumes []*types.Volume) (*ZIPArchiveInfo, error) {
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes provided")
	}

	lastVolume := volumes[len(volumes)-1]
	endSnippet, err := p.fetchVolumeEndSnippet(ctx, lastVolume)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch end snippet: %w", err)
	}

	// Find and parse End of Central Directory record
	endOfCentralDir, err := p.findEndOfCentralDirectory(endSnippet)
	if err != nil {
		return nil, fmt.Errorf("failed to find central directory: %w", err)
	}

	// Parse central directory entries
	files, err := p.parseCentralDirectory(endSnippet, endOfCentralDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse central directory: %w", err)
	}

	archiveInfo := &ZIPArchiveInfo{
		Files:       files,
		TotalFiles:  len(files),
		IsMultiPart: len(volumes) > 1,
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

// fetchVolumeEndSnippet fetches the last 256KB of a volume
func (p *ZIPParser) fetchVolumeEndSnippet(ctx context.Context, vol *types.Volume) ([]byte, error) {
	if len(vol.Segments) == 0 {
		return nil, fmt.Errorf("volume has no segments")
	}

	// get last segment
	lastSegment := vol.Segments[len(vol.Segments)-1]

	// Fetch the entire last segment (ZIP central dir is at end)
	var body []byte
	err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
		data, e := conn.GetDecodedBody(lastSegment.MessageID)
		body = data
		return e
	})
	if err != nil {
		return nil, fmt.Errorf("failed to fetch segment: %w", err)
	}

	// If we need more data, fetch previous segments too
	if len(body) < defaultZIPEndSnippetSize && len(vol.Segments) > 1 {
		// Fetch a few more segments to get enough data
		segmentsNeeded := min(3, len(vol.Segments)-1)
		allData := make([]byte, 0, defaultZIPEndSnippetSize)

		for i := len(vol.Segments) - segmentsNeeded - 1; i < len(vol.Segments); i++ {
			seg := vol.Segments[i]
			var data []byte
			err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
				d, e := conn.GetDecodedBody(seg.MessageID)
				data = d
				return e
			})
			if err != nil {
				continue
			}
			allData = append(allData, data...)
		}

		if len(allData) > 0 {
			body = allData
		}
	}

	return body, nil
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

// findEndOfCentralDirectory finds and parses the End of Central Directory record
func (p *ZIPParser) findEndOfCentralDirectory(data []byte) (*endOfCentralDirRecord, error) {
	// Search backwards for the signature
	// EOCD is at the end, but there might be a comment after it
	for i := len(data) - 22; i >= 0; i-- {
		if i+4 > len(data) {
			continue
		}

		sig := binary.LittleEndian.Uint32(data[i:])
		if sig == ZIPEndOfCentralDirSig {
			// Found it! Parse the record
			if i+22 > len(data) {
				continue
			}

			record := &endOfCentralDirRecord{
				diskNumber:       binary.LittleEndian.Uint16(data[i+4:]),
				centralDirDisk:   binary.LittleEndian.Uint16(data[i+6:]),
				diskEntries:      binary.LittleEndian.Uint16(data[i+8:]),
				totalEntries:     binary.LittleEndian.Uint16(data[i+10:]),
				centralDirSize:   binary.LittleEndian.Uint32(data[i+12:]),
				centralDirOffset: binary.LittleEndian.Uint32(data[i+16:]),
				commentLength:    binary.LittleEndian.Uint16(data[i+20:]),
			}

			return record, nil
		}
	}

	return nil, fmt.Errorf("end of Central Directory signature not found")
}

// parseCentralDirectory parses the central directory entries
func (p *ZIPParser) parseCentralDirectory(data []byte, eocd *endOfCentralDirRecord) ([]*ZIPFileEntry, error) {
	var files []*ZIPFileEntry

	// Find where central directory starts in our snippet
	// We fetched from the end, so calculate offset
	snippetStart := int64(len(data)) - int64(eocd.centralDirSize) - 22 // 22 = EOCD record size
	if snippetStart < 0 {
		// Central directory might span across our snippet
		// Parse what we have
		snippetStart = 0
	}

	r := bytes.NewReader(data[snippetStart:])

	for i := 0; i < int(eocd.totalEntries); i++ {
		file, err := p.parseCentralDirEntry(r)
		if err != nil {
			// If we can't parse, we've likely run out of data
			p.logger.Debug().Err(err).Int("parsed", i).Msg("Stopped parsing central directory")
			break
		}

		if file != nil {
			files = append(files, file)
		}
	}

	return files, nil
}

// parseCentralDirEntry parses a single central directory entry
func (p *ZIPParser) parseCentralDirEntry(r *bytes.Reader) (*ZIPFileEntry, error) {
	// Check if we have enough data for header
	if r.Len() < 46 {
		return nil, io.EOF
	}

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

	// Skip extra field and comment
	skipLen := int64(header.ExtraFieldLength) + int64(header.CommentLength)
	if _, err := r.Seek(skipLen, io.SeekCurrent); err != nil {
		return nil, err
	}

	// Check if directory
	isDir := strings.HasSuffix(filename, "/") || header.UncompressedSize == 0

	return &ZIPFileEntry{
		Name:              filename,
		UncompressedSize:  int64(header.UncompressedSize),
		CompressedSize:    int64(header.CompressedSize),
		Method:            header.Method,
		IsStored:          header.Method == ZIPStoreMethod,
		IsDirectory:       isDir,
		LocalHeaderOffset: int64(header.LocalHeaderOffset),
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

// calculateZIPDataOffset calculates the actual data offset by reading the local file header
func (p *ZIPParser) calculateZIPDataOffset(ctx context.Context, volumes []*types.Volume, file *ZIPFileEntry) (int64, error) {
	// We need to read the local file header at LocalHeaderOffset to get:
	// - Filename length (2 bytes at offset 26)
	// - Extra field length (2 bytes at offset 28)
	// Data starts at: LocalHeaderOffset + 30 + filename_length + extra_field_length

	headerOffset := file.LocalHeaderOffset

	// Create a temporary FS to read the local header
	usenetFS, err := fs.NewFS(ctx, p.manager, p.maxConcurrent, 0, volumes, p.logger) // 0 prefetch for parsing
	if err != nil {
		return 0, fmt.Errorf("failed to create FS: %w", err)
	}

	// We only need to read 30 bytes to get filename and extra field lengths
	// Local header structure:
	// 0-3: signature (0x04034b50)
	// 4-25: various fields
	// 26-27: filename length (uint16)
	// 28-29: extra field length (uint16)
	headerData := make([]byte, 30)

	// Open the volume
	f, err := usenetFS.Open(volumes[0].Name)
	if err != nil {
		return 0, fmt.Errorf("failed to open volume: %w", err)
	}
	defer func(f fs2.File) {
		_ = f.Close()
	}(f)

	// Seek to local header
	if seeker, ok := f.(io.Seeker); ok {
		if _, err := seeker.Seek(headerOffset, io.SeekStart); err != nil {
			return 0, fmt.Errorf("failed to seek: %w", err)
		}
	} else {
		return 0, fmt.Errorf("volume does not support seeking")
	}

	// Read the header
	if _, err := io.ReadFull(f, headerData); err != nil {
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
