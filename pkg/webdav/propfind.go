package webdav

import (
	"context"
	"github.com/stanNthe5/stringbuf"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"
)

type contextKey string

const (
	// metadataOnlyKey is used to indicate that the request is for metadata only
	metadataOnlyKey contextKey = "metadataOnly"
)

func (h *Handler) handlePropfind(w http.ResponseWriter, r *http.Request) {
	// Setup context for metadata only
	ctx := context.WithValue(r.Context(), metadataOnlyKey, true)
	r = r.WithContext(ctx)

	cleanPath := path.Clean(r.URL.Path)

	// Build the list of entries
	type entry struct {
		escHref string // already XML-safe + percent-escaped
		escName string
		size    int64
		isDir   bool
		modTime string
	}

	// Always include the resource itself
	f, err := h.OpenFile(r.Context(), cleanPath, os.O_RDONLY, 0)
	if err != nil {
		h.logger.Error().Err(err).Str("path", cleanPath).Msg("Failed to open file")
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		h.logger.Error().Err(err).Msg("Failed to stat file")
		http.Error(w, "Server Error", http.StatusInternalServerError)
		return
	}

	var rawEntries []os.FileInfo
	if fi.IsDir() {
		rawEntries = append(rawEntries, h.getChildren(cleanPath)...)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05.000-07:00")
	entries := make([]entry, 0, len(rawEntries)+1)
	// Add the current file itself
	entries = append(entries, entry{
		escHref: xmlEscape(fastEscapePath(cleanPath)),
		escName: xmlEscape(fi.Name()),
		isDir:   fi.IsDir(),
		size:    fi.Size(),
		modTime: fi.ModTime().Format("2006-01-02T15:04:05.000-07:00"),
	})
	for _, info := range rawEntries {

		nm := info.Name()
		// build raw href
		href := path.Join("/", cleanPath, nm)
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

	// Set headers
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Vary", "Accept-Encoding")

	// Set status code and write response
	w.WriteHeader(http.StatusMultiStatus) // 207 MultiStatus
	_, _ = w.Write(sb.Bytes())
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
