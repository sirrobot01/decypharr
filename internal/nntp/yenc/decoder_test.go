package yenc

import (
	"bytes"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

// yencEncode encodes raw bytes into a yEnc article body for testing. The
// =yend trailer carries a correct pcrc32, so every decode also exercises the
// decoder's CRC verification.
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
	buf.WriteString(fmt.Sprintf("=yend size=%d pcrc32=%08x\r\n", len(data), crc32.ChecksumIEEE(data)))

	return buf.String()
}

// bodyResponse wraps an encoded body in a complete BODY response: status
// line plus the ".\r\n" terminator, as the decoder sees it on the wire.
func bodyResponse(encoded string) string {
	return "222 0 <test@example> body\r\n" + encoded + ".\r\n"
}

func decodeResponse(t *testing.T, response string) (BodyResult, error) {
	t.Helper()
	return NewBodyDecoder(strings.NewReader(response), nil).Next()
}

func TestBodyDecoder_SimpleFile(t *testing.T) {
	original := []byte("Hello, this is a test of yEnc decoding!")
	res, err := decodeResponse(t, bodyResponse(yencEncode(original, "test.txt", 0, 0, 0)))
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	if res.StatusCode != 222 {
		t.Errorf("StatusCode = %d, want 222", res.StatusCode)
	}
	if !bytes.Equal(res.Data, original) {
		t.Errorf("Decoded data mismatch\n  got:  %q\n  want: %q", res.Data, original)
	}
	if res.Meta.FileName != "test.txt" {
		t.Errorf("FileName = %q, want %q", res.Meta.FileName, "test.txt")
	}
}

func TestBodyDecoder_BinaryData(t *testing.T) {
	// Test with all byte values 0-255
	original := make([]byte, 256)
	for i := range original {
		original[i] = byte(i)
	}
	res, err := decodeResponse(t, bodyResponse(yencEncode(original, "binary.bin", 0, 0, 0)))
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	if !bytes.Equal(res.Data, original) {
		t.Errorf("Binary decode mismatch: got %d bytes, want %d bytes", len(res.Data), len(original))
		for i := 0; i < len(res.Data) && i < len(original); i++ {
			if res.Data[i] != original[i] {
				t.Errorf("  first diff at byte %d: got 0x%02x, want 0x%02x", i, res.Data[i], original[i])
				break
			}
		}
	}
}

func TestBodyDecoder_MultipartMeta(t *testing.T) {
	original := []byte("Part one data here")
	res, err := decodeResponse(t, bodyResponse(yencEncode(original, "multipart.bin", 1, 1, int64(len(original)))))
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	if !bytes.Equal(res.Data, original) {
		t.Errorf("Decoded data mismatch\n  got:  %q\n  want: %q", res.Data, original)
	}
	if res.Meta.FileName != "multipart.bin" {
		t.Errorf("FileName = %q, want %q", res.Meta.FileName, "multipart.bin")
	}
	if res.Meta.PartNumber != 1 {
		t.Errorf("PartNumber = %d, want 1", res.Meta.PartNumber)
	}
	if res.Meta.Offset != 0 {
		t.Errorf("Offset = %d, want 0", res.Meta.Offset)
	}
	if res.Meta.PartSize != int64(len(original)) {
		t.Errorf("PartSize = %d, want %d", res.Meta.PartSize, len(original))
	}
}

func TestBodyDecoder_LargePayload(t *testing.T) {
	// Simulate a typical usenet segment (~750KB)
	original := make([]byte, 750*1024)
	for i := range original {
		original[i] = byte(i % 251) // prime to avoid patterns
	}
	res, err := decodeResponse(t, bodyResponse(yencEncode(original, "large.bin", 1, 1, int64(len(original)))))
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}

	if !bytes.Equal(res.Data, original) {
		t.Errorf("Large payload mismatch: got %d bytes, want %d bytes", len(res.Data), len(original))
	}
}

func TestBodyDecoder_SmallReads(t *testing.T) {
	original := []byte("Testing small source reads with yEnc decoder")
	response := bodyResponse(yencEncode(original, "small.txt", 0, 0, 0))

	// One byte per source Read exercises every buffer-boundary path.
	dec := NewBodyDecoder(iotest.OneByteReader(strings.NewReader(response)), nil)
	res, err := dec.Next()
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	if !bytes.Equal(res.Data, original) {
		t.Errorf("Small reads mismatch\n  got:  %q\n  want: %q", res.Data, original)
	}
}

