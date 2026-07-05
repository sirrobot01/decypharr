package webdav

import (
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/sirrobot01/decypharr/pkg/manager"
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

type entryItem struct {
	escHref string // already XML-safe + percent-escaped
	escName string
	size    int64
	isDir   bool
	modTime string
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
	for ra := range strings.SplitSeq(s[len(b):], ",") {
		ra = strings.TrimSpace(ra)
		if ra == "" {
			continue
		}
		before, after, ok := strings.Cut(ra, "-")
		if !ok {
			return nil, fmt.Errorf("invalid range")
		}
		start, end := strings.TrimSpace(before), strings.TrimSpace(after)
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

// Basic XML escaping function
func xmlEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func convertToXML(cleanPath string, currentInfo *manager.FileInfo, children []manager.FileInfo) stringbuf.StringBuf {
	entries := make([]entryItem, 0, len(children)+1)
	// AddOrUpdate the current file itself
	if currentInfo != nil {
		entries = append(entries, entryItem{
			escHref: xmlEscape(fastEscapePath(cleanPath)),
			escName: xmlEscape(currentInfo.Name()),
			isDir:   currentInfo.IsDir(),
			size:    currentInfo.Size(),
			modTime: currentInfo.ModTime().Format(time.RFC3339),
		})
	}

	for _, info := range children {

		nm := info.Name()
		// build raw href
		href := path.Join("/", cleanPath, nm)
		if info.IsDir() {
			href += "/"
		}

		entries = append(entries, entryItem{
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

	// AddOrUpdate responses for each entryItem
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
		_, _ = sb.WriteString(e.modTime)
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
