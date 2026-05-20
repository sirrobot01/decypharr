package parser

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/Tensai75/nzbparser"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/config"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/storage"
	"github.com/sourcegraph/conc/iter"
)

var ErrMoreRarDataNeeded = fmt.Errorf("rar: need more data")

var (
	// defaultMaxSnippetSize is used for content-type detection via magic bytes.
	// TS sync-byte check at offset 188 is the deepest we go, so 512 bytes is ample.
	defaultMaxSnippetSize = 512
	// metadataOnly requests the yEnc header (name/size/offsets) without any
	// decoded payload — the connection is drained and returned to the pool.
	metadataOnly = 0
	// seasonRegex matches season indicators like S06, S01, or Season 6 in release names.
	seasonRegex = regexp.MustCompile(`(?i)[.\s]S(\d{1,2})[.\s]|Season\s(\d{1,2})`)
)

// NZBParser provides a simplified, robust NZB parser
type NZBParser struct {
	logger        zerolog.Logger
	manager       *nntp.Client // Connection manager for parsing operations
	maxConcurrent int          // Max concurrent connections
	par2Descs     []Par2FileDesc
}

type fileAnalysisResult struct {
	fileSize     int64 // Total decoded size of the NZB file entry.
	lastFileSize int64 // Total decoded size of the last NZB file entry in the group.
	segmentSize  int64 // Decoded size of a single yEnc part/segment.
}

type contentResult struct {
	file           nzbparser.NzbFile
	fileType       storage.NZBFileType
	actualFilename string
	fileSize       int64 // decoded size of the part (from yEnc), if available
	segmentSize    int64 // decoded size of a segment (from yEnc), if available
	partNumber     int64 // yEnc part number, if available
	partBegin      int64 // yEnc begin offset, if available
}

type filePartMeta struct {
	fileSize    int64
	segmentSize int64
	partNumber  int64
	partBegin   int64
}

type FileGroup struct {
	BaseName       string
	ActualFilename string
	Type           storage.NZBFileType
	Files          []nzbparser.NzbFile
	metadata       *fileAnalysisResult
	fileMeta       map[string]filePartMeta
	Groups         map[string]struct{}
	par2Attempted  bool
}

func (f *FileGroup) getMetadata() *fileAnalysisResult {
	if f.metadata != nil {
		return f.metadata
	}
	// Heuristic: assume segment is ~97% of reported bytes (yEnc overhead)
	if len(f.Files) == 0 || len(f.Files[0].Segments) == 0 {
		return &fileAnalysisResult{}
	}

	metadata := &fileAnalysisResult{}
	// Estimate actual segment size from reported bytes (account for ~3% yEnc overhead)
	reportedBytes := int64(f.Files[0].Segments[0].Bytes)
	if reportedBytes <= 0 {
		reportedBytes = 750000 // Default 750KB segment
	}
	metadata.segmentSize = int64(float64(reportedBytes) * 0.97)
	if metadata.segmentSize <= 0 {
		metadata.segmentSize = reportedBytes
	}
	metadata.fileSize = metadata.segmentSize * int64(len(f.Files[0].Segments))
	metadata.lastFileSize = metadata.segmentSize * int64(len(f.Files[len(f.Files)-1].Segments))
	f.metadata = metadata
	return f.metadata
}

// NewParser creates a new simplified NZB parser with a connection manager
func NewParser(manager *nntp.Client, maxConcurrent int, logger zerolog.Logger) *NZBParser {
	return &NZBParser{
		logger:        logger,
		manager:       manager,
		maxConcurrent: maxConcurrent,
	}
}

var (
	// RAR file patterns - simplified and more accurate
	rarMainPattern       = regexp.MustCompile(`\.rar$`)
	rarPartPattern       = regexp.MustCompile(`\.r\d{2}$`) // .r00, .r01, etc.
	rarVolumePattern     = regexp.MustCompile(`\.part\d+\.rar$`)
	ignoreExtensions     = []string{".sfv", ".nfo", ".jpg", ".png", ".txt", ".srt", ".idx", ".sub"}
	sevenZMainPattern    = regexp.MustCompile(`\.7z$`)
	sevenZPartPattern    = regexp.MustCompile(`\.7z\.\d{3}$`)
	extWithNumberPattern = regexp.MustCompile(`\.[^ "\.]*\.\d+$`)
	volPar2Pattern       = regexp.MustCompile(`(?i)\.vol\d+\+\d+\.par2?$`)
	partPattern          = regexp.MustCompile(`(?i)\.part\d+\.[^ "\.]*$`)
	regularExtPattern    = regexp.MustCompile(`\.[^ "\.]*$`)
)

