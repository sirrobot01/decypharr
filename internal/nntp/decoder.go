package nntp

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// YencMetadata contains just the header information
type YencMetadata struct {
	Name     string // filename
	Size     int64  // total file size
	Part     int    // part number
	Total    int    // total parts
	Begin    int64  // part start byte
	End      int64  // part end byte
	LineSize int    // line length
}

// DecodeYencHeaders extracts only yenc header metadata without decoding body
func DecodeYencHeaders(reader io.Reader) (*YencMetadata, error) {
	buf := bufio.NewReader(reader)
	metadata := &YencMetadata{}

	// Find and parse =ybegin header
	if err := parseYBeginHeader(buf, metadata); err != nil {
		return nil, NewYencDecodeError(fmt.Errorf("failed to parse ybegin header: %w", err))
	}

	// Parse =ypart header if this is a multipart file
	if metadata.Part > 0 {
		if err := parseYPartHeader(buf, metadata); err != nil {
			return nil, NewYencDecodeError(fmt.Errorf("failed to parse ypart header: %w", err))
		}
	}

	return metadata, nil
}

func parseYBeginHeader(buf *bufio.Reader, metadata *YencMetadata) error {
	var s string
	var err error

	// Find the =ybegin line
	for {
		s, err = buf.ReadString('\n')
		if err != nil {
			return err
		}
		if len(s) >= 7 && s[:7] == "=ybegin" {
			break
		}
	}

	// Parse the header line
	parts := strings.SplitN(s[7:], "name=", 2)
	if len(parts) > 1 {
		metadata.Name = strings.TrimSpace(parts[1])
	}

	// Parse other parameters
	for _, header := range strings.Split(parts[0], " ") {
		kv := strings.SplitN(strings.TrimSpace(header), "=", 2)
		if len(kv) < 2 {
			continue
		}

		switch kv[0] {
		case "size":
			metadata.Size, _ = strconv.ParseInt(kv[1], 10, 64)
		case "line":
			metadata.LineSize, _ = strconv.Atoi(kv[1])
		case "part":
			metadata.Part, _ = strconv.Atoi(kv[1])
		case "total":
			metadata.Total, _ = strconv.Atoi(kv[1])
		}
	}

	return nil
}

func parseYPartHeader(buf *bufio.Reader, metadata *YencMetadata) error {
	var s string
	var err error

	// Find the =ypart line
	for {
		s, err = buf.ReadString('\n')
		if err != nil {
			return err
		}
		if len(s) >= 6 && s[:6] == "=ypart" {
			break
		}
	}

	// Parse part parameters
	for _, header := range strings.Split(s[6:], " ") {
		kv := strings.SplitN(strings.TrimSpace(header), "=", 2)
		if len(kv) < 2 {
			continue
		}

		switch kv[0] {
		case "begin":
			metadata.Begin, _ = strconv.ParseInt(kv[1], 10, 64)
		case "end":
			metadata.End, _ = strconv.ParseInt(kv[1], 10, 64)
		}
	}

	return nil
}
