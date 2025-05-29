package webdav

import (
	"github.com/stanNthe5/stringbuf"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

func isValidURL(str string) bool {
	u, err := url.Parse(str)
	// A valid URL should parse without error, and have a non-empty scheme and host.
	return err == nil && u.Scheme != "" && u.Host != ""
}

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

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000-07:00")
	entries := make([]entry, 0, len(children)+1)

	// Add the current file itself
	entries = append(entries, entry{
		escHref: xmlEscape(fastEscapePath(urlPath)),
		escName: xmlEscape(fi.Name()),
		isDir:   fi.IsDir(),
		size:    fi.Size(),
		modTime: fi.ModTime().Format("2006-01-02T15:04:05.000-07:00"),
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
			modTime: info.ModTime().Format("2006-01-02T15:04:05.000-07:00"),
		})
	}

	sb := builderPool.Get().(stringbuf.StringBuf)
	sb.Reset()
	defer builderPool.Put(sb)

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
