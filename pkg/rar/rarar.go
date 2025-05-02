// Source: https://github.com/eliasbenb/RARAR.py
// Note that this code only translates the original Python for RAR3 (not RAR5) support.

package rar

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
)

// Constants from the Python code
var (
	// Chunk sizes
	DefaultChunkSize = 4096
	HttpChunkSize    = 32768
	MaxSearchSize    = 1 << 20 // 1MB

	// RAR marker and block types
	Rar3Marker  = []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}
	BlockFile   = byte(0x74)
	BlockHeader = byte(0x73)
	BlockMarker = byte(0x72)
	BlockEnd    = byte(0x7B)

	// Header flags
	FlagDirectory      = 0xE0
	FlagHasHighSize    = 0x100
	FlagHasUnicodeName = 0x200
	FlagHasData        = 0x8000
)

// Compression methods
var CompressionMethods = map[byte]string{
	0x30: "Store",
	0x31: "Fastest",
	0x32: "Fast",
	0x33: "Normal",
	0x34: "Good",
	0x35: "Best",
}

// Error definitions
var (
	ErrMarkerNotFound               = errors.New("RAR marker not found within search limit")
	ErrInvalidFormat                = errors.New("invalid RAR format")
	ErrNetworkError                 = errors.New("network error")
	ErrRangeRequestsNotSupported    = errors.New("server does not support range requests")
	ErrCompressionNotSupported      = errors.New("compression method not supported")
	ErrDirectoryExtractNotSupported = errors.New("directory extract not supported")
)

// File represents a file entry in a RAR archive
type File struct {
	Path           string
	Size           int64
	CompressedSize int64
	Method         byte
	CRC            uint32
	IsDirectory    bool
	DataOffset     int64
	NextOffset     int64
}

// Name returns the base filename of the file
func (f *File) Name() string {
	return filepath.Base(f.Path)
}

func (f *File) ByteRange() *[2]int64 {
	return &[2]int64{f.DataOffset, f.DataOffset + f.CompressedSize - 1}
}

// HttpFile represents a file accessed over HTTP
type HttpFile struct {
	URL      string
	Position int64
	Client   *http.Client
	FileSize int64
}

// NewHttpFile creates a new HTTP file
func NewHttpFile(url string) (*HttpFile, error) {
	client := &http.Client{}
	file := &HttpFile{
		URL:      url,
		Position: 0,
		Client:   client,
	}

	// Get file size
	size, err := file.getFileSize()
	if err != nil {
		return nil, fmt.Errorf("failed to get file size: %w", err)
	}
	file.FileSize = size

	return file, nil
}

// getFileSize gets the total file size from the server
func (f *HttpFile) getFileSize() (int64, error) {
	resp, err := f.Client.Head(f.URL)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
	}

	contentLength := resp.Header.Get("Content-Length")
	if contentLength == "" {
		return 0, fmt.Errorf("%w: content length not provided", ErrNetworkError)
	}

	var size int64
	_, err = fmt.Sscanf(contentLength, "%d", &size)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
	}

	return size, nil
}

// ReadAt implements the io.ReaderAt interface
func (f *HttpFile) ReadAt(p []byte, off int64) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}

	// Ensure we don't read past the end of the file
	size := int64(len(p))
	if f.FileSize > 0 {
		remaining := f.FileSize - off
		if remaining <= 0 {
			return 0, io.EOF
		}
		if size > remaining {
			size = remaining
			p = p[:size]
		}
	}

	// Create HTTP request with Range header
	req, err := http.NewRequest("GET", f.URL, nil)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
	}

	end := off + size - 1
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

	// Make the request
	resp, err := f.Client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
	}
	defer resp.Body.Close()

	// Handle response
	switch resp.StatusCode {
	case http.StatusPartialContent:
		// Read the content
		return io.ReadFull(resp.Body, p)
	case http.StatusOK:
		// Some servers return the full content instead of partial
		// In this case, we need to read the entire response and extract the part we need
		fullData, err := io.ReadAll(resp.Body)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
		}

		if int64(len(fullData)) <= off {
			return 0, io.EOF
		}

		end = off + size
		if int64(len(fullData)) < end {
			end = int64(len(fullData))
		}

		copy(p, fullData[off:end])
		return int(end - off), nil
	case http.StatusRequestedRangeNotSatisfiable:
		// We're at EOF
		return 0, io.EOF
	default:
		return 0, fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
	}
}

// Reader reads RAR3 format archives
type Reader struct {
	File      *HttpFile
	ChunkSize int
	Marker    int64
	Files     []*File
}

// NewReader creates a new RAR3 reader
func NewReader(url string) (*Reader, error) {
	file, err := NewHttpFile(url)
	if err != nil {
		return nil, err
	}

	reader := &Reader{
		File:      file,
		ChunkSize: HttpChunkSize,
		Files:     make([]*File, 0),
	}

	// Find RAR marker
	marker, err := reader.findMarker()
	if err != nil {
		return nil, err
	}
	reader.Marker = marker

	// Generate file list
	if err := reader.readFiles(); err != nil {
		return nil, err
	}

	return reader, nil
}

