package yenc

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"testing"
)

// yencEncode encodes raw bytes into yEnc format for testing.
func yencEncode(data []byte, name string, partNum int, begin, end int64) string {
	var buf bytes.Buffer

	// =ybegin header
	if partNum > 0 {
		buf.WriteString(fmt.Sprintf("=ybegin part=%d line=128 size=%d name=%s\r\n", partNum, len(data), name))
		buf.WriteString(fmt.Sprintf("=ypart begin=%d end=%d\r\n", begin, end))
	} else {
		buf.WriteString(fmt.Sprintf("=ybegin line=128 size=%d name=%s\r\n", len(data), name))
	}

	// Encode body
	col := 0
	for _, b := range data {
		encoded := (b + 42) & 0xFF
		// Escape special characters: NUL, LF, CR, '=', TAB, SPACE, '.'
		if encoded == 0 || encoded == '\n' || encoded == '\r' || encoded == '=' || encoded == '\t' || encoded == ' ' || encoded == '.' {
			buf.WriteByte('=')
			buf.WriteByte((encoded + 64) & 0xFF)
			col += 2
		} else {
			buf.WriteByte(encoded)
			col++
		}
		if col >= 128 {
			buf.WriteString("\r\n")
			col = 0
		}
	}
	if col > 0 {
		buf.WriteString("\r\n")
	}

	// =yend trailer
	buf.WriteString(fmt.Sprintf("=yend size=%d\r\n", len(data)))

	return buf.String()
}

func TestDecoder_SimpleFile(t *testing.T) {
	original := []byte("Hello, this is a test of yEnc decoding!")
	encoded := yencEncode(original, "test.txt", 0, 0, 0)

	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	decoded, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(decoded, original) {
		t.Errorf("Decoded data mismatch\n  got:  %q\n  want: %q", decoded, original)
	}

	if dec.Meta.FileName != "test.txt" {
		t.Errorf("FileName = %q, want %q", dec.Meta.FileName, "test.txt")
	}
}

func TestDecoder_BinaryData(t *testing.T) {
	// Test with all byte values 0-255
	original := make([]byte, 256)
	for i := range original {
		original[i] = byte(i)
	}
	encoded := yencEncode(original, "binary.bin", 0, 0, 0)

	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	decoded, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(decoded, original) {
		t.Errorf("Binary decode mismatch: got %d bytes, want %d bytes", len(decoded), len(original))
		// Show first difference
		for i := 0; i < len(decoded) && i < len(original); i++ {
			if decoded[i] != original[i] {
				t.Errorf("  first diff at byte %d: got 0x%02x, want 0x%02x", i, decoded[i], original[i])
				break
			}
		}
	}
}

func TestDecoder_MultipartMeta(t *testing.T) {
	original := []byte("Part one data here")
	encoded := yencEncode(original, "multipart.bin", 1, 1, int64(len(original)))

	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	decoded, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(decoded, original) {
		t.Errorf("Decoded data mismatch\n  got:  %q\n  want: %q", decoded, original)
	}

	if dec.Meta.FileName != "multipart.bin" {
		t.Errorf("FileName = %q, want %q", dec.Meta.FileName, "multipart.bin")
	}
	if dec.Meta.PartNumber != 1 {
		t.Errorf("PartNumber = %d, want 1", dec.Meta.PartNumber)
	}
	if dec.Meta.Offset != 0 {
		t.Errorf("Offset = %d, want 0", dec.Meta.Offset)
	}
	if dec.Meta.PartSize != int64(len(original)) {
		t.Errorf("PartSize = %d, want %d", dec.Meta.PartSize, len(original))
	}
}

func TestDecoder_LargePayload(t *testing.T) {
	// Simulate a typical usenet segment (~750KB)
	original := make([]byte, 750*1024)
	for i := range original {
		original[i] = byte(i % 251) // prime to avoid patterns
	}
	encoded := yencEncode(original, "large.bin", 1, 1, int64(len(original)))

	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	decoded, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}

	if !bytes.Equal(decoded, original) {
		t.Errorf("Large payload mismatch: got %d bytes, want %d bytes", len(decoded), len(original))
	}
}

func TestDecoder_SmallReads(t *testing.T) {
	original := []byte("Testing small buffer reads with yEnc decoder")
	encoded := yencEncode(original, "small.txt", 0, 0, 0)

	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	// Read one byte at a time
	var result []byte
	buf := make([]byte, 1)
	for {
		n, err := dec.Read(buf)
		if n > 0 {
			result = append(result, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
	}

	if !bytes.Equal(result, original) {
		t.Errorf("Small reads mismatch\n  got:  %q\n  want: %q", result, original)
	}
}

func TestDecoder_ForcePureGo(t *testing.T) {
	original := UsePureGo
	UsePureGo = true
	defer func() { UsePureGo = original }()

	encoded := yencEncode([]byte("force pure go"), "force.txt", 0, 0, 0)
	dec := AcquireDecoder(strings.NewReader(encoded))
	defer ReleaseDecoder(dec)

	if _, ok := dec.Reader.(*pureGoYencDecoder); !ok {
		t.Fatalf("expected pure-Go yEnc decoder, got %T", dec.Reader)
	}
}

func TestDecoder_NNTPTerminator(t *testing.T) {
	original := []byte("NNTP terminator test")
	const nextResponse = "222 next response\r\n"
	source := &oneByteReader{reader: strings.NewReader(
		yencEncode(original, "nntp.txt", 0, 0, 0) + ".\r\n" + nextResponse,
	)}

	dec := AcquireDecoder(source)
	defer ReleaseDecoder(dec)

	decoded, err := io.ReadAll(dec)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	if !bytes.Equal(decoded, original) {
		t.Errorf("Decoded data mismatch\n  got:  %q\n  want: %q", decoded, original)
	}

	// A BODY decoder must consume NNTP's dot line, but no bytes from the
	// following response. Limiting the source to one byte per Read prevents
	// buffering from hiding either boundary error.
	remainder, err := io.ReadAll(source)
	if err != nil {
		t.Fatalf("read source remainder: %v", err)
	}
	if string(remainder) != nextResponse {
		t.Fatalf("source remainder = %q, want %q", remainder, nextResponse)
	}
}

type oneByteReader struct {
	reader io.Reader
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.reader.Read(p)
}