func (p *NZBParser) Parse(ctx context.Context, filename string, content []byte) (nzb *storage.NZB, groups map[string]*FileGroup, err error) {
	// Recover from panics to prevent crashes
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error().Interface("panic", r).Str("filename", filename).Msg("Panic recovered in Parse")
			err = fmt.Errorf("parse panic: %v", r)
		}
	}()

	// Parse raw XML
	raw, err := nzbparser.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse NZB content: %w", err)
	}

	// Create base NZB structure
	nzb = &storage.NZB{
		Files:    []storage.NZBFile{},
		Status:   "parsed",
		Name:     determineNZBName(filename, raw.Meta),
		Title:    raw.Meta["title"],
		Password: raw.Meta["password"],
	}
	// Group files by base Name and type
	fileGroups := p.groupFiles(ctx, raw.Files)

	if len(fileGroups) == 0 {
		return nil, nil, fmt.Errorf("no valid file groups found in NZB")
	}

	p.logger.Debug().Int("groups", len(fileGroups)).Msg("NZB file groups created")

	// Stat the first segment to confirm connectivity
	checked := false
	for _, group := range fileGroups {
		if len(group.Files) == 0 || len(group.Files[0].Segments) == 0 {
			continue
		}
		segment := group.Files[0].Segments[0]
		err = p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
			_, _, statErr := conn.Stat(segment.Id)
			return statErr
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to stat segment %s <%s>: %w", group.ActualFilename, segment.Id, err)
		}
		checked = true
		break
	}
	if !checked {
		return nil, nil, fmt.Errorf("no segments available to stat in NZB")
	}

	nzb.ID = uuid.New().String()
	return nzb, fileGroups, nil
}

// extractSeason attempts to extract a season number from an NZB title.
// It matches patterns like S06, S01, or Season 6, Season 01.
// Returns the season digits (e.g. "06") or empty string if not found.
func extractSeason(name string) string {
	m := seasonRegex.FindStringSubmatch(name)
	if m != nil {
		if m[1] != "" {
			return m[1]
		}
		return m[2]
	}
	return ""
}

// looksObfuscated checks if media files still have obfuscated hash-based names
// (e.g. abc.xyz.06535e0171b4a3.mkv) where all files share the same prefix and
// only differ by a hex hash suffix. This indicates PAR2 deobfuscation didn't
// produce meaningful episode names.
func looksObfuscated(files []*storage.NZBFile) bool {
	if len(files) < 2 {
		return false
	}
	hexSuffix := regexp.MustCompile(`^(.+)\.([a-f0-9]{8,})$`)
	var commonPrefix string
	for _, f := range files {
		base := strings.TrimSuffix(f.Name, filepath.Ext(f.Name))
		m := hexSuffix.FindStringSubmatch(base)
		if m == nil {
			return false
		}
		if commonPrefix == "" {
			commonPrefix = m[1]
		} else if m[1] != commonPrefix {
			return false
		}
	}
	return commonPrefix != ""
}

func (p *NZBParser) Process(ctx context.Context, nzb *storage.NZB, groups map[string]*FileGroup) (result *storage.NZB, err error) {
	// Recover from panics to prevent crashes
	defer func() {
		if r := recover(); r != nil {
			p.logger.Error().Interface("panic", r).Str("nzb", nzb.Name).Msg("Panic recovered in Process")
			err = fmt.Errorf("process panic: %v", r)
		}
	}()

	// Parse each group (with deferred archive option)
	p.par2Descs = nil
	files := p.processFileGroups(ctx, groups, nzb.Password)

	if len(files) == 0 {
		return nil, fmt.Errorf("no valid files found in NZB")
	}

	cfg := config.Get()

	// Handle deobfuscation renaming for media files (and their subtitles)
	// Collect all media files
	var mediaFiles []*storage.NZBFile
		for i := range files {
			if files[i].FileType == storage.NZBFileTypeMedia {
				mediaFiles = append(mediaFiles, &files[i])
			}
		}

		// Sort media files by original NZB Number to ensure sequence is maintained
		sort.Slice(mediaFiles, func(i, j int) bool {
			return mediaFiles[i].Number < mediaFiles[j].Number
		})

		if len(mediaFiles) == 1 {
			// Single media file: rename to NZB name
			fileExt := filepath.Ext(mediaFiles[0].Name)
			nzbExt := filepath.Ext(nzb.Name)
			if fileExt != "" && !strings.EqualFold(nzbExt, fileExt) {
				mediaFiles[0].Name = nzb.Name + fileExt
			} else {
				mediaFiles[0].Name = nzb.Name
			}
		} else if len(mediaFiles) > 1 {
			// Check if PAR2 deobfuscation already produced unique episode names
			// (e.g. S05E01.mkv, S05E02.mkv). If names are all the same, or all
			// share a common obfuscated pattern (prefix.hex.mkv), fall back to
			// the configured fallback strategy.
			unique := make(map[string]struct{}, len(mediaFiles))
			for _, mf := range mediaFiles {
				unique[mf.Name] = struct{}{}
			}
			if len(unique) == 1 || looksObfuscated(mediaFiles) {
				for i, mf := range mediaFiles {
					fileExt := filepath.Ext(mf.Name)
					if cfg.Usenet.SeasonPackRenaming {
						season := extractSeason(nzb.Name)
						if season != "" {
							mf.Name = fmt.Sprintf("S%02sE%02d%s", season, i+1, fileExt)
						} else {
							mf.Name = fmt.Sprintf("%s - %02d%s", nzb.Name, i+1, fileExt)
						}
					} else {
						mf.Name = fmt.Sprintf("%s - %02d%s", nzb.Name, i+1, fileExt)
					}
				}
			}
		}

	skippedFiles := 0
	var skippedErr error
	// Calculate total Size
	for _, file := range files {
		if err := cfg.IsFileAllowed(file.Name, file.Size); err != nil {
			skippedFiles++
			skippedErr = err
			continue
		}
		nzb.TotalSize += file.Size
		file.NzbID = nzb.ID
		nzb.Files = append(nzb.Files, file)
	}
	if skippedFiles > 0 {
		p.logger.Info().Err(skippedErr).Int("skipped_files", skippedFiles).Str("nzb", nzb.Name).Msg("Some files were skipped due to size or extension restrictions")
	}
	if len(nzb.Files) == 0 {
		if skippedFiles > 0 {
			return nil, fmt.Errorf("all files were skipped due to size or extension restrictions(error %v)", skippedErr)
		}
		return nil, fmt.Errorf("no valid files found in NZB after processing")
	}
	return nzb, nil
}

