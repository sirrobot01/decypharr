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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/retry"
)

// Constants from the Python code
var (
	// DefaultChunkSize Chunk sizes
	DefaultChunkSize = 4096
	HttpChunkSize    = 32768
	MaxSearchSize    = 1 << 20 // 1MB

	// Rar3Marker RAR marker and block types
	Rar3Marker  = []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}
	BlockFile   = byte(0x74)
	BlockHeader = byte(0x73)
	BlockMarker = byte(0x72)
	BlockEnd    = byte(0x7B)

	// FlagDirectory Header flags
	FlagDirectory      = 0xE0
	FlagHasHighSize    = 0x100
	FlagHasUnicodeName = 0x200
	FlagHasData        = 0x8000
)

// Error definitions
var (
	ErrMarkerNotFound               = errors.New("RAR marker not found within search limit")
	ErrInvalidFormat                = errors.New("invalid RAR format")
	ErrNetworkError                 = errors.New("network error")
	ErrRangeRequestsNotSupported    = errors.New("server does not support range requests")
	ErrCompressionNotSupported      = errors.New("compression method not supported")
	ErrDirectoryExtractNotSupported = errors.New("directory extract not supported")
)

// Name returns the base filename of the file
func (f *File) Name() string {
	if i := strings.LastIndexAny(f.Path, "\\/"); i >= 0 {
		return f.Path[i+1:]
	}
	return f.Path
}

func (f *File) ByteRange() *[2]int64 {
	return &[2]int64{f.DataOffset, f.DataOffset + f.CompressedSize - 1}
}

func NewHttpFile(url string) (*HttpFile, error) {
	file := &HttpFile{
		URL:        url,
		Position:   0,
		client:     &http.Client{Timeout: 60 * time.Second},
		MaxRetries: config.Get().Retries,
	}

	// GetReader file size
	size, err := file.getFileSize()
	if err != nil {
		return nil, fmt.Errorf("failed to get file size: %w", err)
	}
	file.FileSize = size

	return file, nil
}

func (f *HttpFile) doWithRetry(operation func() (any, error)) (any, error) {
	var result any

	err := retry.Do(
		func() error {
			var opErr error
			result, opErr = operation()
			if opErr == nil {
				return nil
			}
			// Only retry on network errors
			if !errors.Is(opErr, ErrNetworkError) {
				return retry.Unrecoverable(opErr)
			}
			return opErr
		},
		retry.Attempts(uint(f.MaxRetries)+1),
		retry.Delay(config.DefaultRetryDelay),
		retry.MaxDelay(config.DefaultRetryDelayMax),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
	)

	if err != nil {
		return nil, err
	}
	return result, nil
}

