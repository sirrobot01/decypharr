package usenet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Tensai75/nzbparser"
	"github.com/chrisfarms/yenc"
	"github.com/nwaples/rardecode/v2"
	"github.com/rs/zerolog"
	"github.com/sirrobot01/decypharr/internal/nntp"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sourcegraph/conc/pool"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// NZBParser provides a simplified, robust NZB parser
type NZBParser struct {
	logger zerolog.Logger
	client *nntp.Client
	cache  *SegmentCache
}

type FileGroup struct {
	BaseName       string
	ActualFilename string
	Type           FileType
	Files          []nzbparser.NzbFile
	Groups         map[string]struct{}
}

type FileInfo struct {
	Size      int64
	ChunkSize int64
	Name      string
}

// NewNZBParser creates a new simplified NZB parser
func NewNZBParser(client *nntp.Client, cache *SegmentCache, logger zerolog.Logger) *NZBParser {
	return &NZBParser{
		logger: logger.With().Str("component", "nzb_parser").Logger(),
		client: client,
		cache:  cache,
	}
}

type FileType int

const (
	FileTypeMedia   FileType = iota // Direct media files (.mkv, .mp4, etc.) // Check internal/utils.IsMediaFile
	FileTypeRar                     // RAR archives (.rar, .r00, .r01, etc.)
	FileTypeArchive                 // Other archives (.7z, .zip, etc.)
	FileTypeIgnore                  // Files to ignore (.nfo, .txt, par2 etc.)
	FileTypeUnknown
)

var (
	// RAR file patterns - simplified and more accurate
	rarMainPattern       = regexp.MustCompile(`\.rar$`)
	rarPartPattern       = regexp.MustCompile(`\.r\d{2}$`) // .r00, .r01, etc.
	rarVolumePattern     = regexp.MustCompile(`\.part\d+\.rar$`)
	ignoreExtensions     = []string{".par2", ".sfv", ".nfo", ".jpg", ".png", ".txt", ".srt", ".idx", ".sub"}
	sevenZMainPattern    = regexp.MustCompile(`\.7z$`)
	sevenZPartPattern    = regexp.MustCompile(`\.7z\.\d{3}$`)
	extWithNumberPattern = regexp.MustCompile(`\.[^ "\.]*\.\d+$`)
	volPar2Pattern       = regexp.MustCompile(`(?i)\.vol\d+\+\d+\.par2?$`)
	partPattern          = regexp.MustCompile(`(?i)\.part\d+\.[^ "\.]*$`)
	regularExtPattern    = regexp.MustCompile(`\.[^ "\.]*$`)
)

type PositionTracker struct {
	reader   io.Reader
	position int64
}

func (pt *PositionTracker) Read(p []byte) (n int, err error) {
	n, err = pt.reader.Read(p)
	pt.position += int64(n)
	return n, err
}

func (pt *PositionTracker) Position() int64 {
	return pt.position
}

func (p *NZBParser) Parse(ctx context.Context, filename string, category string, content []byte) (*NZB, error) {
	// Parse raw XML
	raw, err := nzbparser.Parse(bytes.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse NZB content: %w", err)
	}

	// Create base NZB structure
	nzb := &NZB{
		Files:    []NZBFile{},
		Status:   "parsed",
		Category: category,
		Name:     determineNZBName(filename, raw.Meta),
		Title:    raw.Meta["title"],
		Password: raw.Meta["password"],
	}
	// Group files by base name and type
	fileGroups := p.groupFiles(ctx, raw.Files)

	// Process each group
	files := p.processFileGroups(ctx, fileGroups, nzb.Password)

	nzb.ID = generateID(nzb)

	if len(files) == 0 {
		return nil, fmt.Errorf("no valid files found in NZB")
	}

	// Calculate total size
	for _, file := range files {
		nzb.TotalSize += file.Size
		file.NzbID = nzb.ID
		nzb.Files = append(nzb.Files, file)
	}
	return nzb, nil
}

