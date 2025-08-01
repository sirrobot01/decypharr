package utils

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

func RemoveItem[S ~[]E, E comparable](s S, values ...E) S {
	result := make(S, 0, len(s))
outer:
	for _, item := range s {
		for _, v := range values {
			if item == v {
				continue outer
			}
		}
		result = append(result, item)
	}
	return result
}

func Contains(slice []string, value string) bool {
	for _, item := range slice {
		if item == value {
			return true
		}
	}
	return false
}

func GenerateHash(data string) string {
	// Simple hash generation using a basic algorithm (for demonstration purposes)
	_hash := 0
	for _, char := range data {
		_hash = (_hash*31 + int(char)) % 1000003 // Simple hash function
	}
	return string(rune(_hash))
}

func DownloadFile(url string) (string, []byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("failed to download file: status code %d", resp.StatusCode)
	}

	filename := getFilenameFromResponse(resp, url)

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read response body: %w", err)
	}

	return filename, data, nil
}

func getFilenameFromResponse(resp *http.Response, originalURL string) string {
	// 1. Try Content-Disposition header
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			if filename := params["filename"]; filename != "" {
				return filename
			}
		}
	}

	// 2. Try to decode URL-encoded filename from Content-Disposition
	if cd := resp.Header.Get("Content-Disposition"); cd != "" {
		if strings.Contains(cd, "filename*=") {
			// Handle RFC 5987 encoded filenames
			parts := strings.Split(cd, "filename*=")
			if len(parts) > 1 {
				encoded := strings.Trim(parts[1], `"`)
				if strings.HasPrefix(encoded, "UTF-8''") {
					if decoded, err := url.QueryUnescape(encoded[7:]); err == nil {
						return decoded
					}
				}
			}
		}
	}

	// 3. Fall back to URL path
	if parsedURL, err := url.Parse(originalURL); err == nil {
		if filename := filepath.Base(parsedURL.Path); filename != "." && filename != "/" {
			// URL decode the filename
			if decoded, err := url.QueryUnescape(filename); err == nil {
				return decoded
			}
			return filename
		}
	}

	// 4. Default filename
	return "downloaded_file"
}

func ValidateServiceURL(urlStr string) error {
	if urlStr == "" {
		return fmt.Errorf("URL cannot be empty")
	}

	// Try parsing as full URL first
	u, err := url.Parse(urlStr)
	if err == nil && u.Scheme != "" && u.Host != "" {
		// It's a full URL, validate scheme
		if u.Scheme != "http" && u.Scheme != "https" {
			return fmt.Errorf("URL scheme must be http or https")
		}
		return nil
	}

	// Check if it's a host:port format (no scheme)
	if strings.Contains(urlStr, ":") && !strings.Contains(urlStr, "://") {
		// Try parsing with http:// prefix
		testURL := "http://" + urlStr
		u, err := url.Parse(testURL)
		if err != nil {
			return fmt.Errorf("invalid host:port format: %w", err)
		}

		if u.Host == "" {
			return fmt.Errorf("host is required in host:port format")
		}

		// Validate port number
		if u.Port() == "" {
			return fmt.Errorf("port is required in host:port format")
		}

		return nil
	}

	return fmt.Errorf("invalid URL format: %s", urlStr)
}

func ExtractFilenameFromURL(rawURL string) string {
	// Parse the URL
	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}

	// Get the base filename from path
	filename := path.Base(parsedURL.Path)

	// Handle edge cases
	if filename == "/" || filename == "." || filename == "" {
		return ""
	}

	return filename
}