// getFileSize gets the total file size from the server
func (f *HttpFile) getFileSize() (int64, error) {
	result, err := f.doWithRetry(func() (any, error) {
		req, err := http.NewRequest(http.MethodHead, f.URL, nil)
		if err != nil {
			return int64(0), fmt.Errorf("%w: %v", ErrNetworkError, err)
		}

		resp, err := f.client.Do(req)
		if err != nil {
			return int64(0), fmt.Errorf("%w: %v", ErrNetworkError, err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return int64(0), fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
		}

		contentLength := resp.Header.Get("Content-Length")
		if contentLength == "" {
			return int64(0), fmt.Errorf("%w: content length not provided", ErrNetworkError)
		}

		var size int64
		_, err = fmt.Sscanf(contentLength, "%d", &size)
		if err != nil {
			return int64(0), fmt.Errorf("%w: %v", ErrNetworkError, err)
		}

		return size, nil
	})

	if err != nil {
		return 0, err
	}

	return result.(int64), nil
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

	result, err := f.doWithRetry(func() (any, error) {
		// Create HTTP request with Range header
		end := off + size - 1

		req, err := http.NewRequest(http.MethodGet, f.URL, nil)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
		}
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", off, end))

		// Make the request
		resp, err := f.client.Do(req)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
		}
		defer func(Body io.ReadCloser) {
			err := Body.Close()
			if err != nil {
				fmt.Printf("warning: failed to close response body: %v\n", err)
			}
		}(resp.Body)

		// Handle response
		switch resp.StatusCode {
		case http.StatusPartialContent:
			// Read the content
			bytesRead, err := io.ReadFull(resp.Body, p)
			return bytesRead, err
		case http.StatusOK:
			fullData, err := io.ReadAll(resp.Body)
			if err != nil {
				return 0, fmt.Errorf("%w: %v", ErrNetworkError, err)
			}

			if int64(len(fullData)) <= off {
				return 0, io.EOF
			}

			end = min(int64(len(fullData)), off+size)

			copy(p, fullData[off:end])
			return int(end - off), nil
		case http.StatusRequestedRangeNotSatisfiable:
			// We're at EOF
			return 0, io.EOF
		default:
			return 0, fmt.Errorf("%w: unexpected status code: %d", ErrNetworkError, resp.StatusCode)
		}
	})

	if err != nil {
		return 0, err
	}

	return result.(int), nil
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
	pos := reader.Marker + int64(len(Rar3Marker)) // Skip marker block

	headerData, err := reader.readBytes(pos, 7)
	if err != nil {
		return nil, err
	}

	if len(headerData) < 7 {
		return nil, ErrInvalidFormat
	}

	headType := headerData[2]
	headSize := int(binary.LittleEndian.Uint16(headerData[5:7]))

	if headType != BlockHeader {
		return nil, ErrInvalidFormat
	}

	// Store the position after the archive header
	reader.HeaderEndPos = pos + int64(headSize)

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

	var result []rune
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

		// Parse each 2-bit flag
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
					lowByte := uint(unicodeData[dataPos])
					dataPos++
					result = append(result, rune(lowByte|(uint(highByte)<<8)))
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
	// NewReader already validated the archive header and stored where it ends.
	pos := r.HeaderEndPos

	// Process all blocks until BlockEnd or EOF.
	for {
		var headerData []byte
		err := retry.Do(
			func() error {
				var readErr error
				headerData, readErr = r.readBytes(pos, 7)
				if readErr != nil {
					if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, ErrNetworkError) {
						return retry.Unrecoverable(fmt.Errorf("error reading block header: %w", readErr))
					}
					return readErr
				}
				if len(headerData) < 7 {
					return fmt.Errorf("incomplete block header")
				}
				return nil
			},
			retry.Attempts(4),
			retry.Delay(config.DefaultRetryDelay),
			retry.MaxDelay(config.DefaultRetryDelayMax),
			retry.DelayType(retry.BackOffDelay),
			retry.LastErrorOnly(true),
		)
		if err != nil || len(headerData) < 7 {
			// EOF or unrecoverable read error — stop iteration.
			break
		}

		headType := headerData[2]
		headFlags := int(binary.LittleEndian.Uint16(headerData[3:5]))
		headSize := int(binary.LittleEndian.Uint16(headerData[5:7]))

		if headType == BlockEnd {
			break
		}

		if headType == BlockFile {
			var completeHeader []byte
			err = retry.Do(
				func() error {
					var readErr error
					completeHeader, readErr = r.readBytes(pos, headSize)
					if readErr != nil {
						return readErr
					}
					if len(completeHeader) < headSize {
						return fmt.Errorf("incomplete header data")
					}
					return nil
				},
				retry.Attempts(4),
				retry.Delay(config.DefaultRetryDelay),
				retry.MaxDelay(config.DefaultRetryDelayMax),
				retry.DelayType(retry.BackOffDelay),
				retry.LastErrorOnly(true),
			)
			if err != nil {
				return fmt.Errorf("failed to read complete file header after retries: %w", err)
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
				var sizeData []byte
				err = retry.Do(
					func() error {
						var readErr error
						sizeData, readErr = r.readBytes(pos-4, 4)
						if readErr != nil {
							return readErr
						}
						if len(sizeData) < 4 {
							return fmt.Errorf("incomplete size data")
						}
						return nil
					},
					retry.Attempts(4),
					retry.Delay(config.DefaultRetryDelay),
					retry.MaxDelay(config.DefaultRetryDelayMax),
					retry.DelayType(retry.BackOffDelay),
					retry.LastErrorOnly(true),
				)
				if err != nil {
					return fmt.Errorf("failed to read data size after retries: %w", err)
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
			before, after, ok := bytes.Cut(fileNameBytes, []byte{0})
			if ok {
				// Try UTF-8 first
				asciiPart := before
				if utf8.Valid(asciiPart) {
					fileName = string(asciiPart)
				} else {
					// Fall back to custom decoder
					asciiStr := string(asciiPart)
					unicodePart := after
					fileName = decodeUnicode(asciiStr, unicodePart)
				}
			} else {
				// No null byte
				if utf8.Valid(fileNameBytes) {
					fileName = string(fileNameBytes)
				} else {
					fileName = string(fileNameBytes) // Last resort
				}
			}
		} else {
			// Non-Unicode filename
			if utf8.Valid(fileNameBytes) {
				fileName = string(fileNameBytes)
			} else {
				fileName = string(fileNameBytes) // Fallback
			}
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
func (r *Reader) GetFiles() ([]*File, error) {
	if len(r.Files) == 0 {
		err := r.readFiles()
		if err != nil {
			return nil, err
		}
	}

	return r.Files, nil
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
