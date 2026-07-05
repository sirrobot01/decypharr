package parser

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/Tensai75/nzbparser"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/crypto"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
	"github.com/sourcegraph/conc/iter"
)

// RAR format constants
const (
	RAR5Signature = "Rar!\x1A\x07\x01\x00"
	RAR4Signature = "Rar!\x1A\x07\x00"

	RAR5HeaderTypeMain     = 1
	RAR5HeaderTypeFile     = 2
	RAR5HeaderTypeService  = 3
	RAR5HeaderTypeEncrypt  = 4
	RAR5HeaderTypeEndOfArc = 5

	RAR5HeaderFlagExtraArea     = 0x0001
	RAR5HeaderFlagDataArea      = 0x0002
	RAR5HeaderFlagSkipIfUnknown = 0x0004
	RAR5HeaderFlagDataSector    = 0x0008

	RAR5MainFlagVolume       = 0x0001 // Archive is part of multi-volume set
	RAR5MainFlagVolumeNumber = 0x0002 // Volume number field is present
	RAR5MainFlagSolid        = 0x0004 // Solid archive
	RAR5MainFlagRecovery     = 0x0008 // Recovery record present
	RAR5MainFlagLocked       = 0x0010 // Locked archive

	RAR5FileFlagDirectory      = 0x0001
	RAR5FileFlagHasUnixTime    = 0x0002
	RAR5FileFlagHasCRC32       = 0x0004
	RAR5FileFlagUnpSizeUnknown = 0x0008

	RAR5ExtraTypeEncryption = 0x01 // File encryption record (contains IV)
	RAR5ExtraTypeHash       = 0x02 // File hash record
	RAR5ExtraTypeTime       = 0x03 // Extended time record
	RAR5ExtraTypeVersion    = 0x04 // Version info
	RAR5ExtraTypeRedirect   = 0x05 // Symlink record
	RAR5ExtraTypeOwner      = 0x06 // Unix owner record
	RAR5ExtraTypeService    = 0x07 // Service data

	RAR5CompressionMethodStore = 0

	RAR4HeaderTypeMarker  = 0x72
	RAR4HeaderTypeArchive = 0x73
	RAR4HeaderTypeFile    = 0x74
	RAR4HeaderTypeService = 0x7A
	RAR4HeaderTypeEnd     = 0x7B

	RAR4HeaderFlagHasAdd    = 0x0001
	RAR4HeaderFlagLongBlock = 0x8000
	RAR4FileFlagDirectory   = 0x00E0
	RAR4FileFlagSolid       = 0x0010
	RAR4FileFlagEncrypted   = 0x0004 // File data is encrypted
	RAR4FileFlagHighSize    = 0x0100 // 64-bit file size (high 4 bytes follow after low 4 bytes)
	RAR4ArchiveFlagPassword = 0x0080 // Archive headers are encrypted

	RAR4CompressionMethodStore = 0x30
)

// RARVersion represents the RAR format version
type RARVersion int

const (
	RARVersion4       RARVersion = 4
	RARVersion5       RARVersion = 5
	RARVersionUnknown RARVersion = 0
)

// RARArchiveInfo contains information about the entire RAR archive
type RARArchiveInfo struct {
	Version           RARVersion
	IsMultiVol        bool
	IsHeaderEncrypted bool   // Headers are encrypted (needs password to list files)
	IsDataEncrypted   bool   // File data is encrypted
	EncryptionKey     []byte // AES-256 key derived from password (32 bytes)
	Files             []*RARFileEntry
	// VolumeOrder, when non-nil, gives the true volume order as a permutation of
	// the input volume positions: VolumeOrder[k] = the input index of the volume
	// whose true RAR5 volume number is k-th. It is set only when every volume
	// reported a distinct main-header volume number, so the caller can reorder
	// the (otherwise NZB-ordered) volumes/segments to the correct sequence. Nil
	// means "ordering unknown — keep NZB/upload order" (the safe default).
	VolumeOrder []int
}

// RARFileEntry represents a file within the RAR archive
type RARFileEntry struct {
	Name             string
	UncompressedSize int64
	PackedSize       int64
	DataOffset       int64 // Offset where compressed data starts
	IsStored         bool  // True if stored (method 0), false if compressed
	IsDirectory      bool
	IsEncrypted      bool                   // File data is encrypted
	EncryptionKey    []byte                 // AES-256 key for data decryption (derived from extra area salt)
	EncryptionIV     []byte                 // AES IV for data decryption (16 bytes, from extra area)
	VolumeParts      []*types.RARVolumePart // Parts across volumes
	CRC32            uint32
	VolumeIndex      int // Which volume this file starts in
}

// RARParser handles parsing RAR archives from usenet segments
type RARParser struct {
	manager       *nntp.Client
	maxConcurrent int
	logger        zerolog.Logger
}

// NewRARParser creates a new RAR parser
func NewRARParser(manager *nntp.Client, maxConcurrent int, logger zerolog.Logger) *RARParser {
	return &RARParser{
		manager:       manager,
		maxConcurrent: maxConcurrent,
		logger:        logger.With().Str("component", "rar_parser").Logger(),
	}
}