// readBytes reads a range of bytes from the file
func (r *Reader) readBytes(start int64, length int) ([]byte, error) {
	if length <= 0 {
		return []byte{}, nil
	}

	data := make([]byte, length)
	n, err := r.File.ReadAt(data, start)
	if err != nil && err != io.EOF {
		return nil, err
	}

	if n < length {
		// Partial read, return what we got
		return data[:n], nil
	}

	return data, nil
}

// findMarker finds the RAR marker in the file
func (r *Reader) findMarker() (int64, error) {
	// First try to find marker in the first chunk
	firstChunkSize := 8192 // 8KB
	chunk, err := r.readBytes(0, firstChunkSize)
	if err != nil {
		return 0, err
	}

	markerPos := bytes.Index(chunk, Rar3Marker)
	if markerPos != -1 {
		return int64(markerPos), nil
	}

	// If not found, continue searching
	position := int64(firstChunkSize - len(Rar3Marker) + 1)
	maxSearch := int64(MaxSearchSize)

	for position < maxSearch {
		chunkSize := min(r.ChunkSize, int(maxSearch-position))
		chunk, err := r.readBytes(position, chunkSize)
		if err != nil || len(chunk) == 0 {
			break
		}

		markerPos := bytes.Index(chunk, Rar3Marker)
		if markerPos != -1 {
			return position + int64(markerPos), nil
		}

		// Move forward by chunk size minus the marker length
		position += int64(max(1, len(chunk)-len(Rar3Marker)+1))
	}

	return 0, ErrMarkerNotFound
}

// decodeUnicode decodes RAR3 Unicode encoding
func decodeUnicode(asciiStr string, unicodeData []byte) string {
	if len(unicodeData) == 0 {
		return asciiStr
	}

	result := []rune{}
	asciiPos := 0
	dataPos := 0
	highByte := byte(0)

	for dataPos < len(unicodeData) {
		flags := unicodeData[dataPos]
		dataPos++

		// Determine the number of character positions this flag byte controls
		var flagBits uint
		var flagCount int
		var bitCount int

		if flags&0x80 != 0 {
			// Extended flag - controls up to 32 characters (16 bit pairs)
			flagBits = uint(flags)
			bitCount = 1
			for (flagBits&(0x80>>bitCount) != 0) && dataPos < len(unicodeData) {
				flagBits = ((flagBits & ((0x80 >> bitCount) - 1)) << 8) | uint(unicodeData[dataPos])
				dataPos++
				bitCount++
			}
			flagCount = bitCount * 4
		} else {
			// Simple flag - controls 4 characters (4 bit pairs)
			flagBits = uint(flags)
			flagCount = 4
		}

		// Process each 2-bit flag
		for i := 0; i < flagCount; i++ {
			if asciiPos >= len(asciiStr) && dataPos >= len(unicodeData) {
				break
			}

			flagValue := (flagBits >> (i * 2)) & 0x03

			switch flagValue {
			case 0:
				// Use ASCII character
				if asciiPos < len(asciiStr) {
					result = append(result, rune(asciiStr[asciiPos]))
					asciiPos++
				}
			case 1:
				// Unicode character with high byte 0
				if dataPos < len(unicodeData) {
					result = append(result, rune(unicodeData[dataPos]))
					dataPos++
				}
			case 2:
				// Unicode character with current high byte
				if dataPos < len(unicodeData) {
					lowByte := unicodeData[dataPos]
					dataPos++
					result = append(result, rune(lowByte)|rune(highByte)<<8)
				}
			case 3:
				// Set new high byte
				if dataPos < len(unicodeData) {
					highByte = unicodeData[dataPos]
					dataPos++
				}
			}
		}
	}

	// Append any remaining ASCII characters
	for asciiPos < len(asciiStr) {
		result = append(result, rune(asciiStr[asciiPos]))
		asciiPos++
	}

	return string(result)
}

