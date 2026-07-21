package parser

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/sirrobot01/decypharr/internal/crypto"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

// rarReader provides a continuous stream across RAR volumes
// It can efficiently skip large data sections without downloading them
type rarReader struct {
	ctx      context.Context
	manager  *nntp.Client
	volumes  []*types.Volume
	position int64 // Current absolute position in the archive

	currentVolumeIndex   int
	currentSegmentIndex  int
	currentSegmentData   []byte
	currentSegmentOffset int // Offset within current segment data
}

func newRarReader(ctx context.Context, manager *nntp.Client, volumes []*types.Volume) *rarReader {
	return &rarReader{
		ctx:                 ctx,
		manager:             manager,
		volumes:             volumes,
		position:            0,
		currentVolumeIndex:  0,
		currentSegmentIndex: 0,
	}
}

// Read implements io.Reader
func (r *rarReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	totalRead := 0

	for totalRead < len(p) {
		// Ensure we have current segment data
		if r.currentSegmentData == nil || r.currentSegmentOffset >= len(r.currentSegmentData) {
			if err := r.loadNextSegment(); err != nil {
				if totalRead > 0 {
					return totalRead, nil
				}
				return 0, err
			}
		}

		// Copy from current segment
		n := copy(p[totalRead:], r.currentSegmentData[r.currentSegmentOffset:])
		r.currentSegmentOffset += n
		r.position += int64(n)
		totalRead += n
	}

	return totalRead, nil
}

// Skip efficiently skips n bytes without downloading unnecessary data
func (r *rarReader) Skip(n int64) error {
	if n <= 0 {
		return nil
	}

	// Fast path: skip within current segment if possible
	if r.currentSegmentData != nil {
		remaining := len(r.currentSegmentData) - r.currentSegmentOffset
		if int64(remaining) >= n {
			r.currentSegmentOffset += int(n)
			r.position += n
			return nil
		}
		// Skip rest of current segment
		r.position += int64(remaining)
		n -= int64(remaining)
		r.currentSegmentData = nil
	}

	// Skip entire segments without downloading
	for n > 0 {
		if r.currentVolumeIndex >= len(r.volumes) {
			return io.EOF
		}

		volume := r.volumes[r.currentVolumeIndex]
		if r.currentSegmentIndex >= len(volume.Segments) {
			// Move to next volume
			r.currentVolumeIndex++
			r.currentSegmentIndex = 0
			continue
		}

		segment := volume.Segments[r.currentSegmentIndex]
		segmentSize := segment.Bytes

		if n >= segmentSize {
			// Skip entire segment
			r.currentSegmentIndex++
			r.position += segmentSize
			n -= segmentSize
		} else {
			// Need to load this segment and skip within it
			if err := r.loadNextSegment(); err != nil {
				return err
			}
			r.currentSegmentOffset = int(n)
			r.position += n
			n = 0
		}
	}

	return nil
}

// loadNextSegment loads the next segment's data
func (r *rarReader) loadNextSegment() error {
	for {
		if r.currentVolumeIndex >= len(r.volumes) {
			return io.EOF
		}

		volume := r.volumes[r.currentVolumeIndex]
		if r.currentSegmentIndex >= len(volume.Segments) {
			// Move to next volume
			r.currentVolumeIndex++
			r.currentSegmentIndex = 0
			continue
		}

		segment := volume.Segments[r.currentSegmentIndex]

		// Download segment using manager
		var data []byte
		err := r.manager.ExecuteWithFailover(r.ctx, func(conn *nntp.Connection) error {
			d, e := conn.GetDecodedBody(segment.MessageID)
			data = d
			return e
		})
		if err != nil {
			return fmt.Errorf("failed to fetch segment: %w", err)
		}

		r.currentSegmentData = data
		r.currentSegmentOffset = 0
		r.currentSegmentIndex++
		return nil
	}
}

// Position returns the current position in the stream
func (r *rarReader) Position() int64 {
	return r.position
}

// AbsoluteToVolumeOffset converts an absolute position in the stream to (volumeIndex, offsetWithinVolume)
func (r *rarReader) AbsoluteToVolumeOffset(absolutePos int64) (volumeIndex int, offsetInVolume int64) {
	currentPos := int64(0)

	for volIdx, volume := range r.volumes {
		// Calculate total size of this volume (sum of all segment sizes)
		volumeSize := int64(0)
		for _, segment := range volume.Segments {
			volumeSize += segment.Bytes
		}

		// Check if the position falls within this volume
		if absolutePos < currentPos+volumeSize {
			// Position is in this volume
			offsetInVol := absolutePos - currentPos
			return volIdx, offsetInVol
		}

		currentPos += volumeSize
	}

	// If we get here, position is beyond all volumes
	// Return last volume and offset
	if len(r.volumes) > 0 {
		return len(r.volumes) - 1, absolutePos
	}
	return 0, absolutePos
}

