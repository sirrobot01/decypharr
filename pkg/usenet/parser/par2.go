package parser

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/pkg/storage"
)

const (
	par2Magic        = "PAR2\000PKT"
	par2FileDescType = "PAR 2.0\000FileDesc"
	par2BlockSize    = 16384
	par2FetchSize    = 256 * 1024
)

type Par2FileDesc struct {
	FileID      [16]byte
	FileHash    [16]byte
	File16kHash [16]byte
	FileLength  uint64
	FileName    string
}

var (
	magic7z   = []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}
	magicRar4 = []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x00}
	magicRar5 = []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07, 0x01, 0x00}
	magicZip  = []byte{0x50, 0x4B, 0x03, 0x04}
	magicGzip = []byte{0x1F, 0x8B}
)

func parsePar2FileDesc(data []byte) []Par2FileDesc {
	var descs []Par2FileDesc
	offset := 0

	for offset < len(data) {
		if offset+8 > len(data) {
			break
		}
		if string(data[offset:offset+8]) != par2Magic {
			offset++
			continue
		}
		if offset+16 > len(data) {
			break
		}
		packetLen := int(binary.LittleEndian.Uint64(data[offset+8 : offset+16]))
		if packetLen < 64 {
			packetLen = len(data) - offset
		}
		if offset+packetLen > len(data) {
			packetLen = len(data) - offset
		}
		if offset+64 > len(data) {
			break
		}
		packetType := string(data[offset+48 : offset+64])
		if packetType == par2FileDescType {
			body := data[offset+64 : offset+packetLen]
			if len(body) < 56 {
				offset += packetLen
				continue
			}
			var fd Par2FileDesc
			copy(fd.FileID[:], body[0:16])
			copy(fd.FileHash[:], body[16:32])
			copy(fd.File16kHash[:], body[32:48])
			fd.FileLength = binary.LittleEndian.Uint64(body[48:56])
			nameBytes := body[56:]
			if idx := bytes.IndexByte(nameBytes, 0); idx >= 0 {
				nameBytes = nameBytes[:idx]
			}
			if len(nameBytes) > 0 {
				fd.FileName = string(nameBytes)
				descs = append(descs, fd)
			}
		}
		offset += packetLen
		if packetLen == 0 {
			break
		}
	}
	return descs
}

func (p *NZBParser) processPar2Group(ctx context.Context, group *FileGroup) ([]*storage.NZBFile, error) {
	if len(group.Files) == 0 || len(group.Files[0].Segments) == 0 {
		return nil, fmt.Errorf("PAR2 group has no files or segments")
	}

	p.logger.Debug().Str("group", group.BaseName).Msg("Downloading PAR2 file for deobfuscation")

	firstSegment := group.Files[0].Segments[0]
	var data *nntp.YencMetadata
	err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
		var e error
		data, e = conn.GetHeaderPrefix(firstSegment.Id, par2FetchSize)
		return e
	})
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to fetch PAR2 segment, deobfuscation not available")
		return nil, nil
	}
	if data == nil || len(data.Snippet) == 0 {
		p.logger.Warn().Msg("PAR2 data is empty, deobfuscation not available")
		return nil, nil
	}

	descs := parsePar2FileDesc(data.Snippet)
	if len(descs) == 0 {
		p.logger.Warn().Msg("No FileDesc entries found in PAR2, deobfuscation not available")
		return nil, nil
	}

	p.par2Descs = descs
	p.logger.Info().Int("entries", len(descs)).Msg("PAR2 FileDesc entries loaded for deobfuscation")
	return nil, nil
}

