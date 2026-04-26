package qbit

import (
	"strings"
	"testing"
)

// TestDecodeAuthHeader covers the fix for the slice-bounds-out-of-range panic
// at pkg/server/qbit/context.go:60-62. When the base64-decoded payload contains
// no colon, strings.LastIndex returns -1 and the subsequent slice expression
// `bearer[:colonIndex]` panics with "slice bounds out of range [:-1]".
//
// Pre-fix the "no colon" cases panic; post-fix they return a clean error and
// let the caller respond with 401 instead of crashing the request handler
// (chi's Recoverer middleware catches the panic, but the goroutine traceback
// is logged on every occurrence).
func TestDecodeAuthHeader(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		wantErr      bool
		wantUser     string
		wantPass     string
		mustNotPanic bool // documents the regression we're guarding against
	}{
		{
			name:     "well-formed Basic auth",
			header:   "Basic " + b64("alice:hunter2"),
			wantErr:  false,
			wantUser: "alice",
			wantPass: "hunter2",
		},
		{
			name:     "well-formed with colon in password",
			header:   "Basic " + b64("alice:hunt:er2"),
			wantErr:  false,
			wantUser: "alice:hunt", // strings.LastIndex => split on the last colon
			wantPass: "er2",
		},
		{
			// Empty payload — base64 of "" is "", decoded back is "". No colon.
			// PRE-FIX: panic.
			name:         "empty payload (the panic case)",
			header:       "Basic ",
			wantErr:      true,
			mustNotPanic: true,
		},
		{
			// Garbage that decodes successfully but has no colon.
			// PRE-FIX: panic.
			name:         "no-colon decoded bytes",
			header:       "Basic " + b64("just-a-token-no-colon"),
			wantErr:      true,
			mustNotPanic: true,
		},
		{
			// "Bearer xyz" splits to ["Bearer", "xyz"] (len == 2, so it doesn't
			// take the early-return path). "xyz" then fails base64 decoding
			// because the length isn't a multiple of 4 — surfaces as decode err.
			name:    "non-Basic scheme (token not valid base64)",
			header:  "Bearer xyz",
			wantErr: true,
		},
		{
			name:    "non-base64 payload",
			header:  "Basic !!!not-base64!!!",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					if tt.mustNotPanic {
						t.Fatalf("decodeAuthHeader panicked on %q: %v (regression — function must return an error, not panic)", tt.header, r)
					}
					panic(r)
				}
			}()

			user, pass, err := decodeAuthHeader(tt.header)

			if tt.wantErr && err == nil {
				t.Errorf("expected an error for header=%q, got nil (user=%q, pass=%q)", tt.header, user, pass)
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error for header=%q: %v", tt.header, err)
			}
			if tt.wantUser != "" && user != tt.wantUser {
				t.Errorf("user mismatch: got %q want %q", user, tt.wantUser)
			}
			if tt.wantPass != "" && pass != tt.wantPass {
				t.Errorf("pass mismatch: got %q want %q", pass, tt.wantPass)
			}
		})
	}
}

// b64 encodes a string as standard base64 with padding. Tiny helper so the
// test cases read like the wire format they represent.
func b64(s string) string {
	const alpha = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	src := []byte(s)
	var sb strings.Builder
	for i := 0; i < len(src); i += 3 {
		var buf [3]byte
		n := copy(buf[:], src[i:])
		sb.WriteByte(alpha[buf[0]>>2])
		sb.WriteByte(alpha[(buf[0]&0x03)<<4|buf[1]>>4])
		if n > 1 {
			sb.WriteByte(alpha[(buf[1]&0x0f)<<2|buf[2]>>6])
		} else {
			sb.WriteByte('=')
		}
		if n > 2 {
			sb.WriteByte(alpha[buf[2]&0x3f])
		} else {
			sb.WriteByte('=')
		}
	}
	return sb.String()
}