// parseRAR5StreamResult contains the result of parsing a RAR5 stream
type parseRAR5StreamResult struct {
	Files             []*RARFileEntry
	IsHeaderEncrypted bool
	EncryptionKey     []byte // AES key for file data decryption (if encrypted)
	EncryptionIV      []byte // AES IV for file data decryption (if encrypted)
	// VolumeNumber is the 0-based volume number read from this volume's RAR5
	// main archive header (present when the archive-flags volume-number bit is
	// set). HasVolumeNumber reports whether it was successfully parsed. These
	// let the caller order volumes by their TRUE position rather than by NZB
	// upload order, which is wrong for obfuscated multi-volume archives whose
	// per-volume filenames carry no .partNN ordering hint.
	VolumeNumber    int
	HasVolumeNumber bool
}

// parseRAR5MainVolumeNumber extracts the 0-based volume number from a RAR5 main
// archive header's data. RAR5 main header layout (after the common
// CRC/size/type/flags fields already stripped into header.Data): ArchiveFlags
// (vint), then VolumeNumber (vint) IFF ArchiveFlags has the volume-number bit
// (RAR5MainFlagVolumeNumber). Returns (number, true) only when the bit is set
// and the vint parses; otherwise (0, false) so the caller falls back to NZB
// order rather than trusting a guess.
func parseRAR5MainVolumeNumber(data []byte) (int, bool) {
	r := bytes.NewReader(data)
	archiveFlags, err := readVInt(r)
	if err != nil {
		return 0, false
	}
	if archiveFlags&RAR5MainFlagVolumeNumber == 0 {
		return 0, false
	}
	volNum, err := readVInt(r)
	if err != nil {
		return 0, false
	}
	return int(volNum), true
}

