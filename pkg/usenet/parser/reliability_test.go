package parser

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/customerror"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/testutil/nntpd"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
	"github.com/sirrobot01/decypharr/pkg/usenet/types"
)

func TestProbeContentAvailabilityReportsAllMissingContent(t *testing.T) {
	p := &NZBParser{logger: zerolog.Nop()}
	groups := map[string]*FileGroup{
		"b": {BaseName: "b", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "b@example"}}}}},
		"a": {BaseName: "a", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "a@example"}}}}},
	}
	missing := &nntp.Error{Type: nntp.ErrorTypeArticleNotFound, Code: 430, Message: "missing"}

	var calls []string
	err := p.probeContentAvailability(context.Background(), groups, func(_ context.Context, messageID string) error {
		calls = append(calls, messageID)
		return missing
	})
	if !errors.Is(err, customerror.UsenetSegmentMissingError) {
		t.Fatalf("all-missing availability error = %v", err)
	}
	if want := []string{"a@example", "b@example"}; !slices.Equal(calls, want) {
		t.Fatalf("STAT calls = %v, want %v", calls, want)
	}
}

func TestProbeContentAvailabilityAcceptsAnotherAvailableGroup(t *testing.T) {
	p := &NZBParser{logger: zerolog.Nop()}
	groups := map[string]*FileGroup{
		"a": {BaseName: "a", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "missing@example"}}}}},
		"b": {BaseName: "b", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "available@example"}}}}},
	}
	missing := &nntp.Error{Type: nntp.ErrorTypeArticleNotFound, Code: 430, Message: "missing"}
	err := p.probeContentAvailability(context.Background(), groups, func(_ context.Context, messageID string) error {
		if messageID == "missing@example" {
			return missing
		}
		return nil
	})
	if err != nil {
		t.Fatalf("availability probe = %v", err)
	}
}

func TestProbeContentAvailabilityStopsOnOperationalError(t *testing.T) {
	p := &NZBParser{logger: zerolog.Nop()}
	groups := map[string]*FileGroup{
		"a": {BaseName: "a", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "a@example"}}}}},
		"b": {BaseName: "b", Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "b@example"}}}}},
	}
	wantErr := errors.New("authentication failed")
	calls := 0
	err := p.probeContentAvailability(context.Background(), groups, func(context.Context, string) error {
		calls++
		return wantErr
	})
	if !errors.Is(err, wantErr) || calls != 1 {
		t.Fatalf("operational error probe = calls %d, err %v", calls, err)
	}
}

func TestProbeContentAvailabilityReusesBodyObservation(t *testing.T) {
	backend := &fakeArticleBackend{}
	broker := newArticleBroker(backend, 1, 1<<20)
	if _, err := broker.Body(t.Context(), "observed@example"); err != nil {
		t.Fatal(err)
	}
	p := NewParserWithSource(broker, 1, zerolog.Nop())
	groups := map[string]*FileGroup{
		"content": {BaseName: "content", articleObserved: true, Files: []manifest.File{{Segments: []manifest.Segment{{Number: 1, MessageID: "observed@example"}}}}},
	}
	calls := 0
	err := p.probeContentAvailability(t.Context(), groups, func(context.Context, string) error {
		calls++
		return errors.New("STAT should not run")
	})
	if err != nil || calls != 0 {
		t.Fatalf("observed availability = calls %d, err %v", calls, err)
	}
}

func TestHeaderProbeOrderCoversEveryPartOnce(t *testing.T) {
	if got := headerProbeOrder(0); len(got) != 0 {
		t.Fatalf("zero-part order = %v", got)
	}
	if got := headerProbeOrder(1); !slices.Equal(got, []int{0}) {
		t.Fatalf("one-part order = %v", got)
	}
	if got := headerProbeOrder(5); !slices.Equal(got, []int{0, 2, 4, 1, 3}) {
		t.Fatalf("five-part order = %v", got)
	}
}

func TestDecodedPartSizeDoesNotInventByteForMissingRange(t *testing.T) {
	if got := decodedPartSize(&nntp.YencMetadata{}); got != 0 {
		t.Fatalf("empty yEnc range size = %d, want 0", got)
	}
	if got := decodedPartSize(&nntp.YencMetadata{Begin: 101, End: 200}); got != 100 {
		t.Fatalf("inclusive yEnc range size = %d, want 100", got)
	}
	if got := decodedPartSize(&nntp.YencMetadata{PartSize: 75, Begin: 1, End: 100}); got != 75 {
		t.Fatalf("explicit yEnc part size = %d, want 75", got)
	}
}

