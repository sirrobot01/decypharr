package utils

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// TruncateName truncates a filesystem name component to maxBytes bytes while
// preserving valid UTF-8. Linux NAME_MAX is 255 bytes; pass 255 for safety.
func TruncateName(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	b := []byte(name)[:maxBytes]
	// Walk back until the byte slice is valid UTF-8 (avoids splitting multibyte runes).
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return strings.TrimRight(string(b), " ")
}

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