// readFiles reads all file entries in the archive
func (r *Reader) readFiles() error {
	pos := r.Marker
	pos += int64(len(Rar3Marker)) // Skip marker block

	// Read archive header
	headerData, err := r.readBytes(pos, 7)
	if err != nil {
		return err
	}

	headType := headerData[2]
	headSize := int(binary.LittleEndian.Uint16(headerData[5:7]))

	if headType != BlockHeader {
		return ErrInvalidFormat
	}

	pos += int64(headSize) // Skip archive header

	// Process file entries
	for {
		headerData, err := r.readBytes(pos, 7)
		if err != nil || len(headerData) < 7 {
			// Reached end of file
			break
		}

		headType := headerData[2]
		headFlags := int(binary.LittleEndian.Uint16(headerData[3:5]))
		headSize := int(binary.LittleEndian.Uint16(headerData[5:7]))

		if headType == BlockEnd {
			// End of archive
			break
		}

		if headType == BlockFile {
			// Get complete header data
			completeHeader, err := r.readBytes(pos, headSize)
			if err != nil || len(completeHeader) < headSize {
				// Couldn't read complete header
				break
			}

			fileInfo, err := r.parseFileHeader(completeHeader, pos)
			if err == nil && fileInfo != nil {
				r.Files = append(r.Files, fileInfo)
				pos = fileInfo.NextOffset
			} else {
				pos += int64(headSize)
			}
		} else {
			// Skip non-file block
			pos += int64(headSize)

			// Skip data if present
			if headFlags&FlagHasData != 0 {
				// Read data size
				sizeData, err := r.readBytes(pos-4, 4)
				if err != nil || len(sizeData) < 4 {
					break
				}

				dataSize := int64(binary.LittleEndian.Uint32(sizeData))
				pos += dataSize
			}
		}
	}

	return nil
}

// parseFileHeader parses a file header and returns file info
func (r *Reader) parseFileHeader(headerData []byte, position int64) (*File, error) {
	if len(headerData) < 7 {
		return nil, fmt.Errorf("header data too short")
	}

	headType := headerData[2]
	headFlags := int(binary.LittleEndian.Uint16(headerData[3:5]))
	headSize := int(binary.LittleEndian.Uint16(headerData[5:7]))

	if headType != BlockFile {
		return nil, fmt.Errorf("not a file block")
	}

	// Check if we have enough data
	if len(headerData) < 32 {
		return nil, fmt.Errorf("file header too short")
	}

	// Parse basic file header fields
	packSize := binary.LittleEndian.Uint32(headerData[7:11])
	unpackSize := binary.LittleEndian.Uint32(headerData[11:15])
	// fileOS := headerData[15]
	fileCRC := binary.LittleEndian.Uint32(headerData[16:20])
	// fileTime := binary.LittleEndian.Uint32(headerData[20:24])
	// unpVer := headerData[24]
	method := headerData[25]
	nameSize := binary.LittleEndian.Uint16(headerData[26:28])
	// fileAttr := binary.LittleEndian.Uint32(headerData[28:32])

	// Handle high pack/unp sizes
	highPackSize := uint32(0)
	highUnpSize := uint32(0)

	offset := 32 // Start after basic header fields

	if headFlags&FlagHasHighSize != 0 {
		if offset+8 <= len(headerData) {
			highPackSize = binary.LittleEndian.Uint32(headerData[offset : offset+4])
			highUnpSize = binary.LittleEndian.Uint32(headerData[offset+4 : offset+8])
		}
		offset += 8
	}

	// Calculate actual sizes
	fullPackSize := int64(packSize) + (int64(highPackSize) << 32)
	fullUnpSize := int64(unpackSize) + (int64(highUnpSize) << 32)

	// Read filename
	var fileName string
	if offset+int(nameSize) <= len(headerData) {
		fileNameBytes := headerData[offset : offset+int(nameSize)]

		if headFlags&FlagHasUnicodeName != 0 {
			// Handle Unicode filename
			zeroPos := bytes.IndexByte(fileNameBytes, 0)
			if zeroPos != -1 {
				// Has ASCII and Unicode parts
				asciiPart := string(fileNameBytes[:zeroPos])
				unicodePart := fileNameBytes[zeroPos+1:]
				fileName = decodeUnicode(asciiPart, unicodePart)
			} else {
				// No null byte, try as UTF-8
				fileName = string(fileNameBytes)
			}
		} else {
			// Try UTF-8, then fall back to ASCII
			fileName = string(fileNameBytes)
		}
	} else {
		fileName = fmt.Sprintf("UnknownFile%d", len(r.Files))
	}

	isDirectory := (headFlags & FlagDirectory) == FlagDirectory

	// Calculate data offsets
	dataOffset := position + int64(headSize)
	nextOffset := dataOffset

	// Only add data size if it's not a directory and has data
	if !isDirectory && headFlags&FlagHasData != 0 {
		nextOffset += fullPackSize
	}

	return &File{
		Path:           fileName,
		Size:           fullUnpSize,
		CompressedSize: fullPackSize,
		Method:         method,
		CRC:            fileCRC,
		IsDirectory:    isDirectory,
		DataOffset:     dataOffset,
		NextOffset:     nextOffset,
	}, nil
}

// GetFiles returns all files in the archive
func (r *Reader) GetFiles() []*File {
	return r.Files
}

// ExtractFile extracts a file from the archive
func (r *Reader) ExtractFile(file *File) ([]byte, error) {
	if file.IsDirectory {
		return nil, ErrDirectoryExtractNotSupported
	}

	// Only support "Store" method
	if file.Method != 0x30 { // 0x30 = "Store"
		return nil, ErrCompressionNotSupported
	}

	return r.readBytes(file.DataOffset, int(file.CompressedSize))
}

// Helper functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
