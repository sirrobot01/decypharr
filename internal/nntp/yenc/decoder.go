package yenc

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strconv"
	"strings"
)

// Metadata contains yEnc header information and snippet bytes.
type Metadata struct {
	Name     string // filename
	Size     int64  // total file size
	Part     int64  // part number
	Total    int64  // total parts
	Begin    int64  // part start byte
	End      int64  // part end byte
	Offset   int64  // part offset within the file
	PartSize int64  // part size (decoded)
	LineSize int    // line length
	Snippet  []byte
}

// UsePureGo forces the pure-Go yEnc decoder even when CGO/rapidyenc is available.
// Set via YENC_PURE_GO=true environment variable.
var UsePureGo = os.Getenv("YENC_PURE_GO") == "true"

// Decoder wraps an io.Reader that decodes yEnc-encoded data on the fly.
// After reading, Meta contains the parsed yEnc header metadata.
type Decoder struct {
	io.Reader
	Meta DecoderMeta
}

// DecoderMeta holds yEnc header metadata, matching the fields from rapidyenc.Meta.
type DecoderMeta struct {
	FileName   string
	FileSize   int64
	PartNumber int64
	TotalParts int64
	Offset     int64
	PartSize   int64
}

// Begin returns the "=ypart begin" value calculated from the Offset.
func (m DecoderMeta) Begin() int64 {
	return m.Offset + 1
}

// End returns the "=ypart end" value calculated from the Offset and PartSize.
func (m DecoderMeta) End() int64 {
	return m.Offset + m.PartSize
}

// pureGoYencDecoder is a streaming yEnc decoder implemented in pure Go.
// It consumes yEnc control lines and decodes payload bytes incrementally.
type pureGoYencDecoder struct {
	r        *bufio.Reader
	meta     *DecoderMeta
	out      []byte
	outPos   int
	scratch  []byte // reused buffer for lines longer than the bufio buffer (rare)
	sawBegin bool
	sawEnd   bool
	done     bool
}

func newPureGoYencDecoder(r io.Reader, meta *DecoderMeta) *pureGoYencDecoder {
	return &pureGoYencDecoder{
		r:    bufio.NewReaderSize(r, 64*1024),
		meta: meta,
		out:  make([]byte, 0, 8*1024),
	}
}

func (d *pureGoYencDecoder) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	n := 0
	for n < len(p) {
		if d.outPos < len(d.out) {
			copied := copy(p[n:], d.out[d.outPos:])
			d.outPos += copied
			n += copied
			if d.outPos == len(d.out) {
				d.outPos = 0
				d.out = d.out[:0]
			}
			continue
		}

		if d.done {
			if n > 0 {
				return n, nil
			}
			return 0, io.EOF
		}

		err := d.fill()
		if err != nil {
			if err == io.EOF {
				d.done = true
				if n > 0 {
					return n, nil
				}
				return 0, io.EOF
			}
			if n > 0 {
				return n, err
			}
			return 0, err
		}
	}

	return n, nil
}

// readLine returns the next line including its trailing '\n' without allocating
// per line: ReadSlice returns a slice into the bufio buffer (valid only until
// the next read, which is fine since processLine consumes it immediately). A
// line longer than the 64 KiB bufio buffer — not expected for valid yEnc, whose
// lines are ~128 bytes — falls back to a reused scratch buffer, so steady state
// stays zero-alloc. This replaces ReadBytes, which allocated a fresh slice for
// every line (~6k allocations per article).
func (d *pureGoYencDecoder) readLine() ([]byte, error) {
	line, err := d.r.ReadSlice('\n')
	if err != bufio.ErrBufferFull {
		return line, err
	}
	d.scratch = append(d.scratch[:0], line...)
	for err == bufio.ErrBufferFull {
		line, err = d.r.ReadSlice('\n')
		d.scratch = append(d.scratch, line...)
	}
	return d.scratch, err
}

func (d *pureGoYencDecoder) fill() error {
	d.out = d.out[:0]
	d.outPos = 0

	for {
		line, err := d.readLine()
		if err != nil && err != io.EOF {
			return err
		}

		if len(line) > 0 {
			done, lineErr := d.processLine(line)
			if lineErr != nil {
				return lineErr
			}
			if len(d.out) > 0 {
				return nil
			}
			if done {
				return io.EOF
			}
		}

		if err == io.EOF {
			return io.EOF
		}
	}
}

func (d *pureGoYencDecoder) processLine(line []byte) (bool, error) {
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return false, nil
	}

	// Handle raw NNTP article termination / dot-stuffing when present.
	if line[0] == '.' {
		if len(line) == 1 {
			return true, nil
		}
		if line[1] == '.' {
			line = line[1:]
		}
	}

	switch {
	case bytes.HasPrefix(line, []byte("=ybegin ")):
		d.sawBegin = true
		parseYBeginLine(line, d.meta)
		return false, nil
	case bytes.HasPrefix(line, []byte("=ypart ")):
		parseYPartLine(line, d.meta)
		return false, nil
	case bytes.HasPrefix(line, []byte("=yend ")):
		parseYEndLine(line, d.meta)
		// The yEnc payload ends here, but an NNTP BODY response ends on the
		// following dot line. Keep consuming until that terminator so a pooled
		// connection is aligned for its next command.
		d.sawEnd = true
		return false, nil
	}

	if !d.sawBegin {
		return false, nil
	}
	if d.sawEnd {
		return false, nil
	}

	// Decode with escape handling.
	d.out = d.out[:0]
	escaped := false
	for _, b := range line {
		if escaped {
			d.out = append(d.out, b-64-42)
			escaped = false
			continue
		}
		if b == '=' {
			escaped = true
			continue
		}
		d.out = append(d.out, b-42)
	}

	return false, nil
}

func parseYBeginLine(line []byte, meta *DecoderMeta) {
	s := strings.TrimSpace(string(line))
	rest := strings.TrimSpace(strings.TrimPrefix(s, "=ybegin "))

	if idx := strings.Index(rest, " name="); idx >= 0 {
		meta.FileName = rest[idx+6:]
		rest = strings.TrimSpace(rest[:idx])
	}

	for field := range strings.FieldsSeq(rest) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "size":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				meta.FileSize = n
			}
		case "part":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				meta.PartNumber = n
			}
		case "total":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				meta.TotalParts = n
			}
		}
	}

	// Single-part posts often omit =ypart; use full file size as part size.
	if meta.PartNumber == 0 {
		meta.Offset = 0
		meta.PartSize = meta.FileSize
	}
}

func parseYPartLine(line []byte, meta *DecoderMeta) {
	s := strings.TrimSpace(string(line))
	rest := strings.TrimSpace(strings.TrimPrefix(s, "=ypart "))

	var begin int64
	var end int64
	for field := range strings.FieldsSeq(rest) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "begin":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				begin = n
			}
		case "end":
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				end = n
			}
		}
	}

	if begin > 0 {
		meta.Offset = begin - 1
	}
	if begin > 0 && end >= begin {
		meta.PartSize = end - meta.Offset
	}
}

func parseYEndLine(line []byte, meta *DecoderMeta) {
	s := strings.TrimSpace(string(line))
	rest := strings.TrimSpace(strings.TrimPrefix(s, "=yend "))

	for field := range strings.FieldsSeq(rest) {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		if k != "size" {
			continue
		}
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			meta.PartSize = n
		}
	}
}

func acquirePureGoDecoder(r io.Reader) *Decoder {
	yd := &Decoder{}
	yd.Reader = newPureGoYencDecoder(r, &yd.Meta)
	return yd
}
