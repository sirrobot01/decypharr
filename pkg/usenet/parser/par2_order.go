package parser

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sirrobot01/decypharr/pkg/usenet/manifest"
)

const (
	par2PacketMagic = "PAR2\x00PKT"
	par2PrefixSize  = 16 * 1024
	par2MaxProbe    = 64 * 1024
)

type par2FileDesc struct {
	hash16k  [16]byte
	fileSize int64
	filename string
}

type par2FileIdentity struct {
	hash16k  [16]byte
	fileSize int64
}

// orderObfuscatedRARGroups uses PAR2 FileDesc packets to identify the actual
// names and order of RAR volumes. This also reunites RAR posts that subject
// parsing incorrectly left in separate one-file groups.
func (p *NZBParser) orderObfuscatedRARGroups(ctx context.Context, groups map[string]*FileGroup, files []manifest.File) {
	descriptors := make(map[par2FileIdentity]par2FileDesc)
	par2Files := 0
	for _, file := range files {
		if len(file.Segments) == 0 {
			continue
		}
		filenameType := p.detectFileType(file.Filename)
		nameLooksPAR2 := strings.HasSuffix(strings.ToLower(file.Filename), ".par2")
		// Obfuscated NZB subjects can leave Filename empty. Probe those entries
		// (plus explicitly named PAR2 files) and identify PAR2 from its magic or
		// decoded yEnc name. The article broker reuses the prefix already fetched
		// during content-type detection for these obfuscated posts.
		if !nameLooksPAR2 && filenameType != storage.NZBFileTypeUnknown {
			continue
		}
		segments := append([]manifest.Segment(nil), file.Segments...)
		sort.SliceStable(segments, func(i, j int) bool { return segments[i].Number < segments[j].Number })
		metadata, err := p.source.Header(ctx, segments[0].MessageID, par2MaxProbe)
		if err != nil || metadata == nil {
			continue
		}
		isPAR2 := len(metadata.Snippet) >= 8 && string(metadata.Snippet[:8]) == par2PacketMagic
		isPAR2 = isPAR2 || strings.HasSuffix(strings.ToLower(metadata.Name), ".par2")
		if !isPAR2 {
			continue
		}
		par2Files++
		for _, desc := range parsePAR2FileDescs(metadata.Snippet) {
			if desc.filename != "" {
				identity := par2FileIdentity{hash16k: desc.hash16k, fileSize: desc.fileSize}
				if previous, ok := descriptors[identity]; !ok || previous.filename == desc.filename {
					descriptors[identity] = desc
				} else {
					// A hash/length collision with two names is ambiguous; don't use it.
					delete(descriptors, identity)
				}
			}
		}
	}
	p.logger.Debug().Int("par2_files_detected", par2Files).Int("file_descriptions", len(descriptors)).Msg("Inspected PAR2 metadata for obfuscated RAR ordering")
	if len(descriptors) == 0 {
		return
	}

	type volumeSet struct {
		files    []orderedRarFile
		expected int
		orders   map[int]struct{}
		inferred bool
	}
	sets := make(map[string]*volumeSet)
	for _, desc := range descriptors {
		name := desc.filename
		if p.detectFileType(name) != "rar" || getRARVolumeOrder(name) == 999999 {
			continue
		}
		base := strings.ToLower(p.getBaseFilename(filepath.Base(name)))
		if base == "" {
			continue
		}
		if sets[base] == nil {
			sets[base] = &volumeSet{orders: make(map[int]struct{})}
		}
		sets[base].expected++
		sets[base].orders[getRARVolumeOrder(name)] = struct{}{}
	}
	if len(sets) == 0 {
		return
	}
	var rarNames []string
	for _, desc := range descriptors {
		if p.detectFileType(desc.filename) == storage.NZBFileTypeRar && getRARVolumeOrder(desc.filename) != 999999 {
			rarNames = append(rarNames, fmt.Sprintf("%s (%d bytes)", desc.filename, desc.fileSize))
		}
	}
	sort.Strings(rarNames)
	p.logger.Debug().Strs("par2_rar_descriptions", rarNames).Msg("PAR2 RAR filenames and sizes")
	descriptorHashes := make(map[[16]byte]struct{}, len(descriptors))
	descriptorSizes := make(map[int64]struct{}, len(descriptors))
	for identity := range descriptors {
		descriptorHashes[identity.hash16k] = struct{}{}
		descriptorSizes[identity.fileSize] = struct{}{}
	}

	var unmatched []orderedRarFile
	rawRARFiles := 0
	sizeMatches, hashMatches, identityMatches := 0, 0, 0
	var candidateSamples []string
	for groupKey, group := range groups {
		if group == nil || group.Type != "rar" {
			continue
		}
		for _, file := range group.Files {
			rawRARFiles++
			candidate := orderedRarFile{file: file, sourceKey: groupKey, group: group}
			if len(file.Segments) == 0 {
				unmatched = append(unmatched, candidate)
				continue
			}
			segments := append([]manifest.Segment(nil), file.Segments...)
			sort.SliceStable(segments, func(i, j int) bool { return segments[i].Number < segments[j].Number })
			metadata, err := p.source.Header(ctx, segments[0].MessageID, par2PrefixSize)
			if err != nil || metadata == nil || metadata.Size <= 0 || len(metadata.Snippet) < par2PrefixSize {
				unmatched = append(unmatched, candidate)
				continue
			}
			hash16k := md5.Sum(metadata.Snippet[:par2PrefixSize])
			if _, ok := descriptorSizes[metadata.Size]; ok {
				sizeMatches++
			}
			if _, ok := descriptorHashes[hash16k]; ok {
				hashMatches++
			}
			identity := par2FileIdentity{hash16k: hash16k, fileSize: metadata.Size}
			if _, ok := descriptors[identity]; ok {
				identityMatches++
			}
			if len(candidateSamples) < 3 {
				candidateSamples = append(candidateSamples, fmt.Sprintf("%s size=%d hash16k=%x", groupKey, metadata.Size, hash16k[:6]))
			}
			desc, ok := descriptors[identity]
			if !ok || p.detectFileType(desc.filename) != "rar" {
				unmatched = append(unmatched, candidate)
				continue
			}
			name := desc.filename
			order := getRARVolumeOrder(name)
			base := strings.ToLower(p.getBaseFilename(filepath.Base(name)))
			set := sets[base]
			if order == 999999 || set == nil {
				unmatched = append(unmatched, candidate)
				continue
			}
			candidate.order = order
			candidate.descriptor = name
			set.files = append(set.files, candidate)
		}
	}
	p.logger.Debug().Int("rar_candidates", rawRARFiles).Int("size_matches", sizeMatches).Int("hash_matches", hashMatches).Int("identity_matches", identityMatches).Strs("candidate_samples", candidateSamples).Msg("PAR2 RAR article identity diagnostics")

	// Some PAR2 sets omit one RAR volume from their FileDesc table. For a
	// single archive only, we can safely place exactly one unlisted RAR post
	// into exactly one missing part number when every other volume is matched
	// by its PAR2 hash and size and the part sequence has a single gap.
	if len(sets) == 1 {
		for base, set := range sets {
			if rawRARFiles != set.expected+1 || len(unmatched) != 1 || len(set.files) != set.expected {
				continue
			}
			minOrder, maxOrder := int(^uint(0)>>1), -1
			for order := range set.orders {
				if order < minOrder {
					minOrder = order
				}
				if order > maxOrder {
					maxOrder = order
				}
			}
			if minOrder != 1 || maxOrder-minOrder+1 != set.expected+1 || maxOrder > 99 {
				continue
			}
			missing := 0
			for order := minOrder; order <= maxOrder; order++ {
				if _, exists := set.orders[order]; !exists {
					if missing != 0 {
						missing = -1
						break
					}
					missing = order
				}
			}
			if missing < 0 || missing == 0 {
				continue
			}
			volume := unmatched[0]
			volume.order = missing
			volume.descriptor = fmt.Sprintf("%s.part%02d.rar", base, missing)
			set.files = append(set.files, volume)
			set.inferred = true
			p.logger.Info().Int("volume", missing).Str("base", base).Msg("Inferred single unlisted RAR volume from PAR2 sequence gap")
		}
	}

	for base, set := range sets {
		p.logger.Debug().Str("base", base).Int("expected_volumes", set.expected).Int("matched_volumes", len(set.files)).Msg("PAR2 RAR volume match status")
		expectedMatched := set.expected
		if set.inferred {
			expectedMatched++
		}
		if set.expected < 2 || len(set.files) != expectedMatched {
			continue
		}
		seenOrders := make(map[int]struct{}, len(set.files))
		groupsToMerge := make(map[string]*FileGroup)
		filesPerGroup := make(map[string]int)
		valid := true
		for _, volume := range set.files {
			if _, duplicate := seenOrders[volume.order]; duplicate {
				valid = false
				break
			}
			seenOrders[volume.order] = struct{}{}
			groupsToMerge[volume.sourceKey] = volume.group
			filesPerGroup[volume.sourceKey]++
		}
		for key, group := range groupsToMerge {
			if len(group.Files) != filesPerGroup[key] {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		sort.SliceStable(set.files, func(i, j int) bool { return set.files[i].order < set.files[j].order })
		merged := &FileGroup{
			BaseName:       p.getBaseFilename(filepath.Base(set.files[0].descriptor)),
			ActualFilename: filepath.Base(set.files[0].descriptor),
			Type:           "rar",
			Files:          make([]manifest.File, 0, len(set.files)),
			fileMeta:       make(map[string]filePartMeta),
			Groups:         make(map[string]struct{}),
		}
		for i, volume := range set.files {
			file := volume.file
			file.Number = i + 1
			merged.Files = append(merged.Files, file)
			merged.articleObserved = merged.articleObserved || volume.group.articleObserved
			for key, meta := range volume.group.fileMeta {
				merged.fileMeta[key] = meta
			}
			for groupName := range volume.group.Groups {
				merged.Groups[groupName] = struct{}{}
			}
		}
		for key := range groupsToMerge {
			delete(groups, key)
		}
		groupKey := merged.BaseName
		for suffix := 2; groups[groupKey] != nil; suffix++ {
			groupKey = fmt.Sprintf("%s (%d)", merged.BaseName, suffix)
		}
		groups[groupKey] = merged
		p.logger.Info().Int("volumes", len(set.files)).Str("group", merged.BaseName).Msg("Matched and ordered obfuscated RAR volumes from PAR2 metadata")
	}
}

type orderedRarFile struct {
	file       manifest.File
	sourceKey  string
	group      *FileGroup
	order      int
	descriptor string
}

// parsePAR2FileDescs extracts FileDesc packets from the start of a PAR2 file.
// Packet hashes are checked so random data or truncated packets cannot change
// the order of a stream.
func parsePAR2FileDescs(data []byte) []par2FileDesc {
	var result []par2FileDesc
	for offset := 0; offset+64 <= len(data); {
		start := bytes.Index(data[offset:], []byte(par2PacketMagic))
		if start < 0 {
			break
		}
		offset += start
		if offset+64 > len(data) {
			break
		}
		packetLength := binary.LittleEndian.Uint64(data[offset+8 : offset+16])
		if packetLength < 64 || packetLength > uint64(len(data)-offset) || packetLength%4 != 0 {
			break
		}
		packet := data[offset : offset+int(packetLength)]
		hash := md5.Sum(packet[32:])
		if bytes.Equal(hash[:], packet[16:32]) && bytes.Equal(packet[48:64], []byte("PAR 2.0\x00FileDesc")) && len(packet) >= 120 {
			// The filename field is padded to a 4-byte boundary, but PAR2 does
			// not require a terminating NUL when the name already fills it.
			name := bytes.TrimRight(packet[120:], "\x00")
			if len(name) > 0 {
				var hash16k [16]byte
				// FileDesc stores the full-file MD5 first, then MD5-16k.
				copy(hash16k[:], packet[96:112])
				fileSize := int64(binary.LittleEndian.Uint64(packet[112:120]))
				result = append(result, par2FileDesc{hash16k: hash16k, fileSize: fileSize, filename: string(name)})
			}
		}
		offset += int(packetLength)
	}
	return result
}
