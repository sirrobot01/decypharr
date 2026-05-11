// Package crypto Package rar provides RAR5 encryption/decryption utilities.
// Implements AES-256-CBC decryption with PBKDF2-HMAC-SHA256 key derivation
// as specified in the RAR 5.0 archive format.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"io"
)

const (
	// BlockSize AES block size
	BlockSize = 16

	// AESKeySize Key sizes
	AESKeySize = 32 // AES-256

	// MaxPbkdf2Salt RAR5 constants
	MaxPbkdf2Salt = 64
	PwCheckSize   = 8
	MaxKdfCount   = 24
)

var (
	ErrBadPassword    = errors.New("rar: incorrect password")
	ErrInvalidKeySize = errors.New("rar: invalid key size")
	ErrInvalidIVSize  = errors.New("rar: invalid IV size")
	ErrInvalidData    = errors.New("rar: invalid encrypted data")
)

// DerivedKeys contains the keys derived from a password using RAR5's PBKDF2.
type DerivedKeys struct {
	Key      []byte // AES-256 key for decryption (32 bytes)
	CheckKey []byte // Key for checksum verification (32 bytes)
	PwCheck  []byte // Password verification value (12 bytes)
}

// DeriveKeys derives encryption keys from password using RAR5's PBKDF2-HMAC-SHA256.
// kdfCount is the log2 of iterations (actual iterations = 2^kdfCount).
// This implementation matches RAR5's calcKeys50 algorithm.
func DeriveKeys(password, salt []byte, kdfCount int) *DerivedKeys {
	if len(salt) > MaxPbkdf2Salt {
		salt = salt[:MaxPbkdf2Salt]
	}

	// Calculate actual iteration count
	iterations := 1 << uint(kdfCount)

	// Initialize HMAC with password
	prf := hmac.New(sha256.New, password)
	prf.Write(salt)
	prf.Write([]byte{0, 0, 0, 1}) // Counter = 1

	// Initial values
	t := prf.Sum(nil)
	u := make([]byte, len(t))
	copy(u, t)

	iterations--

	// Derive 3 keys with different iteration counts
	keys := make([][]byte, 3)
	iterCounts := []int{iterations, 16, 16}

	for i, iter := range iterCounts {
		for iter > 0 {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for j := range u {
				t[j] ^= u[j]
			}
			iter--
		}
		keys[i] = make([]byte, len(t))
		copy(keys[i], t)
	}

	// Build password check value
	pwcheck := make([]byte, len(keys[2]))
	copy(pwcheck, keys[2])

	// XOR fold the password check
	for i, v := range pwcheck[PwCheckSize:] {
		pwcheck[i&(PwCheckSize-1)] ^= v
	}
	pwcheck = pwcheck[:PwCheckSize]

	// Add SHA256 checksum (first 4 bytes)
	sum := sha256.Sum256(pwcheck)
	pwcheck = append(pwcheck, sum[:4]...)

	return &DerivedKeys{
		Key:      keys[0],
		CheckKey: keys[1],
		PwCheck:  pwcheck,
	}
}

// VerifyPassword checks if the password is correct by comparing password check values.
// expectedCheck is the 12-byte value stored in the encryption header.
func VerifyPassword(keys *DerivedKeys, expectedCheck []byte) bool {
	if len(expectedCheck) != len(keys.PwCheck) {
		return false
	}
	// Constant-time comparison
	var diff byte
	for i := range expectedCheck {
		diff |= expectedCheck[i] ^ keys.PwCheck[i]
	}
	return diff == 0
}

