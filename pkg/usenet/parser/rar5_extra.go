package parser

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/sirrobot01/decypharr/internal/crypto"
)

type rar5Encryption struct {
	Encrypted bool
	Key, IV   []byte
}

// parseRAR5Extra reads each record within its declared size.
// Record sizes include the variable-length type field.
// Format: https://www.rarlab.com/technote.htm
func parseRAR5Extra(data []byte, password string) (rar5Encryption, error) {
	var encryption rar5Encryption
	for len(data) > 0 {
		size, n := binary.Uvarint(data)
		if n <= 0 {
			return rar5Encryption{}, fmt.Errorf("invalid RAR5 extra record size")
		}
		data = data[n:]
		if size == 0 || size > uint64(len(data)) {
			return rar5Encryption{}, io.ErrUnexpectedEOF
		}
		record := bytes.NewReader(data[:size])
		data = data[size:]
		kind, err := binary.ReadUvarint(record)
		if err != nil {
			return rar5Encryption{}, fmt.Errorf("RAR5 extra record type: %w", err)
		}
		if kind != RAR5ExtraTypeEncryption {
			continue
		}
		if encryption.Encrypted {
			return rar5Encryption{}, fmt.Errorf("duplicate RAR5 encryption record")
		}
		version, err := binary.ReadUvarint(record)
		if err != nil || version != 0 {
			return rar5Encryption{}, fmt.Errorf("invalid RAR5 encryption version")
		}
		flags, err := binary.ReadUvarint(record)
		if err != nil {
			return rar5Encryption{}, fmt.Errorf("RAR5 encryption flags: %w", err)
		}
		kdf, err := record.ReadByte()
		// Limit key derivation to 2^24 rounds per file.
		if err != nil || kdf > 24 {
			return rar5Encryption{}, fmt.Errorf("invalid RAR5 key derivation count")
		}
		var salt [16]byte
		if _, err := io.ReadFull(record, salt[:]); err != nil {
			return rar5Encryption{}, fmt.Errorf("RAR5 salt: %w", err)
		}
		iv := make([]byte, 16)
		if _, err := io.ReadFull(record, iv); err != nil {
			return rar5Encryption{}, fmt.Errorf("RAR5 IV: %w", err)
		}
		if flags&1 != 0 {
			var check [12]byte
			if _, err := io.ReadFull(record, check[:]); err != nil {
				return rar5Encryption{}, fmt.Errorf("RAR5 password check: %w", err)
			}
		}
		encryption.Encrypted = true
		encryption.IV = iv
		if password != "" {
			encryption.Key = crypto.DeriveKeys([]byte(password), salt[:], int(kdf)).Key
		}
	}
	return encryption, nil
}
