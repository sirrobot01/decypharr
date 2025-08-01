package usenet

import "time"

// NZB represents a torrent-like structure for NZB files
type NZB struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Title          string        `json:"title,omitempty"`
	TotalSize      int64         `json:"total_size"`
	DatePosted     time.Time     `json:"date_posted"`
	Category       string        `json:"category"`
	Groups         []string      `json:"groups"`
	Files          []NZBFile     `json:"files"`
	Downloaded     bool          `json:"downloaded"` // Whether the NZB has been downloaded
	StreamingInfo  StreamingInfo `json:"streaming_info"`
	AddedOn        time.Time     `json:"added_on"`        // When the NZB was added to the system
	LastActivity   time.Time     `json:"last_activity"`   // Last activity timestamp
	Status         string        `json:"status"`          // "queued", "downloading", "completed", "failed"
	Progress       float64       `json:"progress"`        // Percentage of download completion
	Percentage     float64       `json:"percentage"`      // Percentage of download completion
	SizeDownloaded int64         `json:"size_downloaded"` // Total size downloaded so far
	ETA            int64         `json:"eta"`             // Estimated time of arrival in seconds
	Speed          int64         `json:"speed"`           // Download speed in bytes per second
	CompletedOn    time.Time     `json:"completed_on"`    // When the NZB was completed
	IsBad          bool          `json:"is_bad"`
	Storage        string        `json:"storage"`
	FailMessage    string        `json:"fail_message,omitempty"` // Error message if the download failed
	Password       string        `json:"-,omitempty"`            // Password for encrypted RAR files
}

// StreamingInfo contains metadata for streaming capabilities
type StreamingInfo struct {
	IsStreamable  bool  `json:"is_streamable"`
	MainFileIndex int   `json:"main_file_index"` // Index of the main media file
	HasParFiles   bool  `json:"has_par_files"`
	HasRarFiles   bool  `json:"has_rar_files"`
	TotalSegments int   `json:"total_segments"`
	EstimatedTime int64 `json:"estimated_time"` // Estimated download time in seconds
}

type SegmentValidationInfo struct {
	ExpectedSize int64
	ActualSize   int64
	Validated    bool
}

// NZBFile represents a grouped file with its segments
type NZBFile struct {
	NzbID             string                            `json:"nzo_id"`
	Name              string                            `json:"name"`
	Size              int64                             `json:"size"`
	StartOffset       int64                             `json:"start_offset"` // This is useful for removing rar headers
	Segments          []NZBSegment                      `json:"segments"`
	Groups            []string                          `json:"groups"`
	SegmentValidation map[string]*SegmentValidationInfo `json:"-"`
	IsRarArchive      bool                              `json:"is_rar_archive"`     // Whether this file is a RAR archive that needs extraction
	Password          string                            `json:"password,omitempty"` // Password for encrypted RAR files
	IsDeleted         bool                              `json:"is_deleted"`
	SegmentSize       int64                             `json:"segment_size,omitempty"` // Size of each segment in bytes, if applicable
}

// NZBSegment represents a segment with all necessary download info
type NZBSegment struct {
	Number      int    `json:"number"`
	MessageID   string `json:"message_id"`
	Bytes       int64  `json:"bytes"`
	StartOffset int64  `json:"start_offset"` // Byte offset within the file
	EndOffset   int64  `json:"end_offset"`   // End byte offset within the file
	Group       string `json:"group"`
}

// CompactNZB is a space-optimized version of NZB for storage
type CompactNZB struct {
	ID          string        `json:"i"`
	Name        string        `json:"n"`
	Status      string        `json:"s"`
	Category    string        `json:"c"`
	Size        int64         `json:"sz"`
	Progress    float64       `json:"p"`
	Speed       int64         `json:"sp,omitempty"`
	ETA         int64         `json:"e,omitempty"`
	Added       int64         `json:"a"`            // Unix timestamp
	Modified    int64         `json:"m"`            // Unix timestamp
	Complete    int64         `json:"co,omitempty"` // Unix timestamp
	Groups      []string      `json:"g,omitempty"`
	Files       []CompactFile `json:"f,omitempty"`
	Storage     string        `json:"st,omitempty"` // Storage path
	FailMessage string        `json:"fm,omitempty"` // Error message if the download failed
	Downloaded  bool          `json:"d,omitempty"`
}

// CompactFile represents a file in compact format
type CompactFile struct {
	Name              string             `json:"n"`
	Size              int64              `json:"s"`
	Type              string             `json:"t"`
	Main              bool               `json:"m,omitempty"`
	Offset            int64              `json:"o"`
	Segments          []CompactSegment   `json:"seg,omitempty"`
	IsRar             bool               `json:"r,omitempty"`
	Password          string             `json:"p,omitempty"`
	IsDeleted         bool               `json:"del,omitempty"` // Whether the file is marked as deleted
	ExtractedFileInfo *ExtractedFileInfo `json:"efi,omitempty"` // Pre-extracted RAR file info
	SegmentSize       int64              `json:"ss,omitempty"`  // Size of each segment in bytes, if applicable
}

