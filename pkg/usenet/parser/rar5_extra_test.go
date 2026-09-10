package parser

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

func TestRAR5ExtraRecordBoundaries(t *testing.T) {
	payload := append([]byte{0, 0, 1}, make([]byte, 16)...)
	iv := bytes.Repeat([]byte{7}, 16)
	payload = append(payload, iv...)
	record := append([]byte{36, RAR5ExtraTypeEncryption}, payload...)
	withCheck := bytes.Clone(record)
	withCheck[0], withCheck[3] = 48, 1
	withCheck = append(withCheck, make([]byte, 12)...)
	badVersion := bytes.Clone(record)
	badVersion[2] = 1
	badKDF := bytes.Clone(record)
	badKDF[4] = 255
	for _, test := range []struct {
		name             string
		data             []byte
		encrypted, valid bool
	}{
		{"empty", nil, false, true},
		{"type only", []byte{1, 7}, false, true},
		{"multibyte unknown then encryption", append([]byte{2, 0x80, 1}, record...), true, true},
		{"encryption", record, true, true},
		{"password check", withCheck, true, true},
		{"truncated size", []byte{0x80}, false, false},
		{"oversized record", []byte{100, 1}, false, false},
		{"zero record", []byte{0}, false, false},
		{"truncated type", []byte{1, 0x80}, false, false},
		{"truncated IV", record[:len(record)-1], false, false},
		{"truncated check", withCheck[:len(withCheck)-1], false, false},
		{"unsupported version", badVersion, false, false},
		{"unbounded KDF", badKDF, false, false},
		{"duplicate encryption", append(bytes.Clone(record), record...), false, false},
		// The next record must not supply bytes missing from this record's IV.
		{"short record followed by data", append([]byte{4, 1, 0, 0, 1, 33, 7}, make([]byte, 32)...), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := parseRAR5Extra(test.data, "password")
			if (err == nil) != test.valid {
				t.Fatalf("result=%#v error=%v", result, err)
			}
			if !test.valid {
				return
			}
			if result.Encrypted != test.encrypted {
				t.Fatalf("encrypted = %v", result.Encrypted)
			}
			if test.encrypted {
				if !bytes.Equal(result.IV, iv) {
					t.Fatalf("IV = %x", result.IV)
				}
				if hex.EncodeToString(result.Key) != "9e41dea5935137cf748669cbf242b3ea049d20e0add3db231dabc9840bc58a6a" {
					t.Fatalf("key = %x", result.Key)
				}
			}
		})
	}
}

func TestRAR5HeaderSizesAndCompression(t *testing.T) {
	for _, name := range []string{"movie.mkv", "movies1.mkv", strings.Repeat("a", 130) + ".mkv"} {
		for _, method := range []uint64{0, 1, 5} {
			extra := append([]byte{2, 0x80, 1, 36, 1, 0, 0, 1}, make([]byte, 32)...) // Unknown type, then encryption.
			var data []byte
			for _, value := range []uint64{0, 100, 0, method << 7, 1, uint64(len(name))} {
				data = binary.AppendUvarint(data, value)
			}
			data = append(data, name...)
			data = append(data, extra...)
			var content []byte
			for _, value := range []uint64{RAR5HeaderTypeFile, RAR5HeaderFlagExtraArea | RAR5HeaderFlagDataArea, uint64(len(extra)), 100} {
				content = binary.AppendUvarint(content, value)
			}
			content = append(content, data...)
			raw := binary.AppendUvarint(make([]byte, 4), uint64(len(content)))
			raw = append(raw, content...)
			parser := &RARParser{}
			reader := bytes.NewReader(append(bytes.Clone(raw), 0xaa))
			header, size, packed, err := parser.readRAR5Header(reader)
			if err != nil || size != len(raw) || packed != 100 || reader.Len() != 1 {
				t.Fatalf("header size=%d packed=%d remaining=%d error=%v", size, packed, reader.Len(), err)
			}
			stream := &rarReader{ctx: t.Context(), currentSegmentData: append(bytes.Clone(raw), 0xaa)}
			streamHeader, streamSize, _, err := parser.readRAR5HeaderFromStream(stream)
			if err != nil || streamSize != size || !bytes.Equal(streamHeader.Data, header.Data) {
				t.Fatalf("stream header size=%d error=%v", streamSize, err)
			}
			entry := parser.parseRAR5FileHeader(header.Data, header.ExtraSize, 0, "part.rar", int64(size), packed, "")
			if entry == nil || entry.Name != name || entry.IsStored != (method == 0) || !entry.IsEncrypted || len(entry.EncryptionIV) != 16 || entry.EncryptionKey != nil {
				t.Fatalf("method=%d entry=%#v", method, entry)
			}
			// Exercise encrypted headers at both one-byte and two-byte size fields.
			key, iv := make([]byte, 32), make([]byte, 16)
			padded := append(bytes.Clone(raw), make([]byte, (aes.BlockSize-len(raw)%aes.BlockSize)%aes.BlockSize)...)
			block, err := aes.NewCipher(key)
			if err != nil {
				t.Fatal(err)
			}
			cipher.NewCBCEncrypter(block, iv).CryptBlocks(padded, padded)
			stream = &rarReader{ctx: t.Context(), currentSegmentData: padded}
			decrypted, encryptedSize, _, err := parser.readAndDecryptRAR5Header(stream, key, iv)
			if err != nil || encryptedSize != len(padded) || !bytes.Equal(decrypted.Data, header.Data) || decrypted.ExtraSize != header.ExtraSize {
				t.Fatalf("encrypted header size=%d error=%v", encryptedSize, err)
			}
		}
	}
}
