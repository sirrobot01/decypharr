package usenet

import (
	"bytes"
	"testing"
)

// pad extends a head to the read size the verifier sees, filling with a
// non-zero byte so stride checks can't pass by accident.
func pad(head []byte) []byte {
	out := make([]byte, verifyHeadBytes)
	for i := range out {
		out[i] = 0xAB
	}
	copy(out, head)
	return out
}

func TestHeadSignatureOK(t *testing.T) {
	mp4 := pad(nil)
	copy(mp4[4:], "ftyp")

	// M2TS: 0x47 at 4, 196, 388 (192-byte packets with 4-byte prefix).
	m2ts := pad(nil)
	for _, off := range []int{4, 196, 388} {
		m2ts[off] = 0x47
	}
	// Plain TS: 0x47 at 0, 188, 376.
	ts := pad(nil)
	for _, off := range []int{0, 188, 376} {
		ts[off] = 0x47
	}
	// Lone sync byte with a broken stride must NOT pass.
	fakeTS := pad([]byte{0x47})

	cases := []struct {
		name string
		head []byte
		want bool
	}{
		{"mkv EBML", pad([]byte{0x1A, 0x45, 0xDF, 0xA3, 0xA3, 0x42, 0x86, 0x81}), true},
		{"avi RIFF", pad([]byte("RIFF\x24\x00\x00\x00AVI ")), true},
		{"ogg", pad([]byte("OggS\x00\x02")), true},
		{"flv", pad([]byte("FLV\x01\x05")), true},
		{"wmv ASF", pad([]byte{0x30, 0x26, 0xB2, 0x75, 0x8E, 0x66, 0xCF, 0x11}), true},
		{"mpeg PS", pad([]byte{0x00, 0x00, 0x01, 0xBA, 0x44}), true},
		{"mp4 ftyp", mp4, true},
		{"mpeg ts", ts, true},
		{"m2ts", m2ts, true},
		{"mp3 id3", pad([]byte("ID3\x04\x00")), true},
		{"mp3 frame sync", pad([]byte{0xFF, 0xFB, 0x90, 0x00}), true},
		{"flac", pad([]byte("fLaC\x00\x00\x00\x22")), true},
		{"aiff", pad([]byte("FORM\x00\x00\x00\x2EAIFF")), true},
		{"wavpack", pad([]byte("wvpk\x20\x00\x00\x00")), true},
		{"dvd ifo", pad([]byte("DVDVIDEO-VTS")), true},
		{"m3u playlist", pad([]byte("#EXTM3U\n#EXTINF")), true},
		{"lone ts sync byte", fakeTS, false},
		{"zero fill", make([]byte, verifyHeadBytes), false},
		{"mkv interior garbage", pad([]byte{0xA3, 0x42, 0x86, 0x81, 0x01, 0x42, 0xF7, 0x81}), false},
		{"rar4 volume header", pad([]byte("Rar!\x1a\x07\x00")), false},
		{"rar5 volume header", pad([]byte("Rar!\x1a\x07\x01\x00")), false},
		{"too short", []byte{0x1A, 0x45, 0xDF, 0xA3}, false},
		{"empty", nil, false},
	}
	for _, tc := range cases {
		if got := headSignatureOK(tc.head); got != tc.want {
			t.Errorf("%s: headSignatureOK = %v, want %v (head %s)", tc.name, got, tc.want, hexPrefix(tc.head))
		}
	}
}

func hexPrefix(b []byte) string {
	n := min(len(b), 8)
	var buf bytes.Buffer
	for _, c := range b[:n] {
		buf.WriteString(string("0123456789abcdef"[c>>4]) + string("0123456789abcdef"[c&0xF]) + " ")
	}
	return buf.String()
}