func TestArchiveAndIgnoredFileTypeDetection(t *testing.T) {
	p := &NZBParser{}
	if got, _ := p.detectFileTypeAndExtensionFromContent([]byte{'P', 'A', 'R', '2', 0, 'P', 'K', 'T'}); got != storage.NZBFileTypeIgnore {
		t.Fatalf("parity data detected as %q, want ignored", got)
	}
	for _, name := range []string{"archive.r99", "archive.r123", "archive.s00", "archive.part100.rar"} {
		if got := p.detectFileType(name); got != storage.NZBFileTypeRar {
			t.Errorf("%q detected as %q, want rar", name, got)
		}
	}
	for _, name := range []string{"archive.z01", "archive.z001"} {
		if got := p.detectFileType(name); got != storage.NZBFileTypeZip {
			t.Errorf("%q detected as %q, want zip", name, got)
		}
	}
	if getZIPVolumeOrder("archive.z99") >= getZIPVolumeOrder("archive.z100") ||
		getZIPVolumeOrder("archive.z100") >= getZIPVolumeOrder("archive.zip") {
		t.Fatal("split ZIP volume ordering is not numeric with .zip last")
	}
}

func TestContentDetectionInfersExtensionForObfuscatedMedia(t *testing.T) {
	p := &NZBParser{}
	tests := []struct {
		name      string
		data      []byte
		extension string
	}{
		{name: "matroska", data: []byte{0x1A, 0x45, 0xDF, 0xA3}, extension: ".mkv"},
		{name: "mp4", data: []byte{0, 0, 0, 0, 'f', 't', 'y', 'p'}, extension: ".mp4"},
		{name: "avi", data: []byte{'R', 'I', 'F', 'F', 0, 0, 0, 0, 'A', 'V', 'I', ' '}, extension: ".avi"},
		{name: "mpeg", data: []byte{0, 0, 1, 0xBA}, extension: ".mpg"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fileType, extension := p.detectFileTypeAndExtensionFromContent(tt.data)
			if fileType != storage.NZBFileTypeMedia || extension != tt.extension {
				t.Fatalf("content classification = (%q, %q), want (%q, %q)", fileType, extension, storage.NZBFileTypeMedia, tt.extension)
			}
		})
	}
}

