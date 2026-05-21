package parser

import (
	"testing"

	"github.com/Tensai75/nzbparser"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

func TestGetNZBSegmentsUsesPerFileMetadata(t *testing.T) {
	first := nzbparser.NzbFile{
		Number: 1,
		Segments: nzbparser.NzbSegments{
			{Number: 1, Bytes: 100, Id: "first-1"},
			{Number: 2, Bytes: 60, Id: "first-2"},
		},
	}
	second := nzbparser.NzbFile{
		Number: 2,
		Segments: nzbparser.NzbSegments{
			{Number: 1, Bytes: 90, Id: "second-1"},
			{Number: 2, Bytes: 90, Id: "second-2"},
		},
	}

	group := &FileGroup{
		BaseName: "sample",
		Files:    []nzbparser.NzbFile{first, second},
		metadata: &fileAnalysisResult{
			fileSize:     160,
			lastFileSize: 160,
			segmentSize:  100,
		},
		fileMeta: map[string]filePartMeta{
			fileMetaKey(first):  {fileSize: 160, segmentSize: 100},
			fileMetaKey(second): {fileSize: 150, segmentSize: 80},
		},
	}

	total, segments := getNZBSegments(1, second, group)
	if total != 150 {
		t.Fatalf("expected total size 150, got %d", total)
	}
	if len(segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(segments))
	}
	if segments[0].Bytes != 80 {
		t.Fatalf("expected first segment size 80, got %d", segments[0].Bytes)
	}
	if segments[1].Bytes != 70 {
		t.Fatalf("expected second segment size 70, got %d", segments[1].Bytes)
	}
	if segments[1].StartOffset != 80 || segments[1].EndOffset != 149 {
		t.Fatalf("expected second segment offsets 80-149, got %d-%d", segments[1].StartOffset, segments[1].EndOffset)
	}
}

func TestGroupProcessedFilesSeparatesPar2FromPayload(t *testing.T) {
	p := &NZBParser{}
	allFiles := []contentResult{
		{
			file:           nzbparser.NzbFile{Number: 1, Filename: "TIesaj2er6vz6c3xW.part01.rar", Basefilename: "TIesaj2er6vz6c3xW"},
			fileType:       storage.NZBFileTypeRar,
			actualFilename: "TIesaj2er6vz6c3xW.part01.rar",
		},
		{
			file:           nzbparser.NzbFile{Number: 2, Filename: "TIesaj2er6vz6c3xW.part02.rar", Basefilename: "TIesaj2er6vz6c3xW"},
			fileType:       storage.NZBFileTypeRar,
			actualFilename: "TIesaj2er6vz6c3xW.part02.rar",
		},
		{
			file:           nzbparser.NzbFile{Number: 3, Filename: "TIesaj2er6vz6c3xW.vol001+01.par2", Basefilename: "TIesaj2er6vz6c3xW"},
			fileType:       storage.NZBFileTypePar2,
			actualFilename: "TIesaj2er6vz6c3xW.vol001+01.par2",
		},
		{
			file:           nzbparser.NzbFile{Number: 4, Filename: "TIesaj2er6vz6c3xW.vol001+02.par2", Basefilename: "TIesaj2er6vz6c3xW"},
			fileType:       storage.NZBFileTypePar2,
			actualFilename: "TIesaj2er6vz6c3xW.vol001+02.par2",
		},
	}

	groups := p.groupProcessedFiles(allFiles)
	if len(groups) != 3 {
		t.Fatalf("expected 3 groups (1 rar + 2 par2), got %d", len(groups))
	}

	rarGroup, ok := groups["TIesaj2er6vz6c3xW"]
	if !ok {
		t.Fatalf("expected merged payload group keyed by base name")
	}
	if rarGroup.Type != storage.NZBFileTypeRar {
		t.Fatalf("expected payload group type rar, got %s", rarGroup.Type)
	}
	if len(rarGroup.Files) != 2 {
		t.Fatalf("expected 2 files in payload group, got %d", len(rarGroup.Files))
	}

	if _, ok := groups["par2::TIesaj2er6vz6c3xW.vol001+01.par2"]; !ok {
		t.Fatalf("expected first par2 group to stay separate")
	}
	if _, ok := groups["par2::TIesaj2er6vz6c3xW.vol001+02.par2"]; !ok {
		t.Fatalf("expected second par2 group to stay separate")
	}
}

func TestRenameMediaFilesKeepsDiscoveredSingleFilename(t *testing.T) {
	files := []storage.NZBFile{{
		Name:     "From.S04E01.The.Arrival.2160p.AMZN.WEB-DL.DDP5.1.H.265-playWEB.mkv",
		FileType: storage.NZBFileTypeMedia,
		Number:   1,
	}}

	renameMediaFiles(files, config.DeobfuscateModeIndex, "From.S04E01.The.Arrival.2160p.AMZN.WEB-DL.DDP5.1.H.265-playWEB", zerolog.Nop())

	if files[0].Name != "From.S04E01.The.Arrival.2160p.AMZN.WEB-DL.DDP5.1.H.265-playWEB.mkv" {
		t.Fatalf("expected discovered filename to be preserved, got %q", files[0].Name)
	}
}