func (p *NZBParser) groupFiles(ctx context.Context, files nzbparser.NzbFiles) map[string]*FileGroup {

	var unknownFiles []nzbparser.NzbFile
	var knownFiles []struct {
		file     nzbparser.NzbFile
		fileType FileType
	}

	for _, file := range files {
		if len(file.Segments) == 0 {
			continue
		}

		fileType := p.detectFileType(file.Filename)

		if fileType == FileTypeUnknown {
			unknownFiles = append(unknownFiles, file)
		} else {
			knownFiles = append(knownFiles, struct {
				file     nzbparser.NzbFile
				fileType FileType
			}{file, fileType})
		}
	}

	p.logger.Info().
		Int("known_files", len(knownFiles)).
		Int("unknown_files", len(unknownFiles)).
		Msg("File type detection")

	unknownResults := p.batchDetectContentTypes(ctx, unknownFiles)

	allFiles := make([]struct {
		file           nzbparser.NzbFile
		fileType       FileType
		actualFilename string
	}, 0, len(knownFiles)+len(unknownResults))

	// Add known files
	for _, known := range knownFiles {
		allFiles = append(allFiles, struct {
			file           nzbparser.NzbFile
			fileType       FileType
			actualFilename string
		}{known.file, known.fileType, known.file.Filename})
	}

	// Add unknown results
	allFiles = append(allFiles, unknownResults...)

	return p.groupProcessedFiles(allFiles)
}

// Batch process unknown files in parallel
func (p *NZBParser) batchDetectContentTypes(ctx context.Context, unknownFiles []nzbparser.NzbFile) []struct {
	file           nzbparser.NzbFile
	fileType       FileType
	actualFilename string
} {
	if len(unknownFiles) == 0 {
		return nil
	}

	// Use worker pool for parallel processing
	workers := min(len(unknownFiles), 10) // Max 10 concurrent downloads
	workerPool := pool.New().WithMaxGoroutines(workers).WithContext(ctx)

	type result struct {
		index          int
		file           nzbparser.NzbFile
		fileType       FileType
		actualFilename string
	}

	results := make([]result, len(unknownFiles))
	var mu sync.Mutex

	// Process each unknown file
	for i, file := range unknownFiles {
		i, file := i, file // Capture loop variables

		workerPool.Go(func(ctx context.Context) error {
			detectedType, actualFilename := p.detectFileTypeByContent(ctx, file)

			mu.Lock()
			results[i] = result{
				index:          i,
				file:           file,
				fileType:       detectedType,
				actualFilename: actualFilename,
			}
			mu.Unlock()

			return nil // Don't fail the entire batch for one file
		})
	}

	// Wait for all to complete

	if err := workerPool.Wait(); err != nil {
		return nil
	}

	// Convert results
	processedFiles := make([]struct {
		file           nzbparser.NzbFile
		fileType       FileType
		actualFilename string
	}, 0, len(results))

	for _, result := range results {
		if result.fileType != FileTypeUnknown {
			processedFiles = append(processedFiles, struct {
				file           nzbparser.NzbFile
				fileType       FileType
				actualFilename string
			}{result.file, result.fileType, result.actualFilename})
		}
	}

	return processedFiles
}