func (p *RARParser) Process(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	p.logger.Debug().
		Str("group", group.BaseName).
		Int("file_count", len(group.Files)).
		Msg("Starting RAR archive processing")

	if len(group.Files) == 0 {
		return nil, fmt.Errorf("no files")
	}

	// Sort RAR files by volume order (.rar first, then .r00, .r01, etc.)
	// For obfuscated filenames that all get the same sort key, fall back to
	// NZB file Number (upload order) which preserves the original volume sequence.
	sort.Slice(group.Files, func(i, j int) bool {
		oi := getRARVolumeOrder(group.Files[i].Filename)
		oj := getRARVolumeOrder(group.Files[j].Filename)
		if oi != oj {
			return oi < oj
		}
		return group.Files[i].Number < group.Files[j].Number
	})

	volumes := buildArchiveVolumeDescriptors(group)
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no RAR volumes found")
	}

	filename := group.BaseName
	filename = utils.RemoveInvalidChars(path.Base(filename))

	// Build base segments and volume info
	baseSegments, volumeInfos, _ := buildBaseSegments(group)
	if len(baseSegments) == 0 {
		return nil, fmt.Errorf("no base segments found for RAR volumes")
	}

	// Parse RAR archive to get file entries with volume parts
	archiveInfo, err := p.parseArchive(ctx, volumes, password)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RAR archive: %w", err)
	}

	// Check if archive has encrypted headers and we couldn't parse it
	if archiveInfo.IsHeaderEncrypted && len(archiveInfo.Files) == 0 {
		return nil, fmt.Errorf("RAR archive has encrypted headers; password required or incorrect")
	}

	// If parseArchive recovered a TRUE volume order that differs from the NZB
	// order (obfuscated archive whose volumes were posted out of sequence under
	// random filenames), reorder everything to that true sequence so the file
	// assembles correctly. baseSegments/volumeInfos were built in NZB order and
	// volume parts were stamped with NZB positions; we rebuild the segment
	// coordinate space from the reordered files and renumber the parts so the
	// downstream offset map (keyed by PartNumber) and the segment walk agree.
	// When VolumeOrder is nil we skip all of this and keep the existing,
	// proven NZB-order behavior.
	if len(archiveInfo.VolumeOrder) == len(group.Files) && len(group.Files) > 1 {
		// inverse[nzbIdx] = the volume's position in true order
		inverse := make([]int, len(group.Files))
		for truePos, nzbIdx := range archiveInfo.VolumeOrder {
			if nzbIdx < 0 || nzbIdx >= len(group.Files) {
				inverse = nil
				break
			}
			inverse[nzbIdx] = truePos
		}
		if inverse != nil {
			// Reorder group.Files into true volume order.
			reordered := make([]nzbparser.NzbFile, len(group.Files))
			for truePos, nzbIdx := range archiveInfo.VolumeOrder {
				reordered[truePos] = group.Files[nzbIdx]
			}
			group.Files = reordered

			// Rebuild the segment coordinate space from the reordered files so
			// baseSegments/volumeInfos/volumeOffsetMap are all in true order.
			// (volumes was already consumed by parseArchive above, so it does
			// not need rebuilding.)
			baseSegments, volumeInfos, _ = buildBaseSegments(group)

			// Renumber each parsed file's volume parts: a part stamped with NZB
			// index `nzbIdx` now lives at true position `inverse[nzbIdx]`, which
			// matches the rebuilt offset map's key for that volume.
			for _, rarFile := range archiveInfo.Files {
				if rarFile == nil {
					continue
				}
				for _, vp := range rarFile.VolumeParts {
					if vp == nil {
						continue
					}
					if vp.PartNumber >= 0 && vp.PartNumber < len(inverse) {
						vp.PartNumber = inverse[vp.PartNumber]
					}
				}
				if rarFile.VolumeIndex >= 0 && rarFile.VolumeIndex < len(inverse) {
					rarFile.VolumeIndex = inverse[rarFile.VolumeIndex]
				}
			}
			p.logger.Debug().
				Int("volumes", len(group.Files)).
				Msg("RAR volumes reordered to true volume sequence for assembly")
		}
	}

	// Build volume offset map
	volumeOffsetMap := buildVolumeOffsetMap(volumeInfos)

	files := make([]*storage.NZBFile, 0, len(archiveInfo.Files))
	hasNoneStored := false

	// Parse each file in the RAR archive
	for _, rarFile := range archiveInfo.Files {
		if rarFile.IsDirectory {
			continue
		}
		// Only process stored (uncompressed) files for streaming
		if !rarFile.IsStored {
			hasNoneStored = true
			continue
		}

		name := utils.RemoveInvalidChars(path.Base(rarFile.Name))
		if name == "" {
			name = filename
		}

		// Build segments for this file across all its volume parts
		fileSegments, err := p.buildSegmentsForFile(rarFile, baseSegments, volumeOffsetMap)
		if err != nil {
			continue
		}

		if len(fileSegments) == 0 {
			continue
		}

		streamSize := int64(0)
		for _, seg := range fileSegments {
			streamSize += seg.Bytes
		}

		size := rarFile.UncompressedSize
		if size <= 0 || (streamSize > 0 && size > streamSize) {
			// Clamp to streamable size to avoid advertising bytes we can't serve.
			size = streamSize
		}

		// DIAG (RAR offset/size investigation): dump the geometry used to build
		// this file's segment map. If the assembled file is structurally broken
		// (plays with a bogus duration / won't start) while every segment
		// decodes fine, the fault is here — wrong volume offsets, a
		// packed-vs-unpacked size mismatch, or bad DataOffset header-skipping.
		// Logged at debug; silent unless log_level=debug.
		if len(fileSegments) > 0 {
			first := fileSegments[0]
			last := fileSegments[len(fileSegments)-1]
			p.logger.Debug().
				Str("file", rarFile.Name).
				Int("volume_parts", len(rarFile.VolumeParts)).
				Int("segments", len(fileSegments)).
				Int64("uncompressed_size", rarFile.UncompressedSize).
				Int64("stream_size", streamSize).
				Int64("advertised_size", size).
				Bool("is_stored", rarFile.IsStored).
				Int64("first_start_offset", first.StartOffset).
				Int64("first_seg_data_start", first.SegmentDataStart).
				Int64("last_end_offset", last.EndOffset).
				Msg("RAR file geometry")
			for pi, part := range rarFile.VolumeParts {
				volOff := volumeOffsetMap[part.PartNumber]
				p.logger.Debug().
					Str("file", rarFile.Name).
					Int("part_index", pi).
					Int("part_number", part.PartNumber).
					Int64("vol_offset_map", volOff).
					Int64("data_offset", part.DataOffset).
					Int64("unpacked_size", part.UnpackedSize).
					Msg("RAR volume part geometry")
			}
		}

		file := &storage.NZBFile{
			Name:          name,
			InternalPath:  rarFile.Name,
			Groups:        getGroupsList(group.Groups),
			Segments:      fileSegments, // Direct segment list with offsets!
			Password:      password,
			FileType:      storage.NZBFileTypeRar,
			Size:          size,
			IsStored:      rarFile.IsStored,
			IsEncrypted:   rarFile.IsEncrypted, // Per-file encryption from extra area
			EncryptionKey: rarFile.EncryptionKey,
			EncryptionIV:  rarFile.EncryptionIV, // Per-file IV from extra area
		}

		// Fallback to global archive key if no specific file key derived
		if len(file.EncryptionKey) == 0 {
			file.EncryptionKey = archiveInfo.EncryptionKey
		}

		files = append(files, file)
	}

	if len(files) == 0 {
		if hasNoneStored {
			return nil, fmt.Errorf("RAR archive contains no stored (uncompressed) files; cannot stream")
		}
		return nil, fmt.Errorf("no valid files found in RAR archive")
	}
	return files, nil
}