// CompactSegment represents a segment in compact format
type CompactSegment struct {
	Number      int    `json:"n"`           // Segment number
	MessageID   string `json:"mid"`         // Message-ID of the segment
	Bytes       int64  `json:"b"`           // Size in bytes
	StartOffset int64  `json:"so"`          // Start byte offset within the file
	EndOffset   int64  `json:"eo"`          // End byte offset within the file
	Group       string `json:"g,omitempty"` // Group associated with this segment
}

type ExtractedFileInfo struct {
	FileName             string `json:"fn,omitempty"`
	FileSize             int64  `json:"fs,omitempty"`
	ArchiveSize          int64  `json:"as,omitempty"`  // Total size of the RAR archive
	EstimatedStartOffset int64  `json:"eso,omitempty"` // Estimated start offset in the archive
	SegmentSize          int64  `json:"ss,omitempty"`  // Size of each segment in the archive
}

// toCompact converts NZB to compact format
func (nzb *NZB) toCompact() *CompactNZB {
	compact := &CompactNZB{
		ID:          nzb.ID,
		Name:        nzb.Name,
		Status:      nzb.Status,
		Category:    nzb.Category,
		Size:        nzb.TotalSize,
		Progress:    nzb.Progress,
		Speed:       nzb.Speed,
		ETA:         nzb.ETA,
		Added:       nzb.AddedOn.Unix(),
		Modified:    nzb.LastActivity.Unix(),
		Storage:     nzb.Storage,
		Downloaded:  nzb.Downloaded,
		FailMessage: nzb.FailMessage,
	}

	if !nzb.CompletedOn.IsZero() {
		compact.Complete = nzb.CompletedOn.Unix()
	}

	// Only store essential groups (first 3)
	if len(nzb.Groups) > 0 {
		maxGroups := 3
		if len(nzb.Groups) < maxGroups {
			maxGroups = len(nzb.Groups)
		}
		compact.Groups = nzb.Groups[:maxGroups]
	}

	// Store only essential file info
	if len(nzb.Files) > 0 {
		compact.Files = make([]CompactFile, len(nzb.Files))
		for i, file := range nzb.Files {
			compact.Files[i] = file.toCompact()
		}
	}

	return compact
}

// fromCompact converts compact format back to NZB
func (compact *CompactNZB) toNZB() *NZB {
	nzb := &NZB{
		ID:           compact.ID,
		Name:         compact.Name,
		Status:       compact.Status,
		Category:     compact.Category,
		TotalSize:    compact.Size,
		Progress:     compact.Progress,
		Percentage:   compact.Progress,
		Speed:        compact.Speed,
		ETA:          compact.ETA,
		Groups:       compact.Groups,
		AddedOn:      time.Unix(compact.Added, 0),
		LastActivity: time.Unix(compact.Modified, 0),
		Storage:      compact.Storage,
		Downloaded:   compact.Downloaded,
		FailMessage:  compact.FailMessage,
		StreamingInfo: StreamingInfo{
			MainFileIndex: -1,
		},
	}

	if compact.Complete > 0 {
		nzb.CompletedOn = time.Unix(compact.Complete, 0)
	}

	// Reconstruct files
	if len(compact.Files) > 0 {
		nzb.Files = make([]NZBFile, len(compact.Files))
		for i, file := range compact.Files {
			nzb.Files[i] = file.toNZB()
		}

		// Set streaming info
		nzb.StreamingInfo.TotalSegments = len(compact.Files)
		nzb.StreamingInfo.IsStreamable = nzb.StreamingInfo.MainFileIndex >= 0
	}

	return nzb
}

func (nf *NZBFile) toCompact() CompactFile {
	compact := CompactFile{
		Name:        nf.Name,
		Size:        nf.Size,
		Offset:      nf.StartOffset,
		IsRar:       nf.IsRarArchive,
		IsDeleted:   nf.IsDeleted,
		Password:    nf.Password,
		SegmentSize: nf.SegmentSize,
	}
	for _, seg := range nf.Segments {
		compact.Segments = append(compact.Segments, CompactSegment(seg))
	}
	return compact
}
func (compact *CompactFile) toNZB() NZBFile {
	f := NZBFile{
		Name:         compact.Name,
		Size:         compact.Size,
		StartOffset:  compact.Offset,
		IsRarArchive: compact.IsRar,
		Password:     compact.Password,
		IsDeleted:    compact.IsDeleted,
		SegmentSize:  compact.SegmentSize,
	}
	for _, seg := range compact.Segments {
		f.Segments = append(f.Segments, NZBSegment(seg))
	}
	return f
}
