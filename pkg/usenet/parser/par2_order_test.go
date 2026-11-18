package parser

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
)

func TestParsePAR2FileDescAndVerifyHash(t *testing.T) {
	wantHash := md5.Sum(bytesOf(0x31, par2PrefixSize))
	data := makePAR2FileDescPacket(wantHash, 12345, "movie.part01.rar", false)
	got := parsePAR2FileDescs(data)
	if len(got) != 1 {
		t.Fatalf("parsed %d PAR2 descriptions, want 1", len(got))
	}
	if got[0].hash16k != wantHash || got[0].fileSize != 12345 || got[0].filename != "movie.part01.rar" {
		t.Fatalf("parsed description = (%x, %d, %q)", got[0].hash16k, got[0].fileSize, got[0].filename)
	}

	terminated := parsePAR2FileDescs(makePAR2FileDescPacket(wantHash, 12345, "release.part03.rar", true))
	if len(terminated) != 1 || terminated[0].filename != "release.part03.rar" {
		t.Fatalf("NUL-terminated PAR2 filename was not parsed: %+v", terminated)
	}
	data[16] ^= 0xff
	if got := parsePAR2FileDescs(data); len(got) != 0 {
		t.Fatalf("accepted packet with invalid MD5, got %d descriptions", len(got))
	}
}

func TestOrderObfuscatedRARFilesFromPAR2(t *testing.T) {
	partOne := bytesOf(0x11, par2PrefixSize)
	partTwo := bytesOf(0x22, par2PrefixSize)
	partOneHash := md5.Sum(partOne)
	partTwoHash := md5.Sum(partTwo)
	par2 := append(makePAR2FileDescPacket(partOneHash, 20000, "release.part01.rar", false),
		makePAR2FileDescPacket(partTwoHash, 21000, "release.part02.rar", true)...)
	source := par2TestSource{headers: map[string]*nntp.YencMetadata{
		"par2":       {Snippet: par2},
		"opaque-two": {Snippet: partTwo, Size: 21000},
		"opaque-one": {Snippet: partOne, Size: 20000},
	}}
	parser := &NZBParser{source: source, logger: zerolog.Nop()}
	groupTwo := &FileGroup{BaseName: "opaque-two", Type: "rar", Files: []manifest.File{{
		Filename: "opaque-two", Number: 1, Segments: []manifest.Segment{{Number: 1, MessageID: "opaque-two"}},
	}}}
	groupOne := &FileGroup{BaseName: "opaque-one", Type: "rar", Files: []manifest.File{{
		Filename: "opaque-one", Number: 2, Segments: []manifest.Segment{{Number: 1, MessageID: "opaque-one"}},
	}}}
	groups := map[string]*FileGroup{"opaque-two": groupTwo, "opaque-one": groupOne}
	// The subject parser leaves this name empty, as it does for the supplied NZB.
	par2Input := []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "par2"}}}}

	parser.orderObfuscatedRARGroups(context.Background(), groups, par2Input)
	if len(groups) != 1 {
		t.Fatalf("groups after PAR2 match = %d, want 1", len(groups))
	}
	var group *FileGroup
	for _, group = range groups {
	}
	if got := group.Files[0].Filename; got != "opaque-one" {
		t.Fatalf("first volume = %q, want opaque-one", got)
	}
	if got := group.Files[1].Filename; got != "opaque-two" {
		t.Fatalf("second volume = %q, want opaque-two", got)
	}
}

func TestOrderInfersSingleRARVolumeMissingFromPAR2(t *testing.T) {
	partOne := bytesOf(0x11, par2PrefixSize)
	partTwo := bytesOf(0x22, par2PrefixSize)
	partThree := bytesOf(0x33, par2PrefixSize)
	par2 := append(makePAR2FileDescPacket(md5.Sum(partOne), 20000, "release.part01.rar", false),
		makePAR2FileDescPacket(md5.Sum(partThree), 22000, "release.part03.rar", false)...)
	source := par2TestSource{headers: map[string]*nntp.YencMetadata{
		"par2":         {Snippet: par2},
		"opaque-one":   {Snippet: partOne, Size: 20000},
		"opaque-two":   {Snippet: partTwo, Size: 21000},
		"opaque-three": {Snippet: partThree, Size: 22000},
	}}
	parser := &NZBParser{source: source, logger: zerolog.Nop()}
	groups := map[string]*FileGroup{}
	for i, name := range []string{"opaque-one", "opaque-two", "opaque-three"} {
		groups[name] = &FileGroup{BaseName: name, Type: "rar", Files: []manifest.File{{
			Filename: name, Number: i + 1, Segments: []manifest.Segment{{Number: 1, MessageID: name}},
		}}}
	}
	par2Input := []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "par2"}}}}

	parser.orderObfuscatedRARGroups(context.Background(), groups, par2Input)
	if len(groups) != 1 {
		t.Fatalf("groups after PAR2 match = %d, want 1", len(groups))
	}
	var group *FileGroup
	for _, group = range groups {
	}
	if len(group.Files) != 3 {
		t.Fatalf("merged volume count = %d, want 3", len(group.Files))
	}
	if got := group.Files[1].Filename; got != "opaque-two" {
		t.Fatalf("inferred second volume = %q, want opaque-two", got)
	}
}

type par2TestSource struct {
	headers map[string]*nntp.YencMetadata
}

func (s par2TestSource) Header(_ context.Context, messageID string, maxSnippet int) (*nntp.YencMetadata, error) {
	metadata := *s.headers[messageID]
	if maxSnippet >= 0 && len(metadata.Snippet) > maxSnippet {
		metadata.Snippet = metadata.Snippet[:maxSnippet]
	}
	return &metadata, nil
}

func (par2TestSource) Body(context.Context, string) ([]byte, error) { return nil, nil }
func (par2TestSource) Stat(context.Context, string) error           { return nil }
func (par2TestSource) IsAvailable(string) bool                      { return true }
func (par2TestSource) Metrics() ArticleMetrics                      { return ArticleMetrics{} }

func bytesOf(value byte, count int) []byte {
	data := make([]byte, count)
	for i := range data {
		data[i] = value
	}
	return data
}

func makePAR2FileDescPacket(hash16k [16]byte, fileSize int64, name string, terminated bool) []byte {
	nameFieldLength := len(name)
	if terminated {
		nameFieldLength++
	}
	packetLength := 64 + 56 + ((nameFieldLength + 3) &^ 3)
	packet := make([]byte, packetLength)
	copy(packet, par2PacketMagic)
	binary.LittleEndian.PutUint64(packet[8:16], uint64(packetLength))
	copy(packet[48:64], "PAR 2.0\x00FileDesc")
	copy(packet[96:112], hash16k[:])
	binary.LittleEndian.PutUint64(packet[112:120], uint64(fileSize))
	copy(packet[120:], name)
	hash := md5.Sum(packet[32:])
	copy(packet[16:32], hash[:])
	return packet
}
