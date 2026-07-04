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
		return group.Files[i].Filename < group.Files[j].Filename
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
	endOfCentralDir, eocdPos, err := p.findEndOfCentralDirectory(endSnippet)
	if err != nil {
		return nil, fmt.Errorf("failed to find central directory: %w", err)
	}

	// Parse central directory entries
	files, err := p.parseCentralDirectory(endSnippet, endOfCentralDir, eocdPos)
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

// fetchVolumeEndSnippet fetches the tail of a volume (the last segment,
// widened backwards by up to three more if that alone is smaller than the
// snippet target). A failed fetch aborts the parse: silently skipping a
// segment would splice a hole into the concatenation and shift every central
// directory offset parsed from it.
func (p *ZIPParser) fetchVolumeEndSnippet(ctx context.Context, vol *types.Volume) ([]byte, error) {
	if len(vol.Segments) == 0 {
		return nil, fmt.Errorf("volume has no segments")
	}

	fetch := func(messageID string) ([]byte, error) {
		var body []byte
		err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
			data, e := conn.GetDecodedBody(messageID)
			body = data
			return e
		})
		return body, err
	}

	last := len(vol.Segments) - 1
	body, err := fetch(vol.Segments[last].MessageID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch segment: %w", err)
	}

	for i := last - 1; i >= 0 && len(body) < defaultZIPEndSnippetSize && last-i <= 3; i-- {
		data, err := fetch(vol.Segments[i].MessageID)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch segment %s: %w", vol.Segments[i].MessageID, err)
		}
		body = append(data, body...)
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

// findEndOfCentralDirectory finds and parses the End of Central Directory
// record, returning it together with its byte position within data. The
// position is what lets the caller anchor the central directory correctly
// even when the archive has a trailing comment.
func (p *ZIPParser) findEndOfCentralDirectory(data []byte) (*endOfCentralDirRecord, int, error) {
	// Search backwards for the signature
	// EOCD is at the end, but there might be a comment after it
	for i := len(data) - 22; i >= 0; i-- {
		sig := binary.LittleEndian.Uint32(data[i:])
		if sig == ZIPEndOfCentralDirSig {
			record := &endOfCentralDirRecord{
				diskNumber:       binary.LittleEndian.Uint16(data[i+4:]),
				centralDirDisk:   binary.LittleEndian.Uint16(data[i+6:]),
				diskEntries:      binary.LittleEndian.Uint16(data[i+8:]),
				totalEntries:     binary.LittleEndian.Uint16(data[i+10:]),
				centralDirSize:   binary.LittleEndian.Uint32(data[i+12:]),
				centralDirOffset: binary.LittleEndian.Uint32(data[i+16:]),
				commentLength:    binary.LittleEndian.Uint16(data[i+20:]),
			}

			return record, i, nil
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
	totalEntries := int64(eocd.totalEntries)
	centralDirSize := int64(eocd.centralDirSize)
	dirEnd := int64(eocdPos)

	// ZIP64: 0xFFFF / 0xFFFFFFFF are sentinels meaning "the real value is in
	// the ZIP64 EOCD record" — mandatory for archives over 4GB, i.e. most
	// media zips. Without this the sentinel sizes made the anchor arithmetic
	// garbage and every entry failed to parse.
	if eocd.totalEntries == 0xFFFF || eocd.centralDirSize == 0xFFFFFFFF || eocd.centralDirOffset == 0xFFFFFFFF {
		z64Entries, z64Size, z64Pos, ok := findZIP64EndOfCentralDirectory(data, eocdPos)
		if !ok {
			return nil, fmt.Errorf("ZIP64 archive but ZIP64 end of central directory record not found in snippet")
		}
		totalEntries = z64Entries
		centralDirSize = z64Size
		dirEnd = z64Pos
	}

	dirStart := dirEnd - centralDirSize
	if dirStart < 0 {
		// Central directory extends beyond our snippet: it starts mid-entry,
		// so realign to the first entry signature we can find and parse the
		// (partial) rest.
		dirStart = 0
		for dirStart+4 <= dirEnd && binary.LittleEndian.Uint32(data[dirStart:]) != ZIPCentralDirectoryHeaderSig {
			dirStart++
		}
	}

	r := bytes.NewReader(data[dirStart:dirEnd])

	var files []*ZIPFileEntry
	for i := int64(0); i < totalEntries; i++ {
		file, err := p.parseCentralDirEntry(r)
		if err != nil {
			// If we can't parse, we've likely run out of data
			p.logger.Debug().Err(err).Int64("parsed", i).Msg("Stopped parsing central directory")
			break
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
		totalEntries := int64(binary.LittleEndian.Uint64(data[i+32:]))
		centralDirSize := int64(binary.LittleEndian.Uint64(data[i+40:]))
		return totalEntries, centralDirSize, int64(i), true
	}
	return 0, 0, 0, false
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

	uncompressedSize := int64(header.UncompressedSize)
	compressedSize := int64(header.CompressedSize)
	localHeaderOffset := int64(header.LocalHeaderOffset)

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
				break
			}
			if id == 0x0001 {
				f := extra[:size]
				take := func() (int64, bool) {
					if len(f) < 8 {
						return 0, false
					}
					v := int64(binary.LittleEndian.Uint64(f))
					f = f[8:]
					return v, true
				}
				if header.UncompressedSize == 0xFFFFFFFF {
					if v, ok := take(); ok {
						uncompressedSize = v
					}
				}
				if header.CompressedSize == 0xFFFFFFFF {
					if v, ok := take(); ok {
						compressedSize = v
					}
				}
				if header.LocalHeaderOffset == 0xFFFFFFFF {
					if v, ok := take(); ok {
						localHeaderOffset = v
					}
				}
			}
			extra = extra[size:]
		}
	}

	// Skip comment
	if _, err := r.Seek(int64(header.CommentLength), io.SeekCurrent); err != nil {
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