func (p *NZBParser) groupFiles(ctx context.Context, files nzbparser.NzbFiles) map[string]*FileGroup {
	// Assign XML document order as Number for files with uniform Number values.
	// This preserves upload order for obfuscated archives where the subject
	// line doesn't contain file number patterns like [X/Y].
	if len(files) > 1 {
		allSameNumber := true
		firstNum := files[0].Number
		for _, f := range files[1:] {
			if f.Number != firstNum {
				allSameNumber = false
				break
			}
		}
		if allSameNumber {
			for i := range files {
				files[i].Number = i + 1
			}
		}
	}

	var unknownFiles []nzbparser.NzbFile
	var allFiles []contentResult

	for _, file := range files {
		if len(file.Segments) == 0 {
			continue
		}

		fileType := p.detectFileType(file.Filename)
		if fileType == storage.NZBFileTypePar2 {
			allFiles = append(allFiles, contentResult{
				file:           file,
				fileType:       fileType,
				actualFilename: file.Filename,
			})
			continue
		}

		if fileType == storage.NZBFileTypeUnknown {
			unknownFiles = append(unknownFiles, file)
		} else {
			allFiles = append(allFiles, contentResult{
				file:           file,
				fileType:       fileType,
				actualFilename: file.Filename,
			})
		}
	}

	unknownResults := p.batchDetectContentTypes(ctx, unknownFiles)

	// Add unknown results
	allFiles = append(allFiles, unknownResults...)

	groups := p.groupProcessedFiles(allFiles)

	// Merge obfuscated RAR groups - when subjects are random strings,
	// each RAR volume gets its own group. This merges them back together.
	groups = p.mergeObfuscatedRarGroups(groups)

	return groups
}