// Group already processed files (fast)
func (p *NZBParser) groupProcessedFiles(allFiles []struct {
	file           nzbparser.NzbFile
	fileType       FileType
	actualFilename string
}) map[string]*FileGroup {
	groups := make(map[string]*FileGroup)

	for _, item := range allFiles {
		// Skip unwanted files
		if item.fileType == FileTypeIgnore || item.fileType == FileTypeArchive {
			continue
		}

		var groupKey string
		if item.actualFilename != "" && item.actualFilename != item.file.Filename {
			groupKey = p.getBaseFilename(item.actualFilename)
		} else {
			groupKey = item.file.Basefilename
		}

		group, exists := groups[groupKey]
		if !exists {
			group = &FileGroup{
				ActualFilename: item.actualFilename,
				BaseName:       groupKey,
				Type:           item.fileType,
				Files:          []nzbparser.NzbFile{},
				Groups:         make(map[string]struct{}),
			}
			groups[groupKey] = group
		}

		// Update filename
		item.file.Filename = item.actualFilename

		group.Files = append(group.Files, item.file)
		for _, g := range item.file.Groups {
			group.Groups[g] = struct{}{}
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

	// Check for vol\d+\+\d+\.par2? (PAR2 volume files)
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
func (p *NZBParser) detectFileType(filename string) FileType {
	lower := strings.ToLower(filename)

	// Check for media first
	if p.isMediaFile(lower) {
		return FileTypeMedia
	}

	// Check rar next
	if p.isRarFile(lower) {
		return FileTypeRar
	}

	// Check for 7z files
	if sevenZMainPattern.MatchString(lower) || sevenZPartPattern.MatchString(lower) {
		return FileTypeArchive
	}

	if strings.HasSuffix(lower, ".zip") || strings.HasSuffix(lower, ".tar") ||
		strings.HasSuffix(lower, ".gz") || strings.HasSuffix(lower, ".bz2") {
		return FileTypeArchive
	}

	// Check for ignored file types
	for _, ext := range ignoreExtensions {
		if strings.HasSuffix(lower, ext) {
			return FileTypeIgnore
		}
	}
	// Default to unknown type
	return FileTypeUnknown
}

// Simplified RAR detection
func (p *NZBParser) isRarFile(filename string) bool {
	return rarMainPattern.MatchString(filename) ||
		rarPartPattern.MatchString(filename) ||
		rarVolumePattern.MatchString(filename)
}

func (p *NZBParser) isMediaFile(filename string) bool {
	return utils.IsMediaFile(filename)
}

func (p *NZBParser) processFileGroups(ctx context.Context, groups map[string]*FileGroup, password string) []NZBFile {
	if len(groups) == 0 {
		return nil
	}

	// Channel to collect results
	results := make(chan *NZBFile, len(groups))
	var wg sync.WaitGroup

	// Process each group concurrently
	for _, group := range groups {
		wg.Add(1)
		go func(g *FileGroup) {
			defer wg.Done()
			file := p.processFileGroup(ctx, g, password)
			results <- file // nil values are fine, we'll filter later
		}(group)
	}

	// Close results channel when all goroutines complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	var files []NZBFile
	for file := range results {
		if file != nil {
			files = append(files, *file)
		}
	}

	return files
}

// Simplified individual group processing
func (p *NZBParser) processFileGroup(ctx context.Context, group *FileGroup, password string) *NZBFile {
	switch group.Type {
	case FileTypeMedia:
		return p.processMediaFile(group, password)
	case FileTypeRar:
		return p.processRarArchive(ctx, group, password)
	case FileTypeArchive:
		return nil
	default:
		// Treat unknown files as media files with conservative estimation
		return p.processMediaFile(group, password)
	}
}

// Process regular media files
func (p *NZBParser) processMediaFile(group *FileGroup, password string) *NZBFile {
	if len(group.Files) == 0 {
		return nil
	}

	// Sort files for consistent ordering
	sort.Slice(group.Files, func(i, j int) bool {
		return group.Files[i].Number < group.Files[j].Number
	})

	// Determine extension
	ext := p.determineExtension(group)

	file := &NZBFile{
		Name:         group.BaseName + ext,
		Groups:       p.getGroupsList(group.Groups),
		Segments:     []NZBSegment{},
		Password:     password,
		IsRarArchive: false,
	}

	currentOffset := int64(0)
	ratio := 0.968
	for _, nzbFile := range group.Files {
		sort.Slice(nzbFile.Segments, func(i, j int) bool {
			return nzbFile.Segments[i].Number < nzbFile.Segments[j].Number
		})

		for _, segment := range nzbFile.Segments {

			decodedSize := int64(float64(segment.Bytes) * ratio)

			seg := NZBSegment{
				Number:      segment.Number,
				MessageID:   segment.Id,
				Bytes:       int64(segment.Bytes),
				StartOffset: currentOffset,
				EndOffset:   currentOffset + decodedSize,
				Group:       file.Groups[0],
			}

			file.Segments = append(file.Segments, seg)
			currentOffset += decodedSize
		}
	}

	fileInfo, err := p.getFileInfo(context.Background(), group)
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to get file info, using fallback")
		file.Size = currentOffset
		file.SegmentSize = currentOffset / int64(len(file.Segments)) // Average segment size
	} else {
		file.Size = fileInfo.Size
		file.SegmentSize = fileInfo.ChunkSize
	}
	return file
}

func (p *NZBParser) processRarArchive(ctx context.Context, group *FileGroup, password string) *NZBFile {
	if len(group.Files) == 0 {
		return nil
	}

	// Sort RAR files by part number
	sort.Slice(group.Files, func(i, j int) bool {
		return group.Files[i].Filename < group.Files[j].Filename
	})

	// Try to extract RAR info during parsing for better accuracy
	extractedInfo := p.extractRarInfo(ctx, group, password)

	filename := group.BaseName + ".mkv" // Default extension
	if extractedInfo != nil && extractedInfo.FileName != "" {
		filename = extractedInfo.FileName
	}

	filename = utils.RemoveInvalidChars(path.Base(filename))

	file := &NZBFile{
		Name:         filename,
		Groups:       p.getGroupsList(group.Groups),
		Segments:     []NZBSegment{},
		Password:     password,
		IsRarArchive: true,
	}

	// Build segments
	ratio := 0.968
	currentOffset := int64(0)

	for _, nzbFile := range group.Files {
		sort.Slice(nzbFile.Segments, func(i, j int) bool {
			return nzbFile.Segments[i].Number < nzbFile.Segments[j].Number
		})

		for _, segment := range nzbFile.Segments {
			decodedSize := int64(float64(segment.Bytes) * ratio)

			seg := NZBSegment{
				Number:      segment.Number,
				MessageID:   segment.Id,
				Bytes:       int64(segment.Bytes),
				StartOffset: currentOffset,
				EndOffset:   currentOffset + decodedSize,
				Group:       file.Groups[0],
			}

			file.Segments = append(file.Segments, seg)
			currentOffset += decodedSize
		}
	}

	if extractedInfo != nil {
		file.Size = extractedInfo.FileSize
		file.SegmentSize = extractedInfo.SegmentSize
		file.StartOffset = extractedInfo.EstimatedStartOffset
	} else {
		file.Size = currentOffset
		file.SegmentSize = currentOffset / int64(len(file.Segments)) // Average segment size
		file.StartOffset = 0                                         // No accurate start offset available
	}
	return file
}

func (p *NZBParser) getFileInfo(ctx context.Context, group *FileGroup) (*FileInfo, error) {
	if len(group.Files) == 0 {
		return nil, fmt.Errorf("no files in group %s", group.BaseName)
	}

	// Sort files
	sort.Slice(group.Files, func(i, j int) bool {
		return group.Files[i].Filename < group.Files[j].Filename
	})
	firstFile := group.Files[0]
	lastFile := group.Files[len(group.Files)-1]
	firstInfo, err := p.client.DownloadHeader(ctx, firstFile.Segments[0].Id)
	if err != nil {
		return nil, err
	}
	lastInfo, err := p.client.DownloadHeader(ctx, lastFile.Segments[len(lastFile.Segments)-1].Id)
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to download last segment header")
		return nil, err
	}

	chunkSize := firstInfo.End - (firstInfo.Begin - 1)
	totalFileSize := (int64(len(group.Files)-1) * firstInfo.Size) + lastInfo.Size
	return &FileInfo{
		Size:      totalFileSize,
		ChunkSize: chunkSize,
		Name:      firstInfo.Name,
	}, nil
}

func (p *NZBParser) extractRarInfo(ctx context.Context, group *FileGroup, password string) *ExtractedFileInfo {
	if len(group.Files) == 0 || len(group.Files[0].Segments) == 0 {
		return nil
	}

	firstRarFile := group.Files[0]
	segmentsToDownload := min(5, len(firstRarFile.Segments))
	headerBuffer, err := p.downloadRarHeaders(ctx, firstRarFile.Segments[:segmentsToDownload])
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to download RAR headers")
		return nil
	}

	fileInfo, err := p.getFileInfo(ctx, group)
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to get file info for RAR group")
		return nil
	}
	// Pass the actual RAR size to the analysis function
	return p.analyzeRarStructure(headerBuffer, password, fileInfo)
}

