package utils

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.uber.org/ratelimit"
)

func ParseRateLimit(rateStr string) ratelimit.Limiter {
	if rateStr == "" {
		return nil
	}
	parts := strings.SplitN(rateStr, "/", 2)
	if len(parts) != 2 {
		return nil
	}

	// parse count
	count, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || count <= 0 {
		return nil
	}

	// Set slack size to 10%
	slackSize := count / 10

	// normalize unit
	unit := strings.ToLower(strings.TrimSpace(parts[1]))
	unit = strings.TrimSuffix(unit, "s")
	switch unit {
	case "minute", "min":
		return ratelimit.New(count, ratelimit.Per(time.Minute), ratelimit.WithSlack(slackSize))
	case "second", "sec":
		return ratelimit.New(count, ratelimit.Per(time.Second), ratelimit.WithSlack(slackSize))
	case "hour", "hr":
		return ratelimit.New(count, ratelimit.Per(time.Hour), ratelimit.WithSlack(slackSize))
	case "day", "d":
		return ratelimit.New(count, ratelimit.Per(24*time.Hour), ratelimit.WithSlack(slackSize))
	default:
		return nil
	}
}

func JSONResponse(w http.ResponseWriter, data any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if data != nil {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(data)
	}
}

func ValidateURL(urlStr string) error {
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

func JoinURL(base string, paths ...string) (string, error) {
	// Split the last path component to separate query parameters
	lastPath := paths[len(paths)-1]
	parts := strings.Split(lastPath, "?")
	paths[len(paths)-1] = parts[0]

	joined, err := url.JoinPath(base, paths...)
	if err != nil {
		return "", err
	}

	// AddOrUpdate back query parameters if they exist
	if len(parts) > 1 {
		return joined + "?" + parts[1], nil
	}

	return joined, nil
}

type DownloadOptions func(r *http.Request)

func WithHeader(key, value string) DownloadOptions {
	return func(r *http.Request) {
		r.Header.Set(key, value)
	}
}

func DownloadFile(url string, options ...DownloadOptions) (string, []byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Apply options to the request
	for _, opt := range options {
		opt(req)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("failed to download file: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("failed to download file: status code %d", resp.StatusCode)
	}
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
		// First try standard MIME parsing
		if _, params, err := mime.ParseMediaType(cd); err == nil {
			// RFC 5987: filename* takes precedence
			if filename := params["filename*"]; filename != "" {
				return filename
			}
			if filename := params["filename"]; filename != "" {
				return filename
			}
		}

		// Manual fallback for non-compliant headers (unquoted filenames with special chars)
		if filename := extractFilenameManual(cd); filename != "" {
			return filename
		}
	}

	// 2. Fall back to URL path
	if parsedURL, err := url.Parse(originalURL); err == nil {
		if filename := filepath.Base(parsedURL.Path); filename != "." && filename != "/" {
			// URL decode the filename
			if decoded, err := url.QueryUnescape(filename); err == nil {
				return decoded
			}
			return filename
		}
	}

	// 3. Default filename
	return "downloaded_file"
}

// extractFilenameManual handles non-compliant Content-Disposition headers
// where filename is not properly quoted (e.g., filename=[Erai-raws]...nzb)
func extractFilenameManual(cd string) string {
	// Try filename*= first (RFC 5987)
	if _, after, ok := strings.Cut(cd, "filename*="); ok {
		value := after
		// Handle UTF-8'' prefix
		if strings.HasPrefix(value, "UTF-8''") || strings.HasPrefix(value, "utf-8''") {
			value = value[7:]
		}
		// Take until semicolon or end
		if semi := strings.Index(value, ";"); semi != -1 {
			value = value[:semi]
		}
		value = strings.Trim(value, `"' `)
		if decoded, err := url.QueryUnescape(value); err == nil {
			return decoded
		}
		return value
	}

	// Try filename= (simple case)
	if _, after, ok := strings.Cut(cd, "filename="); ok {
		value := after
		// Take until semicolon or end
		if semi := strings.Index(value, ";"); semi != -1 {
			value = value[:semi]
		}
		// Remove surrounding quotes if present
		value = strings.Trim(value, `"' `)
		if value != "" {
			return value
		}
	}

	return ""
}

func GetContentType(fileName string) string {
	contentType := mime.TypeByExtension(filepath.Ext(fileName))
	if contentType == "" {
		return "application/octet-stream"
	}
	return contentType
}

// IsValidURL checks if a string is a valid HTTP/HTTPS URL.
// Optimized for speed with early exits before calling url.Parse.
func IsValidURL(s string) bool {
	n := len(s)
	if n < 10 { // minimum: "http://a.b"
		return false
	}

	// Fast scheme check without allocation
	var schemeEnd int
	if s[0] == 'h' && s[1] == 't' && s[2] == 't' && s[3] == 'p' {
		if s[4] == ':' && s[5] == '/' && s[6] == '/' {
			schemeEnd = 7 // http://
		} else if s[4] == 's' && s[5] == ':' && s[6] == '/' && s[7] == '/' {
			schemeEnd = 8 // https://
		} else {
			return false
		}
	} else {
		return false
	}

	// Check host portion is non-empty
	host := s[schemeEnd:]
	if slashIdx := strings.IndexByte(host, '/'); slashIdx != -1 {
		host = host[:slashIdx]
	}
	if len(host) == 0 {
		return false
	}

	// Full parse for edge cases (ports, userinfo, IPv6, etc.)
	u, err := url.Parse(s)
	return err == nil && u.Host != ""
}