// mergeObfuscatedRarGroups detects and merges RAR FileGroups that likely belong
// to the same multi-volume archive but couldn't be grouped due to obfuscated
// subjects/filenames.
//
// Obfuscation detection: When an NZB has random subjects (e.g., "yXIBWWn7qKVUVpS6")
// instead of descriptive filenames (e.g., "movie.part01.rar"), each RAR volume
// ends up in its own single-file group. This function merges those back together.
func (p *NZBParser) mergeObfuscatedRarGroups(groups map[string]*FileGroup) map[string]*FileGroup {
	// Collect all single-file RAR groups (potential obfuscation victims)
	var singleFileRarGroups []*FileGroup
	var otherGroups []*FileGroup

	for _, group := range groups {
		if group.Type == storage.NZBFileTypeRar && len(group.Files) == 1 {
			singleFileRarGroups = append(singleFileRarGroups, group)
		} else {
			otherGroups = append(otherGroups, group)
		}
	}

	// If we have multiple single-file RAR groups, this is likely obfuscation
	// Merge them into a single group
	if len(singleFileRarGroups) > 1 {
		p.logger.Debug().
			Int("single_file_rar_groups", len(singleFileRarGroups)).
			Msg("Detected potential obfuscated RAR archive, merging groups")

		// Create a merged group using the first group as base
		mergedGroup := &FileGroup{
			BaseName:       singleFileRarGroups[0].BaseName,
			ActualFilename: singleFileRarGroups[0].ActualFilename,
			Type:           storage.NZBFileTypeRar,
			Files:          make([]nzbparser.NzbFile, 0, len(singleFileRarGroups)),
			Groups:         make(map[string]struct{}),
		}

		// Merge all files from single-file RAR groups
		for _, group := range singleFileRarGroups {
			mergedGroup.Files = append(mergedGroup.Files, group.Files...)
			for g := range group.Groups {
				mergedGroup.Groups[g] = struct{}{}
			}
		}

		// Sort merged files by their NZB file Number (index in original NZB)
		// This preserves upload order which typically matches volume order
		// for multi-volume RAR archives uploaded sequentially
		sort.Slice(mergedGroup.Files, func(i, j int) bool {
			// Use the NZB file Number field which represents order in NZB
			return mergedGroup.Files[i].Number < mergedGroup.Files[j].Number
		})

		// Rebuild the groups map with the merged group
		result := make(map[string]*FileGroup)
		result[mergedGroup.BaseName] = mergedGroup
		for _, group := range otherGroups {
			result[group.BaseName] = group
		}

		p.logger.Info().
			Int("merged_files", len(mergedGroup.Files)).
			Str("group_name", mergedGroup.BaseName).
			Msg("Merged obfuscated RAR groups into single group")

		return result
	}

	// No merging needed
	return groups
}

// Batch process unknown files in parallel
func (p *NZBParser) batchDetectContentTypes(ctx context.Context, unknownFiles []nzbparser.NzbFile) []contentResult {
	if len(unknownFiles) == 0 {
		return nil
	}

	// Use up to maxConcurrent workers — same budget as the rest of the parser.
	workers := min(len(unknownFiles), p.maxConcurrent)

	mapper := iter.Mapper[nzbparser.NzbFile, contentResult]{
		MaxGoroutines: workers, // limit concurrency
	}

	mapped := mapper.Map(unknownFiles, func(f *nzbparser.NzbFile) contentResult {
		// You can still pass ctx through to your inner function.
		detectedType, actualFilename, err := p.detectFileTypeByContent(ctx, *f)
		if err != nil {
			p.logger.Trace().
				Err(err).
				Str("file", f.Filename).
				Msg("Failed to detect file type by content")
		}

		return contentResult{
			file:           *f,
			fileType:       detectedType,
			actualFilename: actualFilename,
		}
	})

	processed := make([]contentResult, 0, len(mapped))
	for _, r := range mapped {
		processed = append(processed, r)
	}
	return processed
}

// Group already processed files (fast)
func (p *NZBParser) groupProcessedFiles(allFiles []contentResult) map[string]*FileGroup {
	groups := make(map[string]*FileGroup)

	for _, item := range allFiles {
		// Skip unwanted files
		if item.fileType == storage.NZBFileTypeIgnore {
			continue
		}

		// If we only got the name from yEnc, try to infer type from it.
		if item.fileType == storage.NZBFileTypeUnknown && item.actualFilename != "" {
			if detected := p.detectFileType(item.actualFilename); detected != storage.NZBFileTypeUnknown {
				item.fileType = detected
			}
		}

		var groupKey string
		if item.actualFilename != "" && item.actualFilename != item.file.Filename {
			groupKey = p.getBaseFilename(item.actualFilename)
		} else {
			groupKey = item.file.Basefilename
		}
		if groupKey == "" {
			groupKey = p.getBaseFilename(item.file.Filename)
			if groupKey == "" {
				groupKey = item.file.Filename
			}
		}

		// Keep PAR2 files in their own groups even when they share the same logical
		// base name as the actual payload archive. Releases like
		// "name.part01.rar" + "name.vol001+01.par2" should produce one RAR group
		// plus separate PAR2 groups, not a single mixed PAR2-only group.
		if item.fileType == storage.NZBFileTypePar2 {
			parName := item.actualFilename
			if parName == "" {
				parName = item.file.Filename
			}
			if parName != "" {
				groupKey = "par2::" + parName
			}
		}

		group, exists := groups[groupKey]
		if !exists {
			group = &FileGroup{
				ActualFilename: item.actualFilename,
				BaseName:       groupKey,
				Type:           item.fileType,
				Files:          []nzbparser.NzbFile{},
				fileMeta:       make(map[string]filePartMeta),
				Groups:         make(map[string]struct{}),
			}
			groups[groupKey] = group
		} else if group.Type == storage.NZBFileTypeUnknown && item.fileType != storage.NZBFileTypeUnknown {
			group.Type = item.fileType
		}
		if group.ActualFilename == "" && item.actualFilename != "" {
			group.ActualFilename = item.actualFilename
		}

		// Update filename
		item.file.Filename = item.actualFilename

		group.Files = append(group.Files, item.file)
		for _, g := range item.file.Groups {
			group.Groups[g] = struct{}{}
		}

		if item.fileSize > 0 || item.segmentSize > 0 || item.partNumber > 0 || item.partBegin > 0 {
			if group.fileMeta == nil {
				group.fileMeta = make(map[string]filePartMeta)
			}
			metaKey := fileMetaKey(item.file)
			if metaKey != "" {
				meta := group.fileMeta[metaKey]
				if meta.fileSize == 0 && item.fileSize > 0 {
					meta.fileSize = item.fileSize
				}
				if meta.segmentSize == 0 && item.segmentSize > 0 {
					meta.segmentSize = item.segmentSize
				}
				if meta.partNumber == 0 && item.partNumber > 0 {
					meta.partNumber = item.partNumber
				}
				if meta.partBegin == 0 && item.partBegin > 0 {
					meta.partBegin = item.partBegin
				}
				group.fileMeta[metaKey] = meta
			}
		}
	}

	return groups
}

