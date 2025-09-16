package webdav

import (
	"fmt"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/stanNthe5/stringbuf"
)

var pctHex = "0123456789ABCDEF"

// fastEscapePath returns a percent-encoded path, preserving '/'
// and only encoding bytes outside the unreserved set:
//
//	ALPHA / DIGIT / '-' / '_' / '.' / '~' / '/'
func fastEscapePath(p string) string {
	var b strings.Builder

	for i := 0; i < len(p); i++ {
		c := p[i]
		// unreserved (plus '/')
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' ||
			c == '.' || c == '~' ||
			c == '/' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(pctHex[c>>4])
			b.WriteByte(pctHex[c&0xF])
		}
	}
	return b.String()
}

type entry struct {
	escHref string // already XML-safe + percent-escaped
	escName string
	size    int64
	isDir   bool
	modTime string
}

func filesToXML(urlPath string, fi os.FileInfo, children []os.FileInfo) stringbuf.StringBuf {

	now := time.Now().UTC().Format(time.RFC3339)
	entries := make([]entry, 0, len(children)+1)

	// Add the current file itself
	entries = append(entries, entry{
		escHref: xmlEscape(fastEscapePath(urlPath)),
		escName: xmlEscape(fi.Name()),
		isDir:   fi.IsDir(),
		size:    fi.Size(),
		modTime: fi.ModTime().Format(time.RFC3339),
	})
	for _, info := range children {

		nm := info.Name()
		// build raw href
		href := path.Join("/", urlPath, nm)
		if info.IsDir() {
			href += "/"
		}

		entries = append(entries, entry{
			escHref: xmlEscape(fastEscapePath(href)),
			escName: xmlEscape(nm),
			isDir:   info.IsDir(),
			size:    info.Size(),
			modTime: info.ModTime().Format(time.RFC3339),
		})
	}

	sb := stringbuf.New("")

	// XML header and main element
	_, _ = sb.WriteString(`<?xml version="1.0" encoding="UTF-8"?>`)
	_, _ = sb.WriteString(`<d:multistatus xmlns:d="DAV:">`)

	// Add responses for each entry
	for _, e := range entries {
		_, _ = sb.WriteString(`<d:response>`)
		_, _ = sb.WriteString(`<d:href>`)
		_, _ = sb.WriteString(e.escHref)
		_, _ = sb.WriteString(`</d:href>`)
		_, _ = sb.WriteString(`<d:propstat>`)
		_, _ = sb.WriteString(`<d:prop>`)

		if e.isDir {
			_, _ = sb.WriteString(`<d:resourcetype><d:collection/></d:resourcetype>`)
		} else {
			_, _ = sb.WriteString(`<d:resourcetype/>`)
			_, _ = sb.WriteString(`<d:getcontentlength>`)
			_, _ = sb.WriteString(strconv.FormatInt(e.size, 10))
			_, _ = sb.WriteString(`</d:getcontentlength>`)
		}

		_, _ = sb.WriteString(`<d:getlastmodified>`)
		_, _ = sb.WriteString(now)
		_, _ = sb.WriteString(`</d:getlastmodified>`)

		_, _ = sb.WriteString(`<d:displayname>`)
		_, _ = sb.WriteString(e.escName)
		_, _ = sb.WriteString(`</d:displayname>`)

		_, _ = sb.WriteString(`</d:prop>`)
		_, _ = sb.WriteString(`<d:status>HTTP/1.1 200 OK</d:status>`)
		_, _ = sb.WriteString(`</d:propstat>`)
		_, _ = sb.WriteString(`</d:response>`)
	}

	// Close root element
	_, _ = sb.WriteString(`</d:multistatus>`)
	return sb
}

func writeXml(w http.ResponseWriter, status int, buf stringbuf.StringBuf) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

func hasHeadersWritten(w http.ResponseWriter) bool {
	// Most ResponseWriter implementations support this
	if hw, ok := w.(interface{ Written() bool }); ok {
		return hw.Written()
	}
	return false
}

func isClientDisconnection(err error) bool {
	if err == nil {
		return false
	}

	errStr := err.Error()
	// Common client disconnection error patterns
	return strings.Contains(errStr, "broken pipe") ||
		strings.Contains(errStr, "connection reset by peer") ||
		strings.Contains(errStr, "write: connection reset") ||
		strings.Contains(errStr, "read: connection reset") ||
		strings.Contains(errStr, "context canceled") ||
		strings.Contains(errStr, "context deadline exceeded") ||
		strings.Contains(errStr, "client disconnected") ||
		strings.Contains(errStr, "EOF")
}

type httpRange struct{ start, end int64 }

func parseRange(s string, size int64) ([]httpRange, error) {
	if s == "" {
		return nil, nil
	}
	const b = "bytes="
	if !strings.HasPrefix(s, b) {
		return nil, fmt.Errorf("invalid range")
	}

	var ranges []httpRange
	for _, ra := range strings.Split(s[len(b):], ",") {
		ra = strings.TrimSpace(ra)
		if ra == "" {
			continue
		}
		i := strings.Index(ra, "-")
		if i < 0 {
			return nil, fmt.Errorf("invalid range")
		}
		start, end := strings.TrimSpace(ra[:i]), strings.TrimSpace(ra[i+1:])
		var r httpRange
		if start == "" {
			i, err := strconv.ParseInt(end, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid range")
			}
			if i > size {
				i = size
			}
			r.start = size - i
			r.end = size - 1
		} else {
			i, err := strconv.ParseInt(start, 10, 64)
			if err != nil || i < 0 {
				return nil, fmt.Errorf("invalid range")
			}
			r.start = i
			if end == "" {
				r.end = size - 1
			} else {
				i, err := strconv.ParseInt(end, 10, 64)
				if err != nil || r.start > i {
					return nil, fmt.Errorf("invalid range")
				}
				if i >= size {
					i = size - 1
				}
				r.end = i
			}
		}
		if r.start > size-1 {
			continue
		}
		ranges = append(ranges, r)
	}
	return ranges, nil
}

func setVideoStreamingHeaders(req *http.Request) {
	// Request optimizations for faster response
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("User-Agent", "VideoStream/1.0")
	req.Header.Set("Priority", "u=1")
}