// parseRAR5Stream parses RAR 5.0 headers from a stream reader
// This properly tracks offsets by reading headers sequentially and skipping data
// If password is provided and headers are encrypted, it will decrypt them
func (p *RARParser) parseRAR5Stream(stream *rarReader, volumeIndex int, volumeName string, password string) (*parseRAR5StreamResult, error) {
	result := &parseRAR5StreamResult{
		Files: make([]*RARFileEntry, 0),
	}

	var encryptionKey []byte // Key for decrypting file data

	// Stream position is already at 8 (after signature)
	for {
		// Record position before reading header
		headerStartPos := stream.Position()

		// Read header
		header, headerSize, dataSize, err := p.readRAR5HeaderFromStream(stream)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		// Check for encryption header - this means headers are encrypted
		if header.Type == RAR5HeaderTypeEncrypt {
			result.IsHeaderEncrypted = true

			// Parse encryption header to get salt and kdfCount
			encHeader, err := crypto.ParseEncryptionHeader(header.Data)
			if err != nil {
				break
			}

			// If no password provided, we can't continue
			if password == "" {
				break
			}

			// Derive key from password
			keys := crypto.DeriveKeys([]byte(password), encHeader.Salt, encHeader.KdfCount)

			// Verify password if check is present
			if encHeader.HasPwCheck {
				if !crypto.VerifyPassword(keys, encHeader.PwCheck) {
					return nil, crypto.ErrBadPassword
				}
			}

			// Store the encryption key for file data decryption
			encryptionKey = keys.Key
			result.EncryptionKey = keys.Key

			// Now we need to read encrypted headers
			// Each encrypted header is: 16-byte IV + encrypted data (aligned to 16 bytes)
			// Continue parsing with decryption enabled
			for {
				// Read IV (16 bytes)
				iv := make([]byte, crypto.BlockSize)
				if _, err := io.ReadFull(stream, iv); err != nil {
					if err == io.EOF {
						break
					}
					break
				}
				result.EncryptionIV = iv

				// Read encrypted header
				encHeader, encHeaderSize, encDataSize, err := p.readAndDecryptRAR5Header(stream, encryptionKey, iv)
				if err != nil {
					if err == io.EOF {
						break
					}
					break
				}

				// Parse the decrypted header
				if encHeader.Type == RAR5HeaderTypeFile {
					headerPos := stream.Position() - int64(encHeaderSize)
					_, offsetInVol := stream.AbsoluteToVolumeOffset(headerPos + int64(encHeaderSize))

					file := p.parseRAR5FileHeader(encHeader.Data, volumeIndex, volumeName, offsetInVol, encDataSize, password)
					if file != nil {
						// Note: file.IsEncrypted is now set correctly from extra area parsing
						// Headers being encrypted does NOT mean data is encrypted
						result.Files = append(result.Files, file)
					}
				}

				// Skip data section
				if encDataSize > 0 {
					// Data is also encrypted, need to account for padding
					paddedSize := ((encDataSize + crypto.BlockSize - 1) / crypto.BlockSize) * crypto.BlockSize
					if err := stream.Skip(paddedSize); err != nil {
						if err == io.EOF {
							break
						}
						break
					}
				}

				if encHeader.Type == RAR5HeaderTypeEndOfArc {
					break
				}
			}
			break
		}

		// Data offset is immediately after the header (absolute position in stream)
		dataOffsetAbsolute := headerStartPos + int64(headerSize)

		// Main archive header: extract the true volume number if present, so the
		// caller can order volumes correctly even when NZB/upload order doesn't
		// match volume order (obfuscated multi-volume archives).
		if header.Type == RAR5HeaderTypeMain {
			if vn, ok := parseRAR5MainVolumeNumber(header.Data); ok {
				result.VolumeNumber = vn
				result.HasVolumeNumber = true
			}
		}

		// Parse file headers
		if header.Type == RAR5HeaderTypeFile {
			_, offsetInVol := stream.AbsoluteToVolumeOffset(dataOffsetAbsolute)

			// Use the volumeIndex parameter passed to this function, not the stream's volume index
			// because each stream only contains one volume
			file := p.parseRAR5FileHeader(header.Data, volumeIndex, volumeName, offsetInVol, dataSize, password)
			if file != nil {
				result.Files = append(result.Files, file)
			}
		}

		// Skip the data section to get to the next header
		if dataSize > 0 {
			if err := stream.Skip(dataSize); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("failed to skip data section: %w", err)
			}
		}

		// Stop at end of archive
		if header.Type == RAR5HeaderTypeEndOfArc {
			break
		}
	}

	return result, nil
}

// readAndDecryptRAR5Header reads an encrypted RAR5 header from stream
func (p *RARParser) readAndDecryptRAR5Header(stream *rarReader, key, iv []byte) (*rar5HeaderData, int, int64, error) {
	// For encrypted headers, we need to read enough data and decrypt
	// The header starts with encrypted CRC+size+type+flags...
	// We read in blocks and decrypt

	// Read first block to get header size
	firstBlock := make([]byte, crypto.BlockSize)
	if _, err := io.ReadFull(stream, firstBlock); err != nil {
		return nil, 0, 0, err
	}

	// Save ciphertext for IV chaining
	firstBlockCipher := make([]byte, len(firstBlock))
	copy(firstBlockCipher, firstBlock)

	// Decrypt first block
	if err := crypto.DecryptBlock(firstBlock, key, iv); err != nil {
		return nil, 0, 0, err
	}

	// Now parse the decrypted data as a normal header
	// Skip CRC (4 bytes), read size
	if len(firstBlock) < 5 {
		return nil, 0, 0, fmt.Errorf("decrypted block too small")
	}

	// Read header size from decrypted data
	r := bytes.NewReader(firstBlock[4:])
	headerSize, err := readVInt(r)
	if err != nil {
		return nil, 0, 0, err
	}

	// Calculate how many more blocks we need
	// Sanity check: limit header size to 64KB to prevent OOM from corrupt decrypted data
	if headerSize > 65536 {
		return nil, 0, 0, fmt.Errorf("encrypted header size too large: %d", headerSize)
	}

	totalEncryptedSize := int(headerSize) + 4 // header + CRC
	totalEncryptedSize = ((totalEncryptedSize + crypto.BlockSize - 1) / crypto.BlockSize) * crypto.BlockSize

	if totalEncryptedSize > crypto.BlockSize {
		// Read remaining blocks
		remaining := make([]byte, totalEncryptedSize-crypto.BlockSize)
		if _, err := io.ReadFull(stream, remaining); err != nil {
			return nil, 0, 0, err
		}

		// Create new IV for CBC continuation (last ciphertext block)
		newIV := firstBlockCipher[len(firstBlockCipher)-crypto.BlockSize:]
		if err := crypto.DecryptBlock(remaining, key, newIV); err != nil {
			return nil, 0, 0, err
		}

		firstBlock = append(firstBlock, remaining...)
	}

	// Parse the decrypted header data
	r = bytes.NewReader(firstBlock[4:]) // Skip CRC

	// Read header size again
	headerSize, vintBytes, _, err := readVIntFromReaderWithBytes(r)
	if err != nil {
		return nil, 0, 0, err
	}

	headerType, n, err := readVIntFromReader(r)
	if err != nil {
		return nil, 0, 0, err
	}

	headerFlags, n2, err := readVIntFromReader(r)
	if err != nil {
		return nil, 0, 0, err
	}

	bytesConsumed := vintBytes + n + n2

	// Read extra area size if present
	if headerFlags&RAR5HeaderFlagExtraArea != 0 {
		_, n, err = readVIntFromReader(r)
		if err != nil {
			return nil, 0, 0, err
		}
		bytesConsumed += n
	}

	// Read data area size if present
	var dataAreaSize int64
	if headerFlags&RAR5HeaderFlagDataArea != 0 {
		dataSize, n, err := readVIntFromReader(r)
		if err != nil {
			return nil, 0, 0, err
		}
		bytesConsumed += n
		dataAreaSize = int64(dataSize)
	}

	// Read remaining header data
	remainingSize := int(headerSize) - bytesConsumed
	var headerData []byte
	if remainingSize > 0 {
		headerData = make([]byte, remainingSize)
		if _, err := io.ReadFull(r, headerData); err != nil {
			return nil, 0, 0, err
		}
	}

	return &rar5HeaderData{
		Type:  headerType,
		Flags: headerFlags,
		Data:  headerData,
	}, totalEncryptedSize, dataAreaSize, nil
}