func (p *NZBParser) getBaseFilename(filename string) string {
	if filename == "" {
		return ""
	}

	// First remove any quotes and trim spaces
	cleaned := strings.Trim(filename, `" -`)

	// Check for vol\d+\+\d+\.par2? (PAR2 Volume files)
	if volPar2Pattern.MatchString(cleaned) {
		return volPar2Pattern.ReplaceAllString(cleaned, "")
	}

	// Check for part\d+\.[^ "\.]* (part files like .part01.rar)

	if partPattern.MatchString(cleaned) {
		return partPattern.ReplaceAllString(cleaned, "")
	}

	// Check for [^ "\.]*\.\d+ (extensions with numbers like .7z.001, .r01, etc.)
	if extWithNumberPattern.MatchString(cleaned) {
		return extWithNumberPattern.ReplaceAllString(cleaned, "")
	}

	// Check for regular extensions [^ "\.]*

	if regularExtPattern.MatchString(cleaned) {
		return regularExtPattern.ReplaceAllString(cleaned, "")
	}

	return cleaned
}

// Simplified file type detection
func (p *NZBParser) detectFileType(filename string) storage.NZBFileType {
	lower := strings.ToLower(filename)

	// Check for media first
	if utils.IsMediaFile(lower) {
		return storage.NZBFileTypeMedia
	}

	// Check rar next
	if p.isRarFile(lower) {
		return storage.NZBFileTypeRar
	}

	if strings.HasSuffix(lower, ".par2") {
		return storage.NZBFileTypePar2
	}

	// Check for 7z files
	if sevenZMainPattern.MatchString(lower) || sevenZPartPattern.MatchString(lower) {
		return storage.NZBFileTypeSevenZip
	}

	if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".bz2") {
		if strings.HasSuffix(lower, ".zip") {
			return storage.NZBFileTypeZip
		}
		return storage.NZBFileTypeUnknown
	}

	// Check for ignored file types
	for _, ext := range ignoreExtensions {
		if strings.HasSuffix(lower, ext) {
			return storage.NZBFileTypeIgnore
		}
	}
	// Default to unknown type
	return storage.NZBFileTypeUnknown
}

// Simplified RAR detection
func (p *NZBParser) isRarFile(filename string) bool {
	return rarMainPattern.MatchString(filename) ||
		rarPartPattern.MatchString(filename) ||
		rarVolumePattern.MatchString(filename)
}

