package utils

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
)

func PathUnescape(path string) string {

	// try to use url.PathUnescape
	if unescaped, err := url.PathUnescape(path); err == nil {
		return unescaped
	}

	// unescape %
	unescapedPath := strings.ReplaceAll(path, "%25", "%")

	// add others

	return unescapedPath
}

func PreCacheFile(filePaths []string) error {
	if len(filePaths) == 0 {
		return fmt.Errorf("no file paths provided")
	}

	for _, filePath := range filePaths {
		err := func(f string) error {

			file, err := os.Open(f)
			if err != nil {
				if os.IsNotExist(err) {
					// File has probably been moved by arr, return silently
					return nil
				}
				return fmt.Errorf("failed to open file: %s: %v", f, err)
			}
			defer file.Close()

			// Pre-cache the file header (first 256KB) using 16KB chunks.
			if err := readSmallChunks(file, 0, 256*1024, 16*1024); err != nil {
				return err
			}
			if err := readSmallChunks(file, 1024*1024, 64*1024, 16*1024); err != nil {
				return err
			}
			return nil
		}(filePath)
		if err != nil {
			return err
		}
	}
	return nil
}

func readSmallChunks(file *os.File, startPos int64, totalToRead int, chunkSize int) error {
	_, err := file.Seek(startPos, 0)
	if err != nil {
		return err
	}

	buf := make([]byte, chunkSize)
	bytesRemaining := totalToRead

	for bytesRemaining > 0 {
		toRead := chunkSize
		if bytesRemaining < chunkSize {
			toRead = bytesRemaining
		}

		n, err := file.Read(buf[:toRead])
		if err != nil {
			if err == io.EOF {
				break
			}
			return err
		}

		bytesRemaining -= n
	}
	return nil
}

func EnsureDir(dirPath string) error {
	if dirPath == "" {
		return fmt.Errorf("directory path is empty")
	}
	_, err := os.Stat(dirPath)
	if os.IsNotExist(err) {
		// Directory does not exist, create it
		if err := os.MkdirAll(dirPath, 0755); err != nil {
			return fmt.Errorf("failed to create directory: %v", err)
		}
		return nil
	}
	return err
}

func FormatSize(bytes int64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
		TB = 1024 * GB
	)

	var size float64
	var unit string

	switch {
	case bytes >= TB:
		size = float64(bytes) / TB
		unit = "TB"
	case bytes >= GB:
		size = float64(bytes) / GB
		unit = "GB"
	case bytes >= MB:
		size = float64(bytes) / MB
		unit = "MB"
	case bytes >= KB:
		size = float64(bytes) / KB
		unit = "KB"
	default:
		size = float64(bytes)
		unit = "bytes"
	}

	// Format to 2 decimal places for larger units, no decimals for bytes
	if unit == "bytes" {
		return fmt.Sprintf("%.0f %s", size, unit)
	}
	return fmt.Sprintf("%.2f %s", size, unit)
}
