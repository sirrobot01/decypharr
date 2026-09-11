// Package yenc adapts the rapidyenc NNTP/yEnc decoder for the connection
// layer: whole-response batch decoding with caller-supplied output buffers.
package yenc

import (
	"io"

	"github.com/mnightingale/rapidyenc"
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

// DecoderMeta holds the yEnc header metadata parsed from one article.
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

// Decode errors surfaced by Next, re-exported so callers need not import
// rapidyenc. All three fire after the full response has been consumed, so
// the underlying connection is left at a clean response boundary.
var (
	ErrDataMissing    = rapidyenc.ErrDataMissing
	ErrDataCorruption = rapidyenc.ErrDataCorruption
	ErrCrcMismatch    = rapidyenc.ErrCrcMismatch
)

// BodyResult is one fully decoded NNTP body response.
type BodyResult struct {
	// Data is the decoded article payload. When the decoder was built with a
	// dataFunc the slice comes from it and the caller decides whether to
	// recycle it. Nil when the response was not multiline (see StatusCode).
	Data []byte
	Meta DecoderMeta
	// StatusCode and Message are the parsed NNTP status line of the
	// response. StatusCode 0 means the status line was never read.
	StatusCode int
	Message    string
}

// BodyDecoder decodes complete NNTP responses (status line included) from a
// stream. It keeps a reusable read buffer across responses, so hold one per
// connection. It must only see a strictly sequential command/response stream:
// its internal buffer would swallow any bytes sent ahead of the next command.
type BodyDecoder struct {
	dec *rapidyenc.Decoder
}

// NewBodyDecoder returns a BodyDecoder reading from r. dataFunc, when
// non-nil, supplies output buffers (for example from a sync.Pool); the
// decoder takes one per response and hands it back as BodyResult.Data.
func NewBodyDecoder(r io.Reader, dataFunc func() []byte) *BodyDecoder {
	if dataFunc == nil {
		return &BodyDecoder{dec: rapidyenc.NewDecoder(r)}
	}
	return &BodyDecoder{dec: rapidyenc.NewDecoder(r, rapidyenc.WithDataFunc(dataFunc))}
}

// Next reads one complete NNTP response and returns its decoded body. On
// decode errors (CRC mismatch, corruption, missing yEnc headers) the result
// still carries any partially decoded data so the caller can recycle the
// buffer or inspect the bytes.
func (d *BodyDecoder) Next() (BodyResult, error) {
	resp, err := d.dec.Next()
	if resp == nil {
		return BodyResult{}, err
	}
	m := resp.Metadata
	return BodyResult{
		Data:       resp.Data,
		StatusCode: m.StatusCode,
		Message:    m.Message,
		Meta: DecoderMeta{
			FileName:   m.FileName,
			FileSize:   m.FileSize,
			PartNumber: m.PartNumber,
			TotalParts: m.TotalParts,
			Offset:     m.Offset,
			PartSize:   m.PartSize,
		},
	}, err
}
