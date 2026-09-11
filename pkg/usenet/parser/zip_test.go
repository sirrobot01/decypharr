package parser

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
)

func TestZIPParserReadsCentralDirectoryLargerThanTail(t *testing.T) {
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	const fileCount = 4_000
	for index := range fileCount {
		header := &zip.FileHeader{
			Name:   fmt.Sprintf("directory/%04d-%s.mkv", index, strings.Repeat("x", 64)),
			Method: zip.Store,
		}
		entry, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte{byte(index)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.SetComment("comment containing a false PK\x05\x06 signature"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	p := &ZIPParser{logger: zerolog.Nop()}
	info, err := p.parseArchiveReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()), false)
	if err != nil {
		t.Fatal(err)
	}
	if info.TotalFiles != fileCount || len(info.Files) != fileCount {
		t.Fatalf("ZIP files = %d/%d, want %d", info.TotalFiles, len(info.Files), fileCount)
	}
	if info.Files[0].Name != "directory/0000-"+strings.Repeat("x", 64)+".mkv" {
		t.Fatalf("first ZIP entry = %q", info.Files[0].Name)
	}
}

func TestZIPCentralDirectoryDoesNotSilentlyReturnPartialEntries(t *testing.T) {
	p := &ZIPParser{logger: zerolog.Nop()}
	if _, err := p.parseCentralDirectoryEntries(make([]byte, 46), 1); err == nil {
		t.Fatal("invalid central directory unexpectedly parsed")
	}
}

func TestAbsoluteZIPHeaderOffsetUsesStartDisk(t *testing.T) {
	entry := &ZIPFileEntry{DiskNumberStart: 2, LocalHeaderOffset: 75}
	offset, err := absoluteZIPHeaderOffset(entry, []int64{0, 1_000, 3_000})
	if err != nil {
		t.Fatal(err)
	}
	if offset != 3_075 {
		t.Fatalf("absolute local header offset = %d, want 3075", offset)
	}
	entry.DiskNumberStart = 3
	if _, err := absoluteZIPHeaderOffset(entry, []int64{0, 1_000, 3_000}); err == nil {
		t.Fatal("out-of-range start disk unexpectedly accepted")
	}
}