func (p *NZBParser) analyzeRarStructure(headerData []byte, password string, fileInfo *FileInfo) *ExtractedFileInfo {
	reader := bytes.NewReader(headerData)
	tracker := &PositionTracker{reader: reader, position: 0}

	rarReader, err := rardecode.NewReader(tracker, rardecode.Password(password))
	if err != nil {
		return nil
	}

	for {
		header, err := rarReader.Next()
		if err != nil {
			break
		}

		if !header.IsDir && p.isMediaFile(header.Name) {
			compressionRatio := float64(fileInfo.Size) / float64(header.UnPackedSize)

			if compressionRatio > 0.95 {
				fileDataOffset := tracker.Position()

				p.logger.Info().
					Str("file", header.Name).
					Int64("accurate_offset", fileDataOffset).
					Float64("compression_ratio", compressionRatio).
					Msg("Found accurate store RAR offset using position tracking")

				return &ExtractedFileInfo{
					FileName:             header.Name,
					FileSize:             header.UnPackedSize,
					SegmentSize:          fileInfo.ChunkSize,
					EstimatedStartOffset: fileDataOffset,
				}
			}
			break
		}

		// Skip file content - this advances the tracker position
		io.Copy(io.Discard, rarReader)
	}

	return nil
}

func (p *NZBParser) determineExtension(group *FileGroup) string {
	// Try to determine extension from filenames
	for _, file := range group.Files {
		ext := filepath.Ext(file.Filename)
		if ext != "" {
			return ext
		}
	}
	return ".mkv" // Default
}