// ParseArchive parses all volumes and extracts file information
func (p *RARParser) parseArchive(ctx context.Context, volumes []*types.Volume, password string) (*RARArchiveInfo, error) {
	if len(volumes) == 0 {
		return nil, fmt.Errorf("no volumes provided")
	}

	// Detect RAR version from first volume
	firstStream := newRarReader(ctx, p.manager, []*types.Volume{volumes[0]})
	sig := make([]byte, 8)
	if _, err := io.ReadFull(firstStream, sig); err != nil {
		return nil, fmt.Errorf("failed to read RAR signature: %w", err)
	}

	version := detectRARVersion(sig)
	if version == RARVersionUnknown {
		return nil, fmt.Errorf("unknown RAR format")
	}

	// Parse ALL volumes in parallel using worker pool
	type volumeResult struct {
		index             int
		files             []*RARFileEntry
		isHeaderEncrypted bool
		encryptionKey     []byte // AES-256 key for encrypted file data
		volumeNumber      int    // true 0-based volume number from RAR5 main header
		hasVolumeNumber   bool   // whether volumeNumber was parsed
		err               error
	}

	type volumeInput struct {
		idx int
		vol *types.Volume
	}

	// Create a worker pool with up to 10 concurrent workers (or number of volumes, whichever is smaller)
	maxWorkers := min(len(volumes), p.maxConcurrent)

	// Create input slice with volume index and volume
	inputs := make([]volumeInput, len(volumes))
	for i, vol := range volumes {
		inputs[i] = volumeInput{idx: i, vol: vol}
	}

	// Use iter.Mapper for parallel processing
	mapper := iter.Mapper[volumeInput, volumeResult]{
		MaxGoroutines: maxWorkers,
	}

	// Map function to parse each volume
	results := mapper.Map(inputs, func(input *volumeInput) volumeResult {
		volIdx := input.idx
		vol := input.vol

		// Create stream reader for this specific volume
		stream := newRarReader(ctx, p.manager, []*types.Volume{vol})

		// Skip signature (7 or 8 bytes depending on version)
		sigSize := 8
		if version == RARVersion4 {
			sigSize = 7
		}
		sigBuf := make([]byte, sigSize)
		if _, err := io.ReadFull(stream, sigBuf); err != nil {
			return volumeResult{index: volIdx, files: nil, err: err}
		}

		// Parse this volume's file entries
		var volumeFiles []*RARFileEntry
		var isEncrypted bool
		var err error

		switch version {
		case RARVersion5:
			result, parseErr := p.parseRAR5Stream(stream, volIdx, vol.Name, password)
			if parseErr != nil {
				err = parseErr
			} else if result != nil {
				volumeFiles = result.Files
				isEncrypted = result.IsHeaderEncrypted
				// Store the encryption key from first encrypted volume
				if len(result.EncryptionKey) > 0 {
					return volumeResult{index: volIdx, files: volumeFiles, isHeaderEncrypted: isEncrypted, encryptionKey: result.EncryptionKey, volumeNumber: result.VolumeNumber, hasVolumeNumber: result.HasVolumeNumber, err: nil}
				}
				return volumeResult{index: volIdx, files: volumeFiles, isHeaderEncrypted: isEncrypted, volumeNumber: result.VolumeNumber, hasVolumeNumber: result.HasVolumeNumber, err: nil}
			}
		case RARVersion4:
			volumeFiles, err = p.parseRAR4Stream(stream, volIdx, vol.Name, vol.Size)
		default:
			err = fmt.Errorf("unsupported RAR version: %d", version)
		}

		if err != nil {
			return volumeResult{index: volIdx, files: nil, err: err}
		}

		return volumeResult{index: volIdx, files: volumeFiles, isHeaderEncrypted: isEncrypted, err: nil}
	})

	// Determine whether we can establish the TRUE volume order from RAR5
	// main-header volume numbers. Default: keep NZB/upload order (correct for
	// in-order archives incl .partNN). Override when we can establish a complete,
	// unambiguous volume numbering — then compute a permutation the caller uses
	// to reorder the volumes/segments consistently.
	//
	// Robustness: real obfuscated postings can have ONE volume whose main header
	// lacks the volume-number bit (observed: 62 of 63 volumes numbered cleanly,
	// one with the bit unset). We tolerate exactly one such hole by inferring it
	// from the single missing value in the otherwise-contiguous sequence. Any
	// more ambiguity than that (2+ missing, duplicates, non-contiguous) → keep
	// NZB order rather than risk corrupting an archive that currently assembles.
	var volumeOrder []int

	// Collect successfully-parsed results and partition by whether a volume
	// number was read.
	type volEntry struct {
		idx    int
		num    int
		hasNum bool
	}
	entries := make([]volEntry, 0, len(results))
	numbered := 0
	unnumbered := 0
	for _, r := range results {
		if r.err != nil {
			continue
		}
		entries = append(entries, volEntry{idx: r.index, num: r.volumeNumber, hasNum: r.hasVolumeNumber})
		if r.hasVolumeNumber {
			numbered++
		} else {
			unnumbered++
		}
	}

	canOrder := len(entries) > 1 && numbered > 0
	// Check the numbered volumes are all distinct.
	if canOrder {
		seen := make(map[int]bool, len(entries))
		for _, e := range entries {
			if !e.hasNum {
				continue
			}
			if seen[e.num] {
				canOrder = false // duplicate volume numbers — unreliable
				break
			}
			seen[e.num] = true
		}
	}
	// Resolve unnumbered volumes. We allow at most one, inferred as the single
	// missing value in the contiguous span [min..min+len-1] of the numbering.
	if canOrder && unnumbered > 0 {
		if unnumbered != 1 {
			canOrder = false // more than one hole — too ambiguous
		} else {
			// Determine the expected contiguous span from the numbered values.
			minNum, maxNum := 1<<62, -(1 << 62)
			present := make(map[int]bool, len(entries))
			for _, e := range entries {
				if !e.hasNum {
					continue
				}
				present[e.num] = true
				if e.num < minNum {
					minNum = e.num
				}
				if e.num > maxNum {
					maxNum = e.num
				}
			}
			// The numbered volumes should form a contiguous run. The single
			// unnumbered volume is the one hole. There are two cases:
			//
			//  1. Interior gap: some value strictly inside [minNum..maxNum] is
			//     missing. That value is unambiguously the hole.
			//
			//  2. No interior gap: [minNum..maxNum] is fully present, so the
			//     numbered run is already complete and the hole is at a
			//     BOUNDARY (either minNum-1 or maxNum+1). In RAR multi-volume
			//     archives the FIRST volume (the base archive) is the one whose
			//     main header commonly lacks the volume-number field, while the
			//     continuation volumes carry an incrementing number. So when the
			//     run is complete, the unnumbered volume belongs at minNum-1
			//     (before the run), NOT maxNum+1. Placing it after the run
			//     rotates the true first volume to the end and corrupts assembly.
			missing := -1
			interiorMissing := -1
			interiorCount := 0
			for v := minNum; v <= maxNum; v++ {
				if !present[v] {
					interiorMissing = v
					interiorCount++
				}
			}
			if interiorCount == 1 {
				// Case 1: unique interior hole.
				missing = interiorMissing
			} else if interiorCount == 0 {
				// Case 2: complete run → hole is the base volume, before the run.
				missing = minNum - 1
			}
			// (interiorCount > 1 leaves missing = -1 → cannot place uniquely.)
			if missing >= 0 {
				// Assign the inferred number to the single unnumbered volume.
				for i := range entries {
					if !entries[i].hasNum {
						entries[i].num = missing
						entries[i].hasNum = true
						break
					}
				}
				p.logger.Debug().
					Int("inferred_volnum", missing).
					Msg("RAR volume order: inferred the one volume whose header lacked a volume number")
			} else {
				canOrder = false // can't uniquely place the hole
			}
		}
	}

	// Sort results by index to maintain NZB/upload order and collect files.
	// (Internal coordinate systems — baseSegments, volumeOffsetMap — are built
	// in this same NZB order by the caller, so we keep it here for consistency
	// and hand back VolumeOrder for the caller to reorder everything together.)
	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	if canOrder {
		// Build permutation: position k in true order ← the input index whose
		// volume number is the k-th smallest.
		sort.Slice(entries, func(i, j int) bool { return entries[i].num < entries[j].num })
		volumeOrder = make([]int, 0, len(entries))
		for _, e := range entries {
			volumeOrder = append(volumeOrder, e.idx)
		}
		// If the permutation is identity, leave it nil (nothing to reorder).
		identity := len(volumeOrder) == len(results)
		for k, idx := range volumeOrder {
			if k != idx {
				identity = false
				break
			}
		}
		if identity {
			volumeOrder = nil
		} else {
			p.logger.Debug().
				Int("volumes", len(volumeOrder)).
				Msg("RAR volume order recovered from RAR5 main-header volume numbers (differs from NZB order)")
		}
	}

	var allRawFiles []*RARFileEntry
	isHeaderEncrypted := false
	for _, result := range results {
		if result.err != nil {
			continue
		}
		if result.isHeaderEncrypted {
			isHeaderEncrypted = true
		}
		allRawFiles = append(allRawFiles, result.files...)
	}

	// If headers are encrypted, we can't list files without password
	if isHeaderEncrypted && len(allRawFiles) == 0 {
		return &RARArchiveInfo{
			Version:           version,
			IsMultiVol:        len(volumes) > 1,
			IsHeaderEncrypted: true,
			Files:             nil,
		}, nil
	}

	if len(allRawFiles) == 0 {
		return nil, fmt.Errorf("no files found in any RAR volume")
	}

	var encryptionKey []byte
	for _, result := range results {
		if len(result.encryptionKey) > 0 {
			encryptionKey = result.encryptionKey
			break
		}
	}

	// Aggregate file parts across volumes
	// Files that span multiple volumes will have multiple entries with the same name
	files := p.aggregateFileParts(allRawFiles)

	archiveInfo := &RARArchiveInfo{
		Version:           version,
		IsMultiVol:        len(volumes) > 1,
		IsHeaderEncrypted: isHeaderEncrypted,
		IsDataEncrypted:   len(encryptionKey) > 0,
		EncryptionKey:     encryptionKey,
		Files:             files,
		VolumeOrder:       volumeOrder,
	}
	return archiveInfo, nil
}

