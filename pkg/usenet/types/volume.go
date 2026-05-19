package types

import "github.com/sirrobot01/decypharr/pkg/storage"

type Volume struct {
	Index         int
	Name          string
	LocalPath     string
	Size          int64
	Segments      []storage.NZBSegment
	IsEncrypted   bool   // True if data is encrypted
	EncryptionKey []byte // AES-256 key for decryption (32 bytes)
	EncryptionIV  []byte // AES IV for decryption (16 bytes)
}

// RARVolumePart describes each archive part across volumes (internal parser use only)
type RARVolumePart struct {
	Name              string
	DataOffset        int64
	PackedSize        int64
	UnpackedSize      int64
	Stored            bool
	Compressed        bool
	PartNumber        int
	CompressionMethod string
}