func (p *NZBParser) processFileGroups(ctx context.Context, groups map[string]*FileGroup, password string) []storage.NZBFile {
	if len(groups) == 0 {
		return nil
	}
	rarCounts, sevenZCounts, zipCounts, mediaCounts, deferredCounts := 0, 0, 0, 0, 0

	// Process PAR2 groups first to build deobfuscation data.
	// Prefer the smallest candidate first because the main/index PAR2 usually
	// has the fewest segments, while recovery volumes often contain no FileDesc packets.
	par2Groups := make([]*FileGroup, 0)
	for _, g := range groups {
		if g.Type == storage.NZBFileTypePar2 && len(g.Files) > 0 {
			par2Groups = append(par2Groups, g)
		}
	}
	sort.SliceStable(par2Groups, func(i, j int) bool {
		si := len(par2Groups[i].Files[0].Segments)
		sj := len(par2Groups[j].Files[0].Segments)
		if si != sj {
			return si < sj
		}
		if par2Groups[i].ActualFilename != par2Groups[j].ActualFilename {
			return par2Groups[i].ActualFilename < par2Groups[j].ActualFilename
		}
		return par2Groups[i].BaseName < par2Groups[j].BaseName
	})
	for _, g := range par2Groups {
		p.logger.Debug().
			Str("group", g.BaseName).
			Int("segments", len(g.Files[0].Segments)).
			Msg("Processing PAR2 group for deobfuscation")
		_, _ = p.processFileGroup(ctx, g, password)
		if len(p.par2Descs) > 0 {
			break
		}
	}

	// Convert map into slice of *values*, not pointers
	fileGroups := make([]FileGroup, 0, len(groups))
	for _, g := range groups {
		if len(g.Files) == 0 {
			continue
		}
		if g.Type == storage.NZBFileTypePar2 {
			continue
		}
		fileGroups = append(fileGroups, *g)
	}

	// Use a Mapper with limited concurrency to prevent goroutine explosion
	// when nested with RAR/archive parsers that also use parallel processing
	mapper := iter.Mapper[FileGroup, []*storage.NZBFile]{
		MaxGoroutines: p.maxConcurrent,
	}

	results := mapper.Map(fileGroups, func(g *FileGroup) []*storage.NZBFile {
		files, err := p.processFileGroup(ctx, g, password)
		if err != nil {
			p.logger.Warn().Err(err).Str("group", g.BaseName).Msg("Failed to process file group")
			return nil
		}
		return files
	})

	// Filter nils
	var files []storage.NZBFile
	for _, groupFiles := range results {
		for _, f := range groupFiles {
			if f != nil {
				files = append(files, *f)
				// Count types
				switch f.FileType {
				case storage.NZBFileTypeRar:
					rarCounts++
				case storage.NZBFileTypeSevenZip:
					sevenZCounts++
				case storage.NZBFileTypeZip:
					zipCounts++
				case storage.NZBFileTypeMedia:
					mediaCounts++
				}
			}
		}
	}

	// Count deferred archives
	for _, g := range fileGroups {
		switch g.Type {
		case storage.NZBFileTypeRar, storage.NZBFileTypeSevenZip, storage.NZBFileTypeZip:
			deferredCounts++
		}
	}

	return files
}

// Simplified individual group processing
func (p *NZBParser) processFileGroup(ctx context.Context, group *FileGroup, password string) ([]*storage.NZBFile, error) {
	if err := p.enrichGroupWithFileInfo(ctx, group); err != nil {
		return nil, err
	}

	switch group.Type {
	case storage.NZBFileTypeMedia:
		return wrapNZBFile(p.processMediaFile(group, password))
	case storage.NZBFileTypePar2:
		return p.processPar2Group(ctx, group)
	case storage.NZBFileTypeUnknown:
		if len(p.par2Descs) > 0 {
			return p.par2DeobfuscationAttempt(ctx, group, password)
		}
		return nil, fmt.Errorf("unsupported file type: %v", group.Type)
	case storage.NZBFileTypeRar:
		rarParser := NewRARParser(p.manager, p.maxConcurrent, p.logger)
		files, err := rarParser.Process(ctx, group, password)
		if err != nil && strings.Contains(err.Error(), "unknown RAR format") {
			p.logger.Warn().Str("group", group.BaseName).Msg("RAR parser failed with unknown format, attempting fallback to SevenZip parser")
			zipParser := NewSevenZParser(p.manager, p.maxConcurrent, p.logger)
			files, err = zipParser.Process(ctx, group, password)
			if err != nil && strings.Contains(err.Error(), "unexpected id") {
				p.logger.Warn().Str("group", group.BaseName).Msg("SevenZip parser also failed, attempting fallback to ZIP parser")
				realZipParser := NewZIPParser(p.manager, p.maxConcurrent, p.logger)
				files, err = realZipParser.Process(ctx, group, password)
				if err != nil && (strings.Contains(err.Error(), "central directory") || strings.Contains(err.Error(), "signature not found")) {
					return p.par2DeobfuscationAttempt(ctx, group, password)
				}
			}
		}
		return files, err
	case storage.NZBFileTypeSevenZip:
		zipParser := NewSevenZParser(p.manager, p.maxConcurrent, p.logger)
		files, err := zipParser.Process(ctx, group, password)
		if err != nil && strings.Contains(err.Error(), "unexpected id") {
			p.logger.Warn().Str("group", group.BaseName).Msg("SevenZip parser failed with unexpected id, attempting fallback to RAR parser")
			rarParser := NewRARParser(p.manager, p.maxConcurrent, p.logger)
			files, err = rarParser.Process(ctx, group, password)
			if err != nil && strings.Contains(err.Error(), "unknown RAR format") {
				p.logger.Warn().Str("group", group.BaseName).Msg("RAR parser also failed, attempting fallback to ZIP parser")
				realZipParser := NewZIPParser(p.manager, p.maxConcurrent, p.logger)
				files, err = realZipParser.Process(ctx, group, password)
				if err != nil && (strings.Contains(err.Error(), "central directory") || strings.Contains(err.Error(), "signature not found")) {
					return p.par2DeobfuscationAttempt(ctx, group, password)
				}
			}
		}
		return files, err
	case storage.NZBFileTypeZip:
		zipParser := NewZIPParser(p.manager, p.maxConcurrent, p.logger)
		files, err := zipParser.Process(ctx, group, password)
		if err != nil && (strings.Contains(err.Error(), "central directory") || strings.Contains(err.Error(), "signature not found")) {
			p.logger.Warn().Str("group", group.BaseName).Msg("ZIP parser failed, attempting fallback to SevenZip parser")
			sevenZParser := NewSevenZParser(p.manager, p.maxConcurrent, p.logger)
			files, err = sevenZParser.Process(ctx, group, password)
			if err != nil && strings.Contains(err.Error(), "unexpected id") {
				p.logger.Warn().Str("group", group.BaseName).Msg("SevenZip parser also failed, attempting fallback to RAR parser")
				rarParser := NewRARParser(p.manager, p.maxConcurrent, p.logger)
				files, err = rarParser.Process(ctx, group, password)
				if err != nil && strings.Contains(err.Error(), "unknown RAR format") {
					return p.par2DeobfuscationAttempt(ctx, group, password)
				}
			}
		}
		return files, err
	default:
		return nil, fmt.Errorf("unsupported file type: %v", group.Type)
	}
}