// readRAR5HeaderFromStream reads a RAR5 header from the stream
// Optimized: reads header content in one call after getting header size
func (p *RARParser) readRAR5HeaderFromStream(stream *rarReader) (*rar5HeaderData, int, int64, error) {
	// Read header CRC (4 bytes) + first few bytes that contain the header size vint
	// We read a small initial buffer to get the CRC and header size
	initialBuf := make([]byte, 16)                // 4 bytes CRC + up to 10 bytes vint + some extra
	n, err := io.ReadFull(stream, initialBuf[:5]) // Read CRC + at least 1 byte of size
	if err != nil {
		return nil, 0, 0, err
	}

	// Parse CRC (first 4 bytes) - we don't verify it, just skip
	pos := 4

	// Read header size vint from buffer, continuing to read more if needed
	headerSize, vintBytes := parseVIntFromBuffer(initialBuf[pos:n])
	if vintBytes == 0 {
		// Need more bytes for the vint - rare case for large headers
		for vintBytes == 0 && n < len(initialBuf) {
			extra, err := stream.Read(initialBuf[n : n+1])
			if err != nil {
				return nil, 0, 0, err
			}
			n += extra
			headerSize, vintBytes = parseVIntFromBuffer(initialBuf[pos:n])
		}
		if vintBytes == 0 {
			return nil, 0, 0, fmt.Errorf("failed to read header size vint")
		}
	}

	headerSizeVintBytes := vintBytes
	pos += vintBytes

	// Sanity check
	if headerSize > 65536 {
		return nil, 0, 0, fmt.Errorf("invalid RAR5 header size: %d (too large)", headerSize)
	}

	// Now read the entire remaining header content in one go
	// headerSize is size starting from header type field
	headerContent := make([]byte, int(headerSize))

	// Copy any bytes we already read past the size vint
	alreadyRead := n - pos
	if alreadyRead > 0 {
		copy(headerContent, initialBuf[pos:n])
	}

	// Read the rest if needed
	if alreadyRead < int(headerSize) {
		_, err := io.ReadFull(stream, headerContent[alreadyRead:])
		if err != nil {
			return nil, 0, 0, err
		}
	}

	// Now parse the header content from memory (no more Read calls!)
	reader := bytes.NewReader(headerContent)
	bytesConsumed := 0

	// Read header type (vint)
	headerType, err := readVInt(reader)
	if err != nil {
		return nil, 0, 0, err
	}
	_ = int(reader.Size()) - reader.Len()

	// Read header flags (vint)
	headerFlags, err := readVInt(reader)
	if err != nil {
		return nil, 0, 0, err
	}
	bytesConsumed = int(reader.Size()) - reader.Len()

	// Read extra area size if present
	if headerFlags&RAR5HeaderFlagExtraArea != 0 {
		_, err = readVInt(reader)
		if err != nil {
			return nil, 0, 0, err
		}
		bytesConsumed = int(reader.Size()) - reader.Len()
	}

	// Read data area size if present
	var dataAreaSize int64
	if headerFlags&RAR5HeaderFlagDataArea != 0 {
		dataSize, err := readVInt(reader)
		if err != nil {
			return nil, 0, 0, err
		}
		bytesConsumed = int(reader.Size()) - reader.Len()
		dataAreaSize = int64(dataSize)
	}

	// Remaining bytes are the header data
	remainingHeaderSize := int(headerSize) - bytesConsumed
	var headerData []byte
	if remainingHeaderSize > 0 {
		headerData = make([]byte, remainingHeaderSize)
		_, err := io.ReadFull(reader, headerData)
		if err != nil {
			return nil, 0, 0, err
		}
	}

	// Total header size is: CRC (4 bytes) + size vint + headerSize
	totalHeaderSize := 4 + headerSizeVintBytes + int(headerSize)

	return &rar5HeaderData{
		Type:  headerType,
		Flags: headerFlags,
		Data:  headerData,
	}, totalHeaderSize, dataAreaSize, nil
}