// detectRARVersion detects RAR version from signature
func detectRARVersion(data []byte) RARVersion {
	if len(data) >= 8 && bytes.Equal(data[:8], []byte(RAR5Signature)) {
		return RARVersion5
	}
	if len(data) >= 7 && bytes.Equal(data[:7], []byte(RAR4Signature)) {
		return RARVersion4
	}
	return RARVersionUnknown
}

// parseRAR5Headers parses RAR 5.0 format headers by reading sequentially through the archive
// This properly tracks offsets by reading headers and skipping data sections
func (p *RARParser) parseRAR5Headers(data []byte, volumeIndex int, volumeName string, password string) ([]*RARFileEntry, error) {
	r := bytes.NewReader(data)

	// Skip signature (8 bytes)
	if _, err := r.Seek(8, io.SeekStart); err != nil {
		return nil, err
	}

	var files []*RARFileEntry
	currentOffset := int64(8) // Current absolute position in the archive file

	for {
		// Save position before reading header
		headerStartOffset := currentOffset

		if r.Len() < 11 { // Minimum header size (CRC + vint sizes)
			break
		}

		header, headerSize, dataSize, err := p.readRAR5Header(r)
		if err != nil {
			// Any error reading headers means we've hit corrupt data or
			// reached beyond our snippet - stop parsing this volume
			if err == io.EOF || strings.Contains(err.Error(), "EOF") {
				break
			}
			// Only log unexpected errors (not snippet boundary issues)
			if !strings.Contains(err.Error(), "invalid header size") &&
				!strings.Contains(err.Error(), "too large") {
				p.logger.Debug().Err(err).Msg("Unexpected error reading RAR5 header")
			}
			break
		}

		// Data starts immediately after the header
		dataOffset := headerStartOffset + int64(headerSize)

		// Parse file headers
		if header.Type == RAR5HeaderTypeFile {
			file := p.parseRAR5FileHeader(header.Data, volumeIndex, volumeName, dataOffset, dataSize, password)
			if file != nil {
				files = append(files, file)
			}
		}

		// Move current offset past this header and its data
		currentOffset = dataOffset + dataSize

		// Try to skip the data area in the reader (if it fits in our snippet)
		if dataSize > 0 && r.Len() >= int(dataSize) {
			// Data is in our snippet, skip it
			if _, err := r.Seek(dataSize, io.SeekCurrent); err != nil {
				break
			}
		} else if dataSize > 0 {
			// Data extends beyond our snippet
			// We can't read more headers from this volume snippet
			break
		}

		// Stop at end of archive header
		if header.Type == RAR5HeaderTypeEndOfArc {
			break
		}
	}

	return files, nil
}

// rar5HeaderData represents a RAR 5.0 header
type rar5HeaderData struct {
	Type  uint64
	Flags uint64
	Data  []byte
}