func TestRenameMediaFilesFallbackForObfuscatedSingleFilename(t *testing.T) {
	files := []storage.NZBFile{{
		Name:     "206693r5v9t78w3Y94Y9O3G2G4533713.mkv",
		FileType: storage.NZBFileTypeMedia,
		Number:   1,
	}}

	renameMediaFiles(files, config.DeobfuscateModeIndex, "The.Matrix.Revolutions.2003.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR-AsRequested", zerolog.Nop())

	want := "The.Matrix.Revolutions.2003.UHD.BluRay.2160p.TrueHD.Atmos.7.1.DV.HEVC.REMUX-FraMeSToR-AsRequested.mkv"
	if files[0].Name != want {
		t.Fatalf("expected fallback filename %q, got %q", want, files[0].Name)
	}
}

func TestRenameMediaFilesKeepsUniqueDetectedEpisodeNames(t *testing.T) {
	files := []storage.NZBFile{
		{Name: "Silicon.Valley.S06E01.1080p.WEBRip.x265-NOGRP.mkv", FileType: storage.NZBFileTypeMedia, Number: 1},
		{Name: "Silicon.Valley.S06E02.1080p.WEBRip.x265-NOGRP.mkv", FileType: storage.NZBFileTypeMedia, Number: 2},
	}

	renameMediaFiles(files, config.DeobfuscateModeSeasonEp, "Silicon.Valley.S06.1080p.WEBRip.x265-NOGRP", zerolog.Nop())

	if files[0].Name != "Silicon.Valley.S06E01.1080p.WEBRip.x265-NOGRP.mkv" || files[1].Name != "Silicon.Valley.S06E02.1080p.WEBRip.x265-NOGRP.mkv" {
		t.Fatalf("expected discovered episode filenames to be preserved, got %q and %q", files[0].Name, files[1].Name)
	}
}

func TestRenameMediaFilesFallbackForDuplicateEpisodeNames(t *testing.T) {
	files := []storage.NZBFile{
		{Name: "sample.mkv", FileType: storage.NZBFileTypeMedia, Number: 1},
		{Name: "sample.mkv", FileType: storage.NZBFileTypeMedia, Number: 2},
	}

	renameMediaFiles(files, config.DeobfuscateModeSeasonEp, "Show.Name.S04.1080p.WEBRip", zerolog.Nop())

	if files[0].Name != "S04E01.mkv" || files[1].Name != "S04E02.mkv" {
		t.Fatalf("expected season/episode fallback names, got %q and %q", files[0].Name, files[1].Name)
	}
}

func TestBuildBaseSegmentsUsesPerFileMetadata(t *testing.T) {
	first := nzbparser.NzbFile{
		Number:   1,
		Filename: "sample.7z.001",
		Segments: nzbparser.NzbSegments{
			{Number: 1, Bytes: 100, Id: "first-1"},
			{Number: 2, Bytes: 60, Id: "first-2"},
		},
	}
	second := nzbparser.NzbFile{
		Number:   2,
		Filename: "sample.7z.002",
		Segments: nzbparser.NzbSegments{
			{Number: 1, Bytes: 90, Id: "second-1"},
			{Number: 2, Bytes: 90, Id: "second-2"},
		},
	}

	group := &FileGroup{
		BaseName: "sample",
		Files:    []nzbparser.NzbFile{first, second},
		metadata: &fileAnalysisResult{
			fileSize:     160,
			lastFileSize: 160,
			segmentSize:  100,
		},
		fileMeta: map[string]filePartMeta{
			fileMetaKey(first):  {fileSize: 160, segmentSize: 100},
			fileMetaKey(second): {fileSize: 150, segmentSize: 80},
		},
	}

	baseSegments, volumeInfos, total := buildBaseSegments(group)
	if total != 310 {
		t.Fatalf("expected total concatenated size 310, got %d", total)
	}
	if len(baseSegments) != 4 {
		t.Fatalf("expected 4 base segments, got %d", len(baseSegments))
	}
	if len(volumeInfos) != 2 {
		t.Fatalf("expected 2 volume infos, got %d", len(volumeInfos))
	}
	if volumeInfos[0].Size != 160 {
		t.Fatalf("expected first volume size 160, got %d", volumeInfos[0].Size)
	}
	if volumeInfos[1].Size != 150 {
		t.Fatalf("expected second volume size 150, got %d", volumeInfos[1].Size)
	}
}