// parseVIntFromBuffer parses a vint from a byte slice without any Read calls
// Returns (value, bytesConsumed) - bytesConsumed is 0 if buffer doesn't contain complete vint
func parseVIntFromBuffer(buf []byte) (uint64, int) {
	var result uint64
	for i := 0; i < len(buf) && i < 10; i++ {
		b := buf[i]
		result |= uint64(b&0x7F) << (uint(i) * 7)
		if b&0x80 == 0 {
			return result, i + 1
		}
	}
	return 0, 0 // Incomplete vint
}

// readVIntFromReaderWithBytes reads a variable-length integer from a reader
// Returns the value, number of bytes read, the actual bytes read, and any error
// Optimized: uses stack-allocated array to minimize allocations
func readVIntFromReaderWithBytes(r io.Reader) (uint64, int, []byte, error) {
	// A vint can be at most 10 bytes for a 64-bit value (7 bits per byte)
	// Use stack-allocated array to avoid heap allocation
	var buf [10]byte
	var result uint64
	bytesRead := 0

	for shift := uint(0); shift < 64 && bytesRead < 10; shift += 7 {
		// Read one byte into our buffer
		n, err := r.Read(buf[bytesRead : bytesRead+1])
		if err != nil {
			if bytesRead > 0 {
				return 0, bytesRead, buf[:bytesRead], err
			}
			return 0, 0, nil, err
		}
		if n == 0 {
			if bytesRead > 0 {
				return 0, bytesRead, buf[:bytesRead], io.EOF
			}
			return 0, 0, nil, io.EOF
		}

		b := buf[bytesRead]
		bytesRead++

		result |= uint64(b&0x7F) << shift

		if b&0x80 == 0 {
			// Done - return slice of what we read
			return result, bytesRead, buf[:bytesRead], nil
		}
	}

	return 0, bytesRead, buf[:bytesRead], fmt.Errorf("vint too large")
}

// readVIntFromReader reads a variable-length integer from a reader
// Returns the value, number of bytes read, and any error
func readVIntFromReader(r io.Reader) (uint64, int, error) {
	val, n, _, err := readVIntFromReaderWithBytes(r)
	return val, n, err
}

// readVInt reads a variable-length integer from bytes.Reader (keep for compatibility)
func readVInt(r *bytes.Reader) (uint64, error) {
	val, _, err := readVIntFromReader(r)
	return val, err
}