func (p *NZBParser) enrichGroupWithFileInfo(ctx context.Context, group *FileGroup) error {
	if len(group.Files) == 0 {
		return nil
	}

	sort.Slice(group.Files, func(i, j int) bool {
		if group.Files[i].Number != group.Files[j].Number {
			return group.Files[i].Number < group.Files[j].Number
		}
		return group.Files[i].Filename < group.Files[j].Filename
	})

	// Use a mapper to fetch metadata for all files.
	// This is necessary because raw split files or obfuscated archives
	// can have varying number of segments and sizes per part.
	type fetchResult struct {
		index int
		meta  filePartMeta
		err   error
	}

	indices := make([]int, len(group.Files))
	for i := range indices {
		indices[i] = i
	}

	workers := min(len(group.Files), p.maxConcurrent)
	mapper := iter.Mapper[int, fetchResult]{
		MaxGoroutines: workers,
	}

	results := mapper.Map(indices, func(idx *int) fetchResult {
		i := *idx
		file := group.Files[i]
		if len(file.Segments) == 0 {
			return fetchResult{i, filePartMeta{}, fmt.Errorf("no segments in file %d", i)}
		}

		var data *nntp.YencMetadata
		err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
			d, e := conn.GetHeaderPrefix(file.Segments[0].Id, metadataOnly)
			data = d
			return e
		})

		if err != nil {
			return fetchResult{i, filePartMeta{}, err}
		}

		return fetchResult{
			index: i,
			meta: filePartMeta{
				fileSize:    data.Size,
				segmentSize: data.End - data.Begin + 1,
			},
		}
	})

	if group.fileMeta == nil {
		group.fileMeta = make(map[string]filePartMeta)
	}

	for _, res := range results {
		if res.err != nil {
			p.logger.Debug().Err(res.err).Int("index", res.index).Msg("Failed to fetch metadata for file part, will use estimation")
			continue
		}

		metaKey := fileMetaKey(group.Files[res.index])
		group.fileMeta[metaKey] = res.meta
	}

	// Set group-wide metadata for backward compatibility (using first file as reference)
	firstMetaKey := fileMetaKey(group.Files[0])
	firstMeta, hasFirst := group.fileMeta[firstMetaKey]
	if !hasFirst {
		// Fallback to estimation if first file fetch failed
		reportedBytes := int64(group.Files[0].Segments[0].Bytes)
		if reportedBytes <= 0 {
			reportedBytes = 750000
		}
		segmentSize := int64(float64(reportedBytes) * 0.97)
		firstMeta = filePartMeta{
			segmentSize: segmentSize,
			fileSize:    segmentSize * int64(len(group.Files[0].Segments)),
		}
	}

	lastMetaKey := fileMetaKey(group.Files[len(group.Files)-1])
	lastMeta, hasLast := group.fileMeta[lastMetaKey]
	if !hasLast {
		lastMeta = filePartMeta{
			fileSize: firstMeta.segmentSize * int64(len(group.Files[len(group.Files)-1].Segments)),
		}
	}

	group.metadata = &fileAnalysisResult{
		fileSize:     firstMeta.fileSize,
		lastFileSize: lastMeta.fileSize,
		segmentSize:  firstMeta.segmentSize,
	}

	return nil
}