// readRAR5Header reads a single RAR 5.0 header
func (p *RARParser) readRAR5Header(r *bytes.Reader) (*rar5HeaderData, int, int64, error) {
	startPos, _ := r.Seek(0, io.SeekCurrent)

	// Read header CRC (4 bytes)
	var headerCRC uint32
	if err := binary.Read(r, binary.LittleEndian, &headerCRC); err != nil {
		return nil, 0, 0, err
	}

	// Read header size (vint)
	headerSize, err := readVInt(r)
	if err != nil {
		return nil, 0, 0, err
	}
	// Sanity check: RAR5 headers should not exceed 64KB
	if headerSize > 65536 {
		return nil, 0, 0, fmt.Errorf("invalid RAR5 header size: %d (too large)", headerSize)
	}

	// Read header type (vint)
	headerType, err := readVInt(r)
	if err != nil {
		return nil, 0, 0, err
	}

	// Read header flags (vint)
	headerFlags, err := readVInt(r)
	if err != nil {
		return nil, 0, 0, err
	}

	// Read extra area size if present
	if headerFlags&RAR5HeaderFlagExtraArea != 0 {
		_, err = readVInt(r)
		if err != nil {
			return nil, 0, 0, err
		}
	}

	// Read data area size if present
	var dataAreaSize int64
	if headerFlags&RAR5HeaderFlagDataArea != 0 {
		dataSize, err := readVInt(r)
		if err != nil {
			return nil, 0, 0, err
		}
		dataAreaSize = int64(dataSize)
	}

	// Calculate remaining header data size based on actual bytes consumed
	currentPos, _ := r.Seek(0, io.SeekCurrent)
	consumedSize := int(currentPos-startPos) - 4 // Exclude CRC
	remainingHeaderSize := int(headerSize) - consumedSize

	// Read remaining header data
	// Protect against invalid size (corrupt/truncated data)
	if remainingHeaderSize < 0 || remainingHeaderSize > 1024*1024 {
		return nil, 0, 0, fmt.Errorf("invalid header size calculation: remaining=%d (headerSize=%d, consumed=%d)",
			remainingHeaderSize, headerSize, consumedSize)
	}

	var headerData []byte
	if remainingHeaderSize > 0 {
		headerData = make([]byte, remainingHeaderSize)
		if _, err := io.ReadFull(r, headerData); err != nil {
			return nil, 0, 0, err
		}
	}

	totalHeaderSize := int(headerSize) + 4 // Include CRC

	return &rar5HeaderData{
		Type:  headerType,
		Flags: headerFlags,
		Data:  headerData,
	}, totalHeaderSize, dataAreaSize, nil
}

// parseRAR5FileHeader parses a RAR 5.0 file header
// If password is provided and encryption salt is found, it derives the file-specific encryption key.
func (p *RARParser) parseRAR5FileHeader(data []byte, volumeIndex int, volumeName string, dataOffset int64, packedSize int64, password string) *RARFileEntry {
	r := bytes.NewReader(data)

	// Read file flags (vint)
	fileFlags, err := readVInt(r)
	if err != nil {
		return nil
	}

	// Validate file flags - RAR5 file flags use only bits 0-3:
	// 0x0001 = Directory, 0x0002 = Has Unix time, 0x0004 = Has CRC32,
	// 0x0008 = Unpacked size unknown
	// Values with other bits set indicate we're reading garbage data
	// (e.g., from continuation volumes that don't have file headers)
	if fileFlags > 0x0F {
		return nil
	}

	// Read unpacked size (vint)
	unpackedSize, err := readVInt(r)
	if err != nil {
		return nil
	}

	// Read file attributes (vint)
	if _, err := readVInt(r); err != nil {
		return nil
	}

	// Read modification time if present (4 bytes)
	if fileFlags&RAR5FileFlagHasUnixTime != 0 {
		_, _ = r.Seek(4, io.SeekCurrent)
	}

	// Read CRC32 if present
	var crc32 uint32
	if fileFlags&RAR5FileFlagHasCRC32 != 0 {
		if err := binary.Read(r, binary.LittleEndian, &crc32); err != nil {
			return nil
		}
	}

	// Read compression info (vint)
	compressionInfo, err := readVInt(r)
	if err != nil {
		return nil
	}

	// Read host OS (vint)
	if _, err := readVInt(r); err != nil {
		return nil
	}

	// Read name length (vint)
	nameLength, err := readVInt(r)
	if err != nil {
		return nil
	}

	// Sanity check: filename should not exceed 4KB
	if nameLength > 4096 {
		return nil
	}

	// Read filename
	nameBytes := make([]byte, nameLength)
	if _, err := io.ReadFull(r, nameBytes); err != nil {
		return nil
	}

	// Sanitize filename to ensure valid UTF-8
	// This prevents "string field contains invalid UTF-8" errors during NZB marshaling
	filename := strings.ToValidUTF8(string(nameBytes), "")

	// Validate filename - should be valid UTF-8 and not contain control characters
	// (except for null which shouldn't be in the name at all)
	for _, b := range nameBytes {
		if b < 0x20 && b != 0x09 && b != 0x0A && b != 0x0D { // Control chars except tab/newline
			return nil
		}
	}

	isDirectory := fileFlags&RAR5FileFlagDirectory != 0

	// If file has content, it's not a directory (heuristic to handle split files that might have 0x01 flag set improperly or confused)
	if unpackedSize > 0 {
		isDirectory = false
	}

	// Sanity check: if filename has a file extension, it's NOT a directory
	// This catches cases where garbage data has directory flag set incorrectly
	if isDirectory && len(filename) > 0 {
		if utils.IsMediaFile(filename) {
			isDirectory = false
		}
	}
	// RAR5 compression_info format:
	// - Bits 0-5 (0x003F): Algorithm version (0 or 1)
	// - Bit 6 (0x0040): Solid flag
	// - Bits 8-10 (0x0380): Compression method (0-5, where 0 = stored/no compression)
	// - Bits 11-15 (0x7C00): Dictionary size
	compressionMethod := (compressionInfo >> 8) & 0x07 // Extract bits 8-10
	isStored := compressionMethod == 0                 // Method 0 = no compression

	// Parse extra area if present (remaining bytes after filename)
	// Extra area contains encryption info, hash, etc.
	var isEncrypted bool
	var encryptionIV []byte
	var encryptionKey []byte

	if r.Len() > 0 {
		// Parse extra area records
		for r.Len() > 2 {
			// Read record size (vint)
			recordSize, err := readVInt(r)
			if err != nil || recordSize == 0 || recordSize > 65536 {
				break
			}

			// Read record type (vint)
			recordType, err := readVInt(r)
			if err != nil {
				break
			}

			// Calculate remaining data in this record
			recordDataSize := int(recordSize) - 1 // Minus the type byte (approximate)
			if recordDataSize <= 0 || recordDataSize > 65536 || recordDataSize > r.Len() {
				// Invalid or too large record - stop parsing extra area
				break
			}

			if recordType == RAR5ExtraTypeEncryption {
				// Encryption record format:
				// - Version (vint)
				// - Flags (vint)
				// - KDF count (1 byte)
				// - Salt (16 bytes, if UseAES256 flag set)
				// - IV (16 bytes)
				// - Check value (12 bytes, optional if flags & 0x01)
				isEncrypted = true

				// Read encryption version
				_, err := readVInt(r)
				if err != nil {
					break
				}

				// Read flags
				encFlags, err := readVInt(r)
				if err != nil {
					break
				}

				// Read KDF count (1 byte)
				kdfByte, err := r.ReadByte()
				if err != nil {
					break
				}
				kdfCount := int(kdfByte)

				// Read salt (16 bytes)
				salt := make([]byte, 16)
				if _, err := io.ReadFull(r, salt); err != nil {
					break
				}

				// If we have a password, derive the file-specific key using this salt
				if password != "" {
					derivedKeys := crypto.DeriveKeys([]byte(password), salt, kdfCount)
					encryptionKey = derivedKeys.Key
				}

				// Read IV (16 bytes) - THIS IS WHAT WE NEED
				iv := make([]byte, 16)
				if _, err := io.ReadFull(r, iv); err != nil {
					break
				}
				encryptionIV = iv

				// Skip check value if present (flags & 0x01 = has check)
				if encFlags&0x01 != 0 {
					checkValue := make([]byte, 12)
					_, _ = io.ReadFull(r, checkValue)
				}
			} else {
				// Skip other record types
				skipData := make([]byte, recordDataSize)
				if _, err := io.ReadFull(r, skipData); err != nil {
					break
				}
			}
		}
	}

	return &RARFileEntry{
		Name:             strings.ToValidUTF8(string(nameBytes), ""),
		UncompressedSize: int64(unpackedSize),
		PackedSize:       packedSize,
		DataOffset:       dataOffset,
		IsStored:         isStored,
		IsDirectory:      isDirectory,
		IsEncrypted:      isEncrypted,
		EncryptionKey:    encryptionKey,
		EncryptionIV:     encryptionIV,
		CRC32:            crc32,
		VolumeIndex:      volumeIndex,
		VolumeParts: []*types.RARVolumePart{{
			Name:         volumeName,
			DataOffset:   dataOffset,
			PackedSize:   packedSize,
			UnpackedSize: packedSize, // Use PackedSize - represents data IN THIS VOLUME PART, not full file
			Stored:       isStored,
			PartNumber:   volumeIndex, // Set part number to volume index
		}},
	}
}