func TestExtensionlessObfuscatedMediaProducesLogicalFile(t *testing.T) {
	config.Reset()
	config.SetConfigPath(t.TempDir())
	_ = config.Get()

	server, err := nntpd.New(nntpd.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)

	payload := make([]byte, 1024)
	copy(payload, []byte{0x1A, 0x45, 0xDF, 0xA3})
	server.AddArticle("<media@nntpd>", nntpd.Encode(payload, "hBnewXHwWBUEYm24QydJAcpQ4nNpC", 1, int64(len(payload)), 0))
	host, port := server.Addr()
	client, err := nntp.NewClient(&config.Config{Usenet: config.Usenet{
		Providers: []config.UsenetProvider{{Host: host, Port: port, MaxConnections: 2}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })

	const source = `<?xml version="1.0" encoding="UTF-8"?>
<nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">
  <file poster="poster" date="1" subject="opaque [1/1] yEnc (1/1)">
    <groups><group>alt.test</group></groups>
    <segments><segment bytes="1100" number="1">media@nntpd</segment></segments>
  </file>
</nzb>`
	p := NewParser(client, 2, zerolog.Nop())
	_, groups, err := p.Parse(t.Context(), "Obfuscated.nzb", []byte(source))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Metrics().NetworkStats; got != 0 {
		t.Fatalf("content-classified post made %d redundant STAT requests", got)
	}
	files, err := p.processFileGroups(t.Context(), groups, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("logical files = %d, want 1", len(files))
	}
	if files[0].Name != "hBnewXHwWBUEYm24QydJAcpQ4nNpC.mkv" {
		t.Fatalf("logical filename = %q, want inferred .mkv extension", files[0].Name)
	}
}

func TestProcessMediaRebasesLogicalOffsets(t *testing.T) {
	first := manifest.File{Filename: "movie.mkv", Number: 1, Groups: []string{"alt.test"}, Segments: []manifest.Segment{{Number: 1, MessageID: "one@example", Bytes: 5}}}
	second := manifest.File{Filename: "movie.mkv", Number: 2, Groups: []string{"alt.test"}, Segments: []manifest.Segment{{Number: 1, MessageID: "two@example", Bytes: 4}}}
	group := &FileGroup{
		BaseName: "movie", ActualFilename: "movie.mkv", Type: storage.NZBFileTypeMedia,
		Files: []manifest.File{first, second}, metadata: &fileAnalysisResult{fileSize: 4, lastFileSize: 3, segmentSize: 4},
		Groups: map[string]struct{}{"alt.test": {}},
	}
	file := (&NZBParser{logger: zerolog.Nop()}).processMediaFile(group, "")
	if file == nil || len(file.Segments) != 2 {
		t.Fatalf("processed file = %#v", file)
	}
	if got := [2]int64{file.Segments[0].StartOffset, file.Segments[0].EndOffset}; got != [2]int64{0, 3} {
		t.Fatalf("first logical range = %v", got)
	}
	if got := [2]int64{file.Segments[1].StartOffset, file.Segments[1].EndOffset}; got != [2]int64{4, 6} {
		t.Fatalf("second logical range = %v", got)
	}
}

func TestArchiveSlicersRejectPartiallyCoveredRanges(t *testing.T) {
	base := []storage.NZBSegment{{Number: 1, MessageID: "article@example", Bytes: 10}}
	if _, err := sliceSegmentsForRangeSimple(base, 8, 4); err == nil {
		t.Fatal("simple archive slicer accepted a truncated range")
	}
	volumeInfos := []storage.ArchiveVolumeInfo{{Size: 10, SegmentStart: 0, SegmentEnd: 1}}
	if _, err := sliceSegmentsForRange(base, volumeInfos, 8, 4); err == nil {
		t.Fatal("7z archive slicer accepted a truncated range")
	}
	part := &types.RARVolumePart{PartNumber: 0, DataOffset: 8, UnpackedSize: 4}
	if _, err := (&RARParser{logger: zerolog.Nop()}).buildSegmentsForVolumePart(part, base, map[int]int64{0: 0}); err == nil {
		t.Fatal("RAR volume slicer accepted a truncated range")
	}
}

func TestArchiveBuildersRejectIncompleteVolume(t *testing.T) {
	group := &FileGroup{
		BaseName: "archive", metadata: &fileAnalysisResult{fileSize: 8, lastFileSize: 8, segmentSize: 4},
		Files: []manifest.File{
			{Filename: "archive.rar", Segments: []manifest.Segment{{Number: 1, MessageID: "a", Bytes: 5}, {Number: 2, MessageID: "b", Bytes: 5}}},
			{Filename: "archive.r00", Segments: []manifest.Segment{{Number: 1, MessageID: "c", Bytes: 5}, {Number: 3, MessageID: "d", Bytes: 5}}},
		},
	}
	if _, err := buildArchiveVolumeDescriptors(group); err == nil {
		t.Fatal("expected incomplete archive volume error")
	}
	if _, _, _, err := buildBaseSegments(group); err == nil {
		t.Fatal("expected incomplete base segment error")
	}
}

func TestObfuscatedRARMergeDoesNotCombineNamedStandaloneArchives(t *testing.T) {
	p := &NZBParser{logger: zerolog.Nop()}
	groups := map[string]*FileGroup{
		"movie":  {BaseName: "movie", ActualFilename: "Movie.rar", Type: storage.NZBFileTypeRar, Files: []manifest.File{{Filename: "Movie.rar"}}},
		"extras": {BaseName: "extras", ActualFilename: "Extras.rar", Type: storage.NZBFileTypeRar, Files: []manifest.File{{Filename: "Extras.rar"}}},
	}
	if got := p.mergeObfuscatedRarGroups(groups); len(got) != 2 {
		t.Fatalf("named standalone RAR group count = %d, want 2", len(got))
	}
}

func TestProcessFileGroupsUsesManifestOrderAndSortedGroups(t *testing.T) {
	backend := &fakeArticleBackend{bodySize: 16}
	p := NewParserWithSource(newArticleBroker(backend, 2, 1<<20), 2, zerolog.Nop())
	groups := map[string]*FileGroup{
		"a": {
			BaseName: "a", ActualFilename: "a.mkv", Type: storage.NZBFileTypeMedia,
			Files:  []manifest.File{{Order: 1, Number: 2, Filename: "a.mkv", Groups: []string{"z", "a"}, Segments: []manifest.Segment{{Number: 1, MessageID: "a.mkv", Bytes: 16}}}},
			Groups: map[string]struct{}{"z": {}, "a": {}},
		},
		"b": {
			BaseName: "b", ActualFilename: "b.mkv", Type: storage.NZBFileTypeMedia,
			Files:  []manifest.File{{Order: 0, Number: 1, Filename: "b.mkv", Groups: []string{"z", "a"}, Segments: []manifest.Segment{{Number: 1, MessageID: "b.mkv", Bytes: 16}}}},
			Groups: map[string]struct{}{"z": {}, "a": {}},
		},
	}
	files, err := p.processFileGroups(t.Context(), groups, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Name != "b.mkv" || files[1].Name != "a.mkv" {
		t.Fatalf("logical file order = %#v", files)
	}
	if want := []string{"a", "z"}; !slices.Equal(files[0].Groups, want) || !slices.Equal(files[1].Groups, want) {
		t.Fatalf("logical newsgroups = %v / %v, want %v", files[0].Groups, files[1].Groups, want)
	}
}