// Process regular media files
func (p *NZBParser) processMediaFile(group *FileGroup, password string) *storage.NZBFile {
	if len(group.Files) == 0 {
		return nil
	}

	// Sort files for consistent ordering
	sort.Slice(group.Files, func(i, j int) bool {
		return group.Files[i].Number < group.Files[j].Number
	})

	// Determine extension
	ext := determineExtension(group)
	if ext == "" {
		ext = filepath.Ext(group.ActualFilename)
	}

	// If the file was falsely classified as an archive initially, its extension might be fake (e.g. .001)
	if ext != "" && (regexp.MustCompile(`^\.\d+$`).MatchString(ext) || ext == ".rar" || ext == ".7z") {
		// Attempt to guess correct media extension from group BaseName or default to .mkv
		baseExt := filepath.Ext(group.BaseName)
		if utils.IsMediaFile(group.BaseName) {
			ext = baseExt
		} else {
			// fallback extension since it's raw media
			ext = ".mkv"
		}
	}

	if ext == "" {
		return nil
	}

	name := group.BaseName
	if !strings.HasSuffix(strings.ToLower(name), strings.ToLower(ext)) {
		name = name + ext
	}

	file := &storage.NZBFile{
		Name:     name,
		Groups:   getGroupsList(group.Groups),
		Segments: []storage.NZBSegment{},
		Password: password,
		FileType: group.Type,
		Number:   group.Files[0].Number,
	}

	currentOffset := int64(0)
	for index, nzbFile := range group.Files {
		totalSize, segments := getNZBSegments(index, nzbFile, group)
		file.Segments = append(file.Segments, segments...)
		currentOffset += totalSize
	}
	file.Size = currentOffset
	return file
}

func (p *NZBParser) detectFileTypeByContent(ctx context.Context, file nzbparser.NzbFile) (storage.NZBFileType, string, error) {
	if len(file.Segments) == 0 {
		return storage.NZBFileTypeUnknown, "", fmt.Errorf("no segments in file %s", file.Filename)
	}

	// Download first segment to check file signature
	firstSegment := file.Segments[0]
	var data *nntp.YencMetadata
	// GetHeaderPrefix returns the connection to the pool after draining;
	// only a small snippet is needed for magic-byte / filename detection.
	err := p.manager.ExecuteWithFailover(ctx, func(conn *nntp.Connection) error {
		d, e := conn.GetHeaderPrefix(firstSegment.Id, defaultMaxSnippetSize)
		data = d
		return e
	})
	if err != nil {
		return storage.NZBFileTypeUnknown, "", fmt.Errorf("failed to fetch segment header for file %s: %w", file.Filename, err)
	}

	if data.Name != "" {
		fileType := p.detectFileType(data.Name)
		if fileType != storage.NZBFileTypeUnknown {
			return fileType, data.Name, nil
		}
	}

	return p.detectFileTypeFromContent(data.Snippet), data.Name, nil
}

func (p *NZBParser) detectFileTypeFromContent(data []byte) storage.NZBFileType {
	if len(data) == 0 {
		return storage.NZBFileTypeUnknown
	}

	// Check for RAR signatures (both RAR 4.x and 5.x)
	if len(data) >= 7 {
		// RAR 4.x signature
		if bytes.Equal(data[:7], []byte("Rar!\x1A\x07\x00")) {
			return storage.NZBFileTypeRar
		}
	}
	if len(data) >= 8 {
		// RAR 5.x signature
		if bytes.Equal(data[:8], []byte("Rar!\x1A\x07\x01\x00")) {
			return storage.NZBFileTypeRar
		}
	}

	// Check for ZIP signature
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4B, 0x03, 0x04}) {
		return storage.NZBFileTypeZip
	}

	// Check for 7z signature
	if len(data) >= 6 && bytes.Equal(data[:6], []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}) {
		return storage.NZBFileTypeSevenZip
	}

	// Check for common media file signatures
	if len(data) >= 4 {
		// Matroska (MKV/WebM)
		if bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}) {
			return storage.NZBFileTypeMedia
		}

		// MP4/MOV (check for 'ftyp' at offset 4)
		if len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp")) {
			return storage.NZBFileTypeMedia
		}

		// AVI
		if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) &&
			bytes.Equal(data[8:12], []byte("AVI ")) {
			return storage.NZBFileTypeMedia
		}
	}

	// MPEG checks need more specific patterns
	if len(data) >= 4 {
		// MPEG-1/2 Program Stream
		if bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xBA}) {
			return storage.NZBFileTypeMedia
		}

		// MPEG-1/2 Video Stream
		if bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xB3}) {
			return storage.NZBFileTypeMedia
		}
	}

	// Check for Transport Stream (TS files)
	if len(data) >= 1 && data[0] == 0x47 {
		// Additional validation for TS files
		if len(data) >= 188 && data[188] == 0x47 {
			return storage.NZBFileTypeMedia
		}
	}

	return storage.NZBFileTypeUnknown
}