// parseRAR4Headers parses RAR 4.x format headers
func (p *RARParser) parseRAR4Headers(data []byte, volumeIndex int, volumeName string) ([]*RARFileEntry, error) {
	r := bytes.NewReader(data)

	// Skip marker block (7 bytes signature + marker header)
	if _, err := r.Seek(7, io.SeekStart); err != nil {
		return nil, err
	}

	var files []*RARFileEntry
	currentOffset := int64(7)

	// 7 = minimum RAR4 block size (CRC2 + Type1 + Flags2 + HeadSize2).
	// Continue while at least one minimal header may remain.
	for r.Len() >= 7 {
		header, err := p.readRAR4Header(r)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		if header.Type == RAR4HeaderTypeFile {
			file := p.parseRAR4FileHeader(header, volumeIndex, volumeName, currentOffset+int64(header.HeadSize))
			if file != nil {
				files = append(files, file)
			}
		}

		// Move to next header
		nextOffset := currentOffset + int64(header.HeadSize) + int64(header.AddSize)
		if nextOffset <= currentOffset {
			break
		}

		// Check if we have enough data in our snippet to continue
		if nextOffset > int64(len(data)) {
			// We've reached the actual file data which is beyond our snippet
			// This is fine - we have the header info we need
			break
		}

		currentOffset = nextOffset

		if _, err := r.Seek(currentOffset, io.SeekStart); err != nil {
			break
		}

		if header.Type == RAR4HeaderTypeEnd {
			break
		}
	}

	return files, nil
}

// rar4Header represents a RAR 4.x header
type rar4Header struct {
	CRC      uint16
	Type     uint8
	Flags    uint16
	HeadSize uint16
	AddSize  uint32
	Data     []byte
}

// readRAR4Header reads a single RAR 4.x header
func (p *RARParser) readRAR4Header(r *bytes.Reader) (*rar4Header, error) {
	var header rar4Header

	// Read header CRC (2 bytes)
	if err := binary.Read(r, binary.LittleEndian, &header.CRC); err != nil {
		return nil, err
	}

	// Read header type (1 byte)
	if err := binary.Read(r, binary.LittleEndian, &header.Type); err != nil {
		return nil, err
	}

	// Read header flags (2 bytes)
	if err := binary.Read(r, binary.LittleEndian, &header.Flags); err != nil {
		return nil, err
	}

	// Read header size (2 bytes)
	if err := binary.Read(r, binary.LittleEndian, &header.HeadSize); err != nil {
		return nil, err
	}

	// Read additional size (4 bytes) if LONG_BLOCK flag is set
	if header.Flags&RAR4HeaderFlagLongBlock != 0 {
		if err := binary.Read(r, binary.LittleEndian, &header.AddSize); err != nil {
			return nil, err
		}
	} else {
		// For non-long blocks, AddSize is 16-bit
		var addSize16 uint16
		if header.HeadSize > 7 {
			if err := binary.Read(r, binary.LittleEndian, &addSize16); err != nil {
				return nil, err
			}
		}
		header.AddSize = uint32(addSize16)
	}

	// Read remaining header data
	baseHeaderSize := 7 // CRC(2) + Type(1) + Flags(2) + HeadSize(2)
	if header.Flags&RAR4HeaderFlagLongBlock != 0 {
		baseHeaderSize += 4
	} else if header.HeadSize > 7 {
		baseHeaderSize += 2
	}

	remainingSize := int(header.HeadSize) - baseHeaderSize
	if remainingSize > 0 {
		header.Data = make([]byte, remainingSize)
		if _, err := io.ReadFull(r, header.Data); err != nil {
			return nil, err
		}
	}

	return &header, nil
}

