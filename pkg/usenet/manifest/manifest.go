// Package manifest decodes NZB XML into a network-independent description of
// its posted files and segments.
package manifest

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Tensai75/subjectparser"
	"golang.org/x/net/html/charset"
)

// Manifest is the local, network-independent representation of an NZB.
// Analyzer stages treat a Manifest as immutable after Decode returns.
type Manifest struct {
	Comment  string
	Metadata map[string]string
	Files    []File
	Stats    Statistics
}

// Statistics describes the topology reported by the NZB document. Bytes are
// the encoded article byte counts from the XML, not decoded yEnc sizes.
type Statistics struct {
	TotalFiles        int
	AvailableSegments int
	TotalSegments     int
	Bytes             int64
}

// File describes one posted file after the compatibility subject parser has
// derived its filename, base filename, and file number.
type File struct {
	Order         int
	Groups        []string
	Segments      []Segment
	Poster        string
	Date          int
	Subject       string
	Number        int
	Filename      string
	BaseFilename  string
	TotalSegments int
	Bytes         int64
}

// Segment is one article reference from an NZB file entry. Bytes is the
// encoded size reported by the NZB document.
type Segment struct {
	Bytes     int64  `xml:"bytes,attr"`
	Number    int    `xml:"number,attr"`
	MessageID string `xml:",innerxml"`
}

type rawFile struct {
	Groups   []string  `xml:"groups>group"`
	Segments []Segment `xml:"segments>segment"`
	Poster   string    `xml:"poster,attr"`
	Date     int       `xml:"date,attr"`
	Subject  string    `xml:"subject,attr"`
}

type rawMetadata struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",innerxml"`
}

type rawHead struct {
	Metadata []rawMetadata `xml:"meta"`
}

// Decode parses an NZB in one streaming pass without retaining a duplicate XML
// object graph. Duplicate and subject-derived fields intentionally match the
// historic nzbparser contract used by the rest of the application.
func Decode(reader io.Reader) (*Manifest, error) {
	if reader == nil {
		return nil, errors.New("decode NZB manifest: reader is nil")
	}

	decoder := xml.NewDecoder(reader)
	decoder.CharsetReader = charset.NewReaderLabel
	decoder.Strict = false
	manifest := &Manifest{Metadata: make(map[string]string)}
	fileBySubject := make(map[string]int)
	rootSeen := false
	rootClosed := false
	depth := 0

	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode NZB manifest: unable to parse NZB file: %w", err)
		}

		switch value := token.(type) {
		case xml.StartElement:
			if !rootSeen {
				if value.Name.Local != "nzb" {
					return nil, fmt.Errorf("decode NZB manifest: expected element type <nzb> but have <%s>", value.Name.Local)
				}
				rootSeen = true
				depth = 1
				continue
			}
			if rootClosed {
				return nil, fmt.Errorf("decode NZB manifest: unexpected element <%s> after </nzb>", value.Name.Local)
			}
			if depth == 1 && value.Name.Local == "file" {
				var raw rawFile
				if err := decoder.DecodeElement(&raw, &value); err != nil {
					return nil, fmt.Errorf("decode NZB manifest: unable to parse NZB file: %w", err)
				}
				appendRawFile(manifest, fileBySubject, raw)
				continue
			}
			if depth == 1 && value.Name.Local == "head" {
				var head rawHead
				if err := decoder.DecodeElement(&head, &value); err != nil {
					return nil, fmt.Errorf("decode NZB manifest: unable to parse NZB metadata: %w", err)
				}
				for _, metadata := range head.Metadata {
					manifest.Metadata[metadata.Type] = metadata.Value
				}
				continue
			}
			depth++
		case xml.EndElement:
			if rootSeen && depth == 1 && value.Name.Local == "nzb" {
				rootClosed = true
				depth = 0
				continue
			}
			if depth > 0 {
				depth--
			}
		case xml.Comment:
			if rootSeen && !rootClosed && depth == 1 {
				manifest.Comment += string(value)
			}
		}
	}

	if !rootSeen || !rootClosed {
		return nil, fmt.Errorf("decode NZB manifest: unable to parse NZB file: unexpected EOF")
	}
	finalize(manifest)
	return manifest, nil
}

func appendRawFile(manifest *Manifest, fileBySubject map[string]int, raw rawFile) {
	if index, ok := fileBySubject[raw.Subject]; ok {
		manifest.Files[index].Segments = append(manifest.Files[index].Segments, raw.Segments...)
		return
	}
	fileBySubject[raw.Subject] = len(manifest.Files)
	manifest.Files = append(manifest.Files, File{
		Groups:   raw.Groups,
		Segments: raw.Segments,
		Poster:   raw.Poster,
		Date:     raw.Date,
		Subject:  raw.Subject,
	})
}

func finalize(manifest *Manifest) {
	maxTotalFiles := 0
	for fileIndex := range manifest.Files {
		file := &manifest.Files[fileIndex]
		unique := file.Segments[:0]
		seen := make(map[string]struct{}, len(file.Segments))
		for _, segment := range file.Segments {
			// innerxml keeps the element's raw text, so a pretty-printed NZB
			// yields ids wrapped in newlines and indentation. Sent to NNTP
			// verbatim those come back as missing articles, which reads as a
			// dead post rather than a formatting problem. Trim before the
			// dedup key is taken so whitespace variants collapse together.
			segment.MessageID = strings.TrimSpace(segment.MessageID)
			if _, exists := seen[segment.MessageID]; exists {
				continue
			}
			seen[segment.MessageID] = struct{}{}
			unique = append(unique, segment)
		}
		file.Segments = unique

		totalSegments := 0
		if subject, err := subjectparser.Parse(file.Subject); err == nil {
			file.Number = subject.File
			file.Filename = subject.Filename
			file.BaseFilename = subject.Basefilename
			totalSegments = subject.TotalSegments
			maxTotalFiles = max(maxTotalFiles, subject.TotalFiles)
		}
		for _, segment := range file.Segments {
			totalSegments = max(totalSegments, segment.Number)
			file.Bytes += segment.Bytes
			manifest.Stats.Bytes += segment.Bytes
		}
		file.TotalSegments = totalSegments
		manifest.Stats.AvailableSegments += len(file.Segments)
		manifest.Stats.TotalSegments += totalSegments
	}

	manifest.Stats.TotalFiles = max(maxTotalFiles, len(manifest.Files))
	// Stable: a subject the subject parser cannot read still parses as file 1
	// of 1, so every obfuscated file in a release carries the same Number and
	// they all tie here. An unstable sort ordered those arbitrarily and
	// file.Order below made that order permanent - the same shape as the
	// wrong-order assembly that serves a garbage head. Document order is the
	// tiebreak.
	sort.SliceStable(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Number < manifest.Files[j].Number
	})
	for fileIndex := range manifest.Files {
		file := &manifest.Files[fileIndex]
		sort.SliceStable(file.Segments, func(i, j int) bool {
			return file.Segments[i].Number < file.Segments[j].Number
		})
		file.Order = fileIndex
	}
}