func TestBodyDecoder_CrcMismatch(t *testing.T) {
	original := []byte("payload whose checksum will be broken")
	encoded := yencEncode(original, "crc.bin", 0, 0, 0)
	good := fmt.Sprintf("pcrc32=%08x", crc32.ChecksumIEEE(original))
	bad := "pcrc32=deadbeef"
	if !strings.Contains(encoded, good) {
		t.Fatal("encoded article missing expected pcrc32 trailer")
	}

	res, err := decodeResponse(t, bodyResponse(strings.Replace(encoded, good, bad, 1)))
	if !errors.Is(err, ErrCrcMismatch) {
		t.Fatalf("err = %v, want ErrCrcMismatch", err)
	}
	// Decoded bytes survive the CRC failure for inspection/repair.
	if !bytes.Equal(res.Data, original) {
		t.Errorf("data not preserved on CRC mismatch: got %d bytes, want %d", len(res.Data), len(original))
	}
}

func TestBodyDecoder_NonYencBody(t *testing.T) {
	res, err := decodeResponse(t, "222 0 <plain@example> body\r\njust some text\r\n.\r\n")
	if !errors.Is(err, ErrDataMissing) {
		t.Fatalf("err = %v, want ErrDataMissing", err)
	}
	if len(res.Data) != 0 {
		t.Errorf("Data = %d bytes, want none", len(res.Data))
	}
}

func TestBodyDecoder_ErrorStatus(t *testing.T) {
	res, err := decodeResponse(t, "430 no such article\r\n")
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	if res.StatusCode != 430 {
		t.Errorf("StatusCode = %d, want 430", res.StatusCode)
	}
	if res.Message != "430 no such article" {
		t.Errorf("Message = %q", res.Message)
	}
	if res.Data != nil {
		t.Errorf("Data = %d bytes, want nil", len(res.Data))
	}
}

func TestBodyDecoder_TruncatedStream(t *testing.T) {
	response := bodyResponse(yencEncode([]byte("cut short"), "trunc.bin", 0, 0, 0))
	// Drop the ".\r\n" terminator and the trailer.
	response = response[:len(response)-20]

	_, err := decodeResponse(t, response)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("err = %v, want io.ErrUnexpectedEOF", err)
	}
}

func TestBodyDecoder_Reuse(t *testing.T) {
	first := []byte("first article payload")
	second := []byte("second article with different content entirely")
	stream := bodyResponse(yencEncode(first, "one.bin", 1, 1, int64(len(first)))) +
		bodyResponse(yencEncode(second, "two.bin", 2, 1, int64(len(second))))

	dec := NewBodyDecoder(strings.NewReader(stream), nil)
	for i, want := range [][]byte{first, second} {
		res, err := dec.Next()
		if err != nil {
			t.Fatalf("Next %d failed: %v", i+1, err)
		}
		if !bytes.Equal(res.Data, want) {
			t.Errorf("article %d mismatch: got %q, want %q", i+1, res.Data, want)
		}
		if wantPart := int64(i + 1); res.Meta.PartNumber != wantPart {
			t.Errorf("article %d PartNumber = %d, want %d", i+1, res.Meta.PartNumber, wantPart)
		}
	}
}

func TestBodyDecoder_DataFunc(t *testing.T) {
	original := make([]byte, 64*1024)
	for i := range original {
		original[i] = byte(i % 253)
	}
	response := bodyResponse(yencEncode(original, "pooled.bin", 1, 1, int64(len(original))))

	supplied := make([]byte, 0, 1<<20)
	dec := NewBodyDecoder(strings.NewReader(response), func() []byte { return supplied })
	res, err := dec.Next()
	if err != nil {
		t.Fatalf("Next failed: %v", err)
	}
	if !bytes.Equal(res.Data, original) {
		t.Fatal("decoded data mismatch")
	}
	if &res.Data[0] != &supplied[:1][0] {
		t.Error("decoder did not decode into the supplied buffer")
	}
}