func (p *NZBParser) getGroupsList(groups map[string]struct{}) []string {
	result := make([]string, 0, len(groups))
	for g := range groups {
		result = append(result, g)
	}
	return result
}

// Download RAR headers from segments
func (p *NZBParser) downloadRarHeaders(ctx context.Context, segments []nzbparser.NzbSegment) ([]byte, error) {
	var headerBuffer bytes.Buffer

	for _, segment := range segments {
		conn, cleanup, err := p.client.GetConnection(ctx)
		if err != nil {
			continue
		}

		data, err := conn.GetBody(segment.Id)
		cleanup()

		if err != nil {
			if !nntp.IsRetryableError(err) {
				return nil, err
			}
			continue
		}

		if len(data) == 0 {
			continue
		}

		// yEnc decode
		part, err := nntp.DecodeYenc(bytes.NewReader(data))
		if err != nil || part == nil || len(part.Body) == 0 {
			p.logger.Warn().Err(err).Str("segment_id", segment.Id).Msg("Failed to decode RAR header segment")
			continue
		}

		headerBuffer.Write(part.Body)

		// Stop if we have enough data (typically first segment is enough for headers)
		if headerBuffer.Len() > 32768 { // 32KB should be plenty for RAR headers
			break
		}
	}

	if headerBuffer.Len() == 0 {
		return nil, fmt.Errorf("no valid header data downloaded")
	}

	return headerBuffer.Bytes(), nil
}

func (p *NZBParser) detectFileTypeByContent(ctx context.Context, file nzbparser.NzbFile) (FileType, string) {
	if len(file.Segments) == 0 {
		return FileTypeUnknown, ""
	}

	// Download first segment to check file signature
	firstSegment := file.Segments[0]
	data, err := p.downloadFirstSegment(ctx, firstSegment)
	if err != nil {
		p.logger.Warn().Err(err).Msg("Failed to download first segment for content detection")
		return FileTypeUnknown, ""
	}

	if data.Name != "" {
		fileType := p.detectFileType(data.Name)
		if fileType != FileTypeUnknown {
			return fileType, data.Name
		}
	}

	return p.detectFileTypeFromContent(data.Body), data.Name
}