// parseRAR4FileHeader parses RAR 4.x file header
func (p *RARParser) parseRAR4FileHeader(header *rar4Header, volumeIndex int, volumeName string, dataOffset int64) *RARFileEntry {
	if len(header.Data) < 21 { // Minimum file header data size
		return nil
	}

	r := bytes.NewReader(header.Data)

	// Read packed size (4 bytes low word)
	var packedSizeLow uint32
	_ = binary.Read(r, binary.LittleEndian, &packedSizeLow)

	// Read unpacked size (4 bytes low word)
	var unpackedSizeLow uint32
	_ = binary.Read(r, binary.LittleEndian, &unpackedSizeLow)

	// Read host OS (1 byte)
	_, _ = r.Seek(1, io.SeekCurrent)

	// Read file CRC (4 bytes)
	var crc32 uint32
	_ = binary.Read(r, binary.LittleEndian, &crc32)

	// Read file time (4 bytes)
	_, _ = r.Seek(4, io.SeekCurrent)

	// Read RAR version (1 byte)
	_, _ = r.Seek(1, io.SeekCurrent)

	// Read compression method (1 byte)
	var method uint8
	_ = binary.Read(r, binary.LittleEndian, &method)

	// Read name length (2 bytes)
	var nameLength uint16
	_ = binary.Read(r, binary.LittleEndian, &nameLength)

	// Read file attributes (4 bytes)
	_, _ = r.Seek(4, io.SeekCurrent)

	// Handle HIGH_SIZE flag - read high 32 bits of sizes
	var packedSize, unpackedSize int64
	if header.Flags&RAR4FileFlagHighSize != 0 {
		// Read high 4 bytes of packed size
		var packedSizeHigh uint32
		_ = binary.Read(r, binary.LittleEndian, &packedSizeHigh)
		// Read high 4 bytes of unpacked size
		var unpackedSizeHigh uint32
		_ = binary.Read(r, binary.LittleEndian, &unpackedSizeHigh)

		packedSize = int64(packedSizeHigh)<<32 | int64(packedSizeLow)
		unpackedSize = int64(unpackedSizeHigh)<<32 | int64(unpackedSizeLow)
	} else {
		packedSize = int64(packedSizeLow)
		unpackedSize = int64(unpackedSizeLow)
	}

	// Read filename
	var nameBytes []byte
	if nameLength > 0 {
		nameBytes = make([]byte, nameLength)
		if _, err := io.ReadFull(r, nameBytes); err != nil {
			return nil
		}
	} else {
		// Fallback: nameLength=0 but there might be a filename at different offset
		// This handles 7z-embedded RAR files where header parsing offsets differ
		// Try to find ASCII filename by backing up bytes and scanning
		pos, _ := r.Seek(0, io.SeekCurrent)
		if pos >= 4 {
			// Check if filename is 4 bytes earlier (before attrs field)
			checkPos := pos - 4
			if checkPos < int64(len(header.Data)) && header.Data[checkPos] >= 0x20 && header.Data[checkPos] < 0x7F {
				// Found ASCII at -4, scan for filename
				end := checkPos
				for end < int64(len(header.Data)) && header.Data[end] >= 0x20 && header.Data[end] < 0x7F {
					end++
				}
				if end > checkPos {
					nameBytes = header.Data[checkPos:end]
				}
			}
		}
	}

	// Check if directory
	isDirectory := (header.Flags & RAR4FileFlagDirectory) == RAR4FileFlagDirectory

	// Check if stored (method 0x30)
	// User report: Method 0x81 (Compressed) files play as raw streams, suggesting they are effectively stored
	// or the player handles the compression. To support seeking, we must treat them as stored
	// and ensure UncompressedSize matches PackedSize so we don't advertise data we can't serve.
	isStored := method == RAR4CompressionMethodStore || method == 0x81

	if isStored && method != RAR4CompressionMethodStore {
		unpackedSize = packedSize
	}

	return &RARFileEntry{
		Name:             strings.ToValidUTF8(string(nameBytes), ""),
		UncompressedSize: unpackedSize,
		PackedSize:       packedSize,
		DataOffset:       dataOffset,
		IsStored:         isStored,
		IsDirectory:      isDirectory,
		CRC32:            crc32,
		VolumeIndex:      volumeIndex,
		VolumeParts: []*types.RARVolumePart{{
			Name:         volumeName,
			DataOffset:   dataOffset,
			PackedSize:   packedSize,
			UnpackedSize: packedSize, // Use PackedSize - represents data IN THIS VOLUME PART, not full file
			Stored:       isStored,
			PartNumber:   volumeIndex, // Set part number to volume index
		}},
	}
}

// buildVolumeOffsetMap builds a map of cumulative offsets for each volume
func buildVolumeOffsetMap(volumeInfos []storage.ArchiveVolumeInfo) map[int]int64 {
	offsetMap := make(map[int]int64)
	var cumulativeOffset int64

	for i, volInfo := range volumeInfos {
		offsetMap[i] = cumulativeOffset
		cumulativeOffset += volInfo.Size
	}

	return offsetMap
}

// buildSegmentsForFile builds the segment list for a file across all its RAR volume parts
// CRITICAL: part.DataOffset is the offset WITHIN the decoded RAR volume file
// We need to map this to the actual NNTP segment that contains that byte
func (p *RARParser) buildSegmentsForFile(
	rarFile *RARFileEntry,
	baseSegments []storage.NZBSegment,
	volumeOffsetMap map[int]int64,
) ([]storage.NZBSegment, error) {
	if len(rarFile.VolumeParts) == 0 {
		return nil, fmt.Errorf("no volume parts for file %s", rarFile.Name)
	}

	// Ensure volume parts are ordered by volume index and data offset.
	partsSorted := true
	for i := 1; i < len(rarFile.VolumeParts); i++ {
		prev := rarFile.VolumeParts[i-1]
		cur := rarFile.VolumeParts[i]
		if cur.PartNumber < prev.PartNumber ||
			(cur.PartNumber == prev.PartNumber && cur.DataOffset < prev.DataOffset) {
			partsSorted = false
			break
		}
	}
	if !partsSorted {
		sort.Slice(rarFile.VolumeParts, func(i, j int) bool {
			if rarFile.VolumeParts[i].PartNumber == rarFile.VolumeParts[j].PartNumber {
				return rarFile.VolumeParts[i].DataOffset < rarFile.VolumeParts[j].DataOffset
			}
			return rarFile.VolumeParts[i].PartNumber < rarFile.VolumeParts[j].PartNumber
		})
	}

	var fileSegments []storage.NZBSegment
	var currentFileOffset int64 // Offset within the final extracted file

	// Parse each volume part of this file
	for _, part := range rarFile.VolumeParts {
		if part.UnpackedSize <= 0 {
			continue
		}

		// Build segments for this specific part
		// part.DataOffset = offset within the RAR volume where this file's data starts
		// part.UnpackedSize = how many bytes of the file are in this part (this is what we stream!)
		partSegments, err := p.buildSegmentsForVolumePart(part, baseSegments, volumeOffsetMap)
		if err != nil {
			continue
		}

		if len(partSegments) == 0 {
			continue
		}

		// Ensure part segments are ordered by their segment number.
		sort.Slice(partSegments, func(i, j int) bool {
			return partSegments[i].Number < partSegments[j].Number
		})

		// Append with correct file offsets
		for i := range partSegments {
			partSegments[i].StartOffset = currentFileOffset
			partSegments[i].EndOffset = currentFileOffset + partSegments[i].Bytes - 1
			currentFileOffset += partSegments[i].Bytes
		}
		fileSegments = append(fileSegments, partSegments...)
	}

	if len(fileSegments) == 0 {
		return nil, fmt.Errorf("no segments built for file %s", rarFile.Name)
	}

	// Ensure segments are ordered by output offsets for streaming correctness.
	ordered := true
	for i := 1; i < len(fileSegments); i++ {
		if fileSegments[i].StartOffset < fileSegments[i-1].StartOffset {
			ordered = false
			break
		}
	}
	if !ordered {
		sort.Slice(fileSegments, func(i, j int) bool {
			return fileSegments[i].StartOffset < fileSegments[j].StartOffset
		})
	}

	return fileSegments, nil
}