// NewDecrypter creates an AES-256-CBC decrypter with the given key and IV.
func NewDecrypter(key, iv []byte) (cipher.BlockMode, error) {
	if len(key) != AESKeySize {
		return nil, ErrInvalidKeySize
	}
	if len(iv) != BlockSize {
		return nil, ErrInvalidIVSize
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	return cipher.NewCBCDecrypter(block, iv), nil
}

// DecryptBlock decrypts a single block of data using AES-256-CBC.
// The data length must be a multiple of 16 (AES block size).
// Decryption is done in-place.
func DecryptBlock(data, key, iv []byte) error {
	if len(data)%BlockSize != 0 {
		return ErrInvalidData
	}

	mode, err := NewDecrypter(key, iv)
	if err != nil {
		return err
	}

	mode.CryptBlocks(data, data)
	return nil
}

// DecryptReader wraps an io.Reader with AES-256-CBC decryption.
type DecryptReader struct {
	r      io.Reader
	mode   cipher.BlockMode
	buf    []byte // Buffer for incomplete blocks
	outbuf []byte // Decrypted output buffer
	block  []byte // Single block buffer
}

// NewDecryptReader creates a new AES-256-CBC decrypting reader.
func NewDecryptReader(r io.Reader, key, iv []byte) (*DecryptReader, error) {
	mode, err := NewDecrypter(key, iv)
	if err != nil {
		return nil, err
	}

	return &DecryptReader{
		r:     r,
		mode:  mode,
		block: make([]byte, BlockSize),
	}, nil
}

// Read reads and decrypts data.
// Only full AES blocks are decrypted; trailing bytes are buffered.
func (d *DecryptReader) Read(p []byte) (int, error) {
	// Return buffered decrypted data first
	if len(d.outbuf) > 0 {
		n := copy(p, d.outbuf)
		d.outbuf = d.outbuf[n:]
		return n, nil
	}

	// Small reads: use block buffer
	if len(p) < BlockSize {
		// Read one full block
		l := len(d.buf)
		_, err := io.ReadFull(d.r, d.block[l:])
		if err != nil {
			return 0, err
		}
		if l > 0 {
			copy(d.block, d.buf)
			d.buf = nil
		}
		d.mode.CryptBlocks(d.block, d.block)
		n := copy(p, d.block)
		d.outbuf = d.block[n:]
		d.block = make([]byte, BlockSize) // New block buffer
		return n, nil
	}

	// Large reads: decrypt directly into p
	// Round down to block size
	toRead := len(p) - (len(p) % BlockSize)

	// Include any buffered partial block
	l := len(d.buf)
	if l > 0 {
		copy(p, d.buf)
		d.buf = nil
	}

	n, err := io.ReadAtLeast(d.r, p[l:toRead], BlockSize-l)
	if err != nil {
		return 0, err
	}

	n += l
	// Keep any incomplete block for next read
	remainder := n % BlockSize
	if remainder > 0 {
		d.buf = make([]byte, remainder)
		copy(d.buf, p[n-remainder:n])
		n -= remainder
	}

	if n > 0 {
		d.mode.CryptBlocks(p[:n], p[:n])
	}

	return n, nil
}

// EncryptionHeader contains RAR5 encryption header data.
type EncryptionHeader struct {
	Version    int    // Encryption version (should be 0)
	KdfCount   int    // Log2 of PBKDF2 iterations
	Salt       []byte // Salt for key derivation (16 bytes)
	PwCheck    []byte // Password verification value (12 bytes, optional)
	HasPwCheck bool   // Whether password check is present
}

// ParseEncryptionHeader parses a RAR5 encryption header.
// Format: version (vint) + flags (vint) + kdfCount (1 byte) + salt (16 bytes) + [pwCheck (12 bytes)]
func ParseEncryptionHeader(data []byte) (*EncryptionHeader, error) {
	if len(data) < 18 { // Minimum: version + flags + kdfCount + salt
		return nil, ErrInvalidData
	}

	// Read version (should be 0)
	version := int(data[0] & 0x7F)
	pos := 1
	if data[0]&0x80 != 0 {
		// Multi-byte vint, but version should be 0
		return nil, ErrInvalidData
	}

	// Read flags
	flags := int(data[pos] & 0x7F)
	pos++
	if data[pos-1]&0x80 != 0 {
		return nil, ErrInvalidData
	}

	// Read KDF count (1 byte)
	if pos >= len(data) {
		return nil, ErrInvalidData
	}
	kdfCount := int(data[pos])
	pos++

	// Read salt (16 bytes)
	if pos+16 > len(data) {
		return nil, ErrInvalidData
	}
	salt := make([]byte, 16)
	copy(salt, data[pos:pos+16])
	pos += 16

	header := &EncryptionHeader{
		Version:  version,
		KdfCount: kdfCount,
		Salt:     salt,
	}

	// Read password check if present (flag 0x0001)
	if flags&0x0001 != 0 {
		if pos+12 > len(data) {
			return nil, ErrInvalidData
		}
		header.PwCheck = make([]byte, 12)
		copy(header.PwCheck, data[pos:pos+12])
		header.HasPwCheck = true
	}

	return header, nil
}