func (p *NZBParser) detectFileTypeFromContent(data []byte) FileType {
	if len(data) == 0 {
		return FileTypeUnknown
	}

	// Check for RAR signatures (both RAR 4.x and 5.x)
	if len(data) >= 7 {
		// RAR 4.x signature
		if bytes.Equal(data[:7], []byte("Rar!\x1A\x07\x00")) {
			return FileTypeRar
		}
	}
	if len(data) >= 8 {
		// RAR 5.x signature
		if bytes.Equal(data[:8], []byte("Rar!\x1A\x07\x01\x00")) {
			return FileTypeRar
		}
	}

	// Check for ZIP signature
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{0x50, 0x4B, 0x03, 0x04}) {
		return FileTypeArchive
	}

	// Check for 7z signature
	if len(data) >= 6 && bytes.Equal(data[:6], []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}) {
		return FileTypeArchive
	}

	// Check for common media file signatures
	if len(data) >= 4 {
		// Matroska (MKV/WebM)
		if bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}) {
			return FileTypeMedia
		}

		// MP4/MOV (check for 'ftyp' at offset 4)
		if len(data) >= 8 && bytes.Equal(data[4:8], []byte("ftyp")) {
			return FileTypeMedia
		}

		// AVI
		if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) &&
			bytes.Equal(data[8:12], []byte("AVI ")) {
			return FileTypeMedia
		}
	}

	// MPEG checks need more specific patterns
	if len(data) >= 4 {
		// MPEG-1/2 Program Stream
		if bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xBA}) {
			return FileTypeMedia
		}

		// MPEG-1/2 Video Stream
		if bytes.Equal(data[:4], []byte{0x00, 0x00, 0x01, 0xB3}) {
			return FileTypeMedia
		}
	}

	// Check for Transport Stream (TS files)
	if len(data) >= 1 && data[0] == 0x47 {
		// Additional validation for TS files
		if len(data) >= 188 && data[188] == 0x47 {
			return FileTypeMedia
		}
	}

	return FileTypeUnknown
}

func (p *NZBParser) downloadFirstSegment(ctx context.Context, segment nzbparser.NzbSegment) (*yenc.Part, error) {
	conn, cleanup, err := p.client.GetConnection(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	data, err := conn.GetBody(segment.Id)
	if err != nil {
		return nil, err
	}

	// yEnc decode
	part, err := nntp.DecodeYenc(bytes.NewReader(data))
	if err != nil || part == nil {
		return nil, fmt.Errorf("failed to decode segment")
	}

	// Return both the filename and decoded data
	return part, nil
}

// Calculate total archive size from all RAR parts in the group
func (p *NZBParser) calculateTotalArchiveSize(group *FileGroup) int64 {
	var total int64
	for _, file := range group.Files {
		for _, segment := range file.Segments {
			total += int64(segment.Bytes)
		}
	}
	return total
}

func determineNZBName(filename string, meta map[string]string) string {
	// Prefer filename if it exists
	if filename != "" {
		filename = strings.Replace(filename, filepath.Ext(filename), "", 1)
	} else {
		if name := meta["name"]; name != "" {
			filename = name
		} else if title := meta["title"]; title != "" {
			filename = title
		}
	}
	return utils.RemoveInvalidChars(filename)
}

func generateID(nzb *NZB) string {
	h := sha256.New()
	h.Write([]byte(nzb.Name))
	h.Write([]byte(fmt.Sprintf("%d", nzb.TotalSize)))
	h.Write([]byte(nzb.Category))
	h.Write([]byte(nzb.Password))
	return hex.EncodeToString(h.Sum(nil))[:16]
}