// parseRAR4Stream parses RAR 4.x headers from a stream reader
// This properly tracks offsets by reading headers sequentially and skipping data
func (p *RARParser) parseRAR4Stream(stream *rarReader, volumeIndex int, volumeName string, volumeSize int64) ([]*RARFileEntry, error) {
	var files []*RARFileEntry

	// Stream position is already at 7 (after RAR4 signature)
	// The signature is: "Rar!\x1A\x07\x00" (7 bytes)

	for {
		// Read RAR4 header
		header, err := p.readRAR4HeaderFromStream(stream)
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}

		// Data offset is immediately after the header
		dataOffsetAbsolute := stream.Position()
		var dataSkipSize int64

		// Parse file headers
		if header.Type == RAR4HeaderTypeFile {
			_, offsetInVol := stream.AbsoluteToVolumeOffset(dataOffsetAbsolute)

			// Use the volumeIndex parameter passed to this function, not the stream's volume index
			// because each stream only contains one volume
			file := p.parseRAR4FileHeader(header, volumeIndex, volumeName, offsetInVol)
			if file != nil {
				// Clamp PackedSize to the remaining bytes in the volume
				// RAR4 headers often report the TOTAL packed size of the file, not just the part in this volume
				// We must limit it to what's actually available in this volume
				remainingInVolume := volumeSize - offsetInVol
				if file.PackedSize > remainingInVolume {
					file.PackedSize = remainingInVolume
					// Also update the volume part size
					if len(file.VolumeParts) > 0 {
						file.VolumeParts[0].PackedSize = remainingInVolume
						file.VolumeParts[0].UnpackedSize = remainingInVolume // Treat as stored stream
					}
				}

				files = append(files, file)
				dataSkipSize = file.PackedSize
			}
		}

		// Skip the file data section (PackedSize) to get to the next header
		// CRITICAL FIX: Do NOT manually skip AddSize.
		// AddSize (if present) is part of the header structure we just read, NOT part of the file data body.
		// dataSkipSize already contains the PackedSize which is the file data body.
		skipTotal := dataSkipSize

		if skipTotal > 0 {
			if err := stream.Skip(skipTotal); err != nil {
				if err == io.EOF {
					break
				}
				return nil, fmt.Errorf("failed to skip RAR4 data section: %w", err)
			}
		}

		// Stop at end of archive
		if header.Type == RAR4HeaderTypeEnd {
			break
		}
	}

	return files, nil
}

// readRAR4HeaderFromStream reads a single RAR 4.x header from stream
func (p *RARParser) readRAR4HeaderFromStream(stream *rarReader) (*rar4Header, error) {
	var header rar4Header

	// Read header CRC (2 bytes)
	if err := binary.Read(stream, binary.LittleEndian, &header.CRC); err != nil {
		return nil, err
	}

	// Read header type (1 byte)
	if err := binary.Read(stream, binary.LittleEndian, &header.Type); err != nil {
		return nil, err
	}

	// Read header flags (2 bytes)
	if err := binary.Read(stream, binary.LittleEndian, &header.Flags); err != nil {
		return nil, err
	}

	// Read header size (2 bytes)
	if err := binary.Read(stream, binary.LittleEndian, &header.HeadSize); err != nil {
		return nil, err
	}

	// Check for zero padding (common at end of RAR files)
	// If we read all zeros, Type will be 0 and HeadSize will be 0
	if header.HeadSize == 0 && header.Type == 0 {
		return nil, io.EOF
	}

	// Validate header size - minimum is 7 bytes
	if header.HeadSize < 7 {
		return nil, fmt.Errorf("invalid RAR4 header size: %d (minimum is 7)", header.HeadSize)
	}

	// Check for marker block or archive header which might have HeadSize of exactly 7
	// For these blocks, there's no additional data
	if header.HeadSize == 7 && (header.Type == RAR4HeaderTypeMarker || header.Type == RAR4HeaderTypeArchive) {
		// This is valid for marker/archive blocks with minimal headers
		return &header, nil
	}

	// CRITICAL FIX: Do NOT strip AddSize from the header data.
	// In RAR4 format, LONG_BLOCK means PackedSize(4) + UnpackedSize(4) are present.
	// These fields are part of the header body and must be read into header.Data
	// so that parseRAR4FileHeader can read them correctly.

	baseHeaderSize := 7

	// Sanity check
	const maxHeaderSize = uint16(65535)
	if header.HeadSize > maxHeaderSize {
		return nil, fmt.Errorf("invalid RAR4 header size: %d", header.HeadSize)
	}

	// Read remaining header data
	remainingSize := int(header.HeadSize) - baseHeaderSize
	if remainingSize < 0 {
		return nil, fmt.Errorf("invalid RAR4 header size calculation: remaining=%d", remainingSize)
	}

	if remainingSize > 0 {
		header.Data = make([]byte, remainingSize)
		if _, err := io.ReadFull(stream, header.Data); err != nil {
			return nil, err
		}
	}

	return &header, nil
}