func (p *NZBParser) deobfuscateGroupWithPar2(ctx context.Context, group *FileGroup, descs []Par2FileDesc) (bool, error) {
	if len(descs) == 0 {
		return false, nil
	}

	matched := 0
	renamed := 0
	originalType := group.Type

	for i := range group.Files {
		if len(group.Files[i].Segments) == 0 {
			continue
		}

		var data *nntp.YencMetadata
		err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
			var e error
			data, e = conn.GetHeaderPrefix(group.Files[i].Segments[0].Id, par2BlockSize)
			return e
		})
		if err != nil || data == nil || len(data.Snippet) == 0 {
			continue
		}

		snippet := data.Snippet
		if len(snippet) > par2BlockSize {
			snippet = snippet[:par2BlockSize]
		}

		hash := md5.Sum(snippet)

		for _, fd := range descs {
			if bytes.Equal(hash[:], fd.File16kHash[:]) {
				matched++
				if group.Files[i].Filename != fd.FileName {
					p.logger.Debug().
						Str("old_name", group.Files[i].Filename).
						Str("new_name", fd.FileName).
						Msg("PAR2 deobfuscation: renamed file")
					group.Files[i].Filename = fd.FileName
					renamed++
				}
				break
			}
		}
	}

	firstOrig := ""
	if renamed > 0 {
		p.logger.Info().Int("renamed", renamed).Msg("PAR2 deobfuscation renamed files")
		for _, f := range group.Files {
			if f.Filename != "" {
				firstOrig = f.Filename
				break
			}
		}
		if firstOrig != "" {
			group.ActualFilename = firstOrig
			name := strings.TrimSuffix(firstOrig, filepath.Ext(firstOrig))
			if name != "" {
				group.BaseName = name
			}
		}
	}

	detected := p.detectArchiveTypeFromContent(ctx, group)
	if detected != storage.NZBFileTypeUnknown {
		group.Type = detected
	} else {
		for _, f := range group.Files {
			d := p.detectFileType(f.Filename)
			if d == storage.NZBFileTypeRar || d == storage.NZBFileTypeZip || d == storage.NZBFileTypeSevenZip || d == storage.NZBFileTypeMedia {
				group.Type = d
				break
			}
		}
	}

	if renamed == 0 && group.Type == originalType {
		if matched > 0 {
			p.logger.Debug().Int("matched", matched).Msg("PAR2 deobfuscation matched files but filenames were already unchanged")
		}
		return false, nil
	}

	return true, nil
}

func (p *NZBParser) detectArchiveTypeFromContent(ctx context.Context, group *FileGroup) storage.NZBFileType {
	if len(group.Files) == 0 || len(group.Files[0].Segments) == 0 {
		return storage.NZBFileTypeUnknown
	}

	var data *nntp.YencMetadata
	err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
		var e error
		data, e = conn.GetHeaderPrefix(group.Files[0].Segments[0].Id, 64)
		return e
	})
	if err != nil || data == nil || len(data.Snippet) < 4 {
		return storage.NZBFileTypeUnknown
	}

	snip := data.Snippet

	switch {
	case len(snip) >= 8 && bytes.Equal(snip[:8], magicRar5):
		return storage.NZBFileTypeRar
	case len(snip) >= 7 && bytes.Equal(snip[:7], magicRar4):
		return storage.NZBFileTypeRar
	case len(snip) >= 6 && bytes.Equal(snip[:6], magic7z):
		return storage.NZBFileTypeSevenZip
	case len(snip) >= 4 && bytes.Equal(snip[:4], magicZip):
		return storage.NZBFileTypeZip
	case len(snip) >= 2 && bytes.Equal(snip[:2], magicGzip):
		return storage.NZBFileTypeZip
	default:
		return storage.NZBFileTypeUnknown
	}
}

func (p *NZBParser) par2DeobfuscationAttempt(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	if group.par2Attempted {
		p.logger.Warn().Str("group", group.BaseName).Msg("PAR2 deobfuscation already attempted for this group, skipping")
		return nil, fmt.Errorf("archive parsers failed after PAR2 deobfuscation for group %s", group.BaseName)
	}

	group.par2Attempted = true

	if len(p.par2Descs) == 0 {
		p.logger.Warn().Str("group", group.BaseName).Msg("PAR2 deobfuscation unavailable: no FileDesc entries loaded")
		return nil, fmt.Errorf("archive parsers failed and no PAR2 data available (possibly requires PAR2 repair or unsupported obfuscation)")
	}

	p.logger.Warn().Str("group", group.BaseName).Msg("All archive parsers failed, attempting PAR2 deobfuscation")

	renamed, err := p.deobfuscateGroupWithPar2(ctx, group, p.par2Descs)
	if err != nil || !renamed {
		if err != nil {
			return nil, fmt.Errorf("PAR2 deobfuscation error: %w", err)
		}
		return nil, fmt.Errorf("PAR2 deobfuscation could not match any files in group %s", group.BaseName)
	}

	p.logger.Warn().Str("group", group.BaseName).Str("type", string(group.Type)).Msg("PAR2 deobfuscation successful, retrying archive parsing")
	return p.processFileGroup(ctx, group, password)
}