// buildSegmentsForVolumePart builds segments for a single RAR volume part
// Maps the DataOffset within the decoded RAR volume to actual NNTP segments
// CRITICAL: We calculate two different offsets:
//   - SegmentDataStart: where within the decoded NNTP segment to start reading
//   - StartOffset/EndOffset: set later in buildSegmentsForFile as file output positions
func (p *RARParser) buildSegmentsForVolumePart(
	part *types.RARVolumePart,
	baseSegments []storage.NZBSegment,
	volumeOffsetMap map[int]int64,
) ([]storage.NZBSegment, error) {
	// get the absolute byte offset where this volume starts in the flat segment list
	volumeStartOffset, ok := volumeOffsetMap[part.PartNumber]
	if !ok {
		return nil, fmt.Errorf("volume offset not found for part number %d", part.PartNumber)
	}

	// Calculate absolute offset in the flat segment space
	// volumeStartOffset = cumulative size of all previous volumes
	// part.DataOffset = offset within THIS volume where the file data starts
	absoluteStartOffset := volumeStartOffset + part.DataOffset
	absoluteEndOffset := absoluteStartOffset + part.UnpackedSize - 1

	// Find all segments that overlap with [absoluteStartOffset, absoluteEndOffset]
	var result []storage.NZBSegment
	var currentOffset int64

	for _, seg := range baseSegments {
		segStart := currentOffset
		segEnd := currentOffset + seg.Bytes - 1

		// Check if this segment overlaps with our data range
		if segEnd < absoluteStartOffset {
			// Segment is entirely before our data
			currentOffset += seg.Bytes
			continue
		}

		if segStart > absoluteEndOffset {
			// Segment is entirely after our data
			break
		}

		// Calculate the overlap
		overlapStart := max(segStart, absoluteStartOffset)
		overlapEnd := min(segEnd, absoluteEndOffset)

		// Calculate offset within the NNTP segment where we start reading
		segmentDataStart := overlapStart - segStart

		// Bytes we'll read from this segment
		bytesToRead := overlapEnd - overlapStart + 1

		// Create a new segment descriptor
		// StartOffset/EndOffset will be set by buildSegmentsForFile as file output positions
		slicedSegment := storage.NZBSegment{
			Number:           seg.Number,
			MessageID:        seg.MessageID,
			Bytes:            bytesToRead,
			SegmentDataStart: segmentDataStart, // Where to start reading within this NNTP segment
			Group:            seg.Group,
			// StartOffset/EndOffset left as 0 - will be set by caller
		}

		result = append(result, slicedSegment)

		if overlapEnd == absoluteEndOffset {
			// We've covered the entire range
			break
		}

		currentOffset += seg.Bytes
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no segments found for range [%d, %d]", absoluteStartOffset, absoluteEndOffset)
	}

	return result, nil
}

// sliceSegmentsForRangeSimple extracts segments covering [offset, offset+length)
// This is a simplified version that works directly with the flat segment list
func sliceSegmentsForRangeSimple(
	baseSegments []storage.NZBSegment,
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

	var result []storage.NZBSegment
	var currentPos int64
	var outputPos int64 // Track position in the OUTPUT file (the extracted file)

	// Scan through segments to find overlapping ones
	for _, seg := range baseSegments {
		segStart := currentPos
		segEnd := currentPos + seg.Bytes - 1

		// Check if this segment overlaps with our target range
		if segEnd < targetStart {
			// Segment is before our range
			currentPos += seg.Bytes
			continue
		}

		if segStart > targetEnd {
			// Segment is after our range, we're done
			break
		}

		// Calculate the overlap
		overlapStart := max(segStart, targetStart)

		overlapEnd := min(segEnd, targetEnd)

		// Calculate segment-relative offsets
		// relStart = where to start reading within this NNTP segment's decoded data
		relStart := overlapStart - segStart

		// Bytes to read from this segment
		bytesToRead := overlapEnd - overlapStart + 1

		// Create sliced segment
		slicedSeg := storage.NZBSegment{
			Number:           seg.Number,
			MessageID:        seg.MessageID,
			Bytes:            bytesToRead,
			StartOffset:      outputPos,                   // Position in the OUTPUT file
			EndOffset:        outputPos + bytesToRead - 1, // End position in OUTPUT file
			Group:            seg.Group,
			SegmentDataStart: relStart, // Where to start reading within this NNTP segment
		}

		result = append(result, slicedSeg)
		outputPos += bytesToRead

		if overlapEnd == targetEnd {
			// We've covered the entire range
			return result, nil
		}

		currentPos += seg.Bytes
	}

	return result, nil
}

// aggregateFileParts combines file parts across volumes for multi-volume RAR archives
// When a file spans multiple RAR volumes, each volume contains a file header for the continuation
// This function merges these into a single RARFileEntry with all volume parts
func (p *RARParser) aggregateFileParts(rawFiles []*RARFileEntry) []*RARFileEntry {
	if len(rawFiles) == 0 {
		return nil
	}

	// Map of filename -> aggregated file entry
	fileMap := make(map[string]*RARFileEntry)

	for _, file := range rawFiles {
		if file == nil {
			continue
		}

		existing, found := fileMap[file.Name]
		if !found {
			// First occurrence of this file
			fileMap[file.Name] = file
		} else {
			// File continuation from another volume - merge the parts
			if len(file.VolumeParts) > 0 {
				// Append volume parts (PartNumber already set correctly during parsing)
				existing.VolumeParts = append(existing.VolumeParts, file.VolumeParts...)
				// Update total packed size
				existing.PackedSize += file.PackedSize
			}
		}
	}

	// Convert map back to slice
	result := make([]*RARFileEntry, 0, len(fileMap))
	for _, file := range fileMap {
		result = append(result, file)
	}

	// Sort by name for consistent ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	return result
}
