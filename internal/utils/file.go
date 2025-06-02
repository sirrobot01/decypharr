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
