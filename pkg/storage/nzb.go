package storage

import (
	"fmt"
	"time"
)

type NZBFileType string

const (
	NZBFileTypeMedia    NZBFileType = "media"   // Media files (.mkv, .mp4, .avi, .mp3, etc.)
	NZBFileTypeRar      NZBFileType = "rar"     // RAR archives (.rar, .r00, .r01, etc.)
	NZBFileTypeSevenZip NZBFileType = "7z"      // 7z archives (.7z, .001, .002, etc.)
	NZBFileTypeZip      NZBFileType = "zip"     // ZIP archives (.zip, .z01, .z02, etc.)
	NZBFileTypePar2     NZBFileType = "par2"    // PAR2 files (.par2)
	NZBFileTypeIgnore   NZBFileType = "ignore"  // Files to ignore (.nfo, .txt,par2 etc.)
	NZBFileTypeUnknown  NZBFileType = "unknown" // Unknown file type
)

// NZB represents a torrent-like structure for NZB files
type NZB struct {
	ID             string    `json:"id" msgpack:"id"`
	Name           string    `json:"name" msgpack:"name"`
	Title          string    `json:"title,omitempty" msgpack:"title,omitempty"`
	Path           string    `json:"path,omitempty" msgpack:"path,omitempty"`
	TotalSize      int64     `json:"total_size" msgpack:"total_size"`
	DatePosted     time.Time `json:"date_posted" msgpack:"date_posted"`
	Category       string    `json:"category" msgpack:"category"`
	Groups         []string  `json:"groups" msgpack:"groups"`
	Files          []NZBFile `json:"logical_files" msgpack:"logical_files"`
	Downloaded     bool      `json:"downloaded" msgpack:"downloaded"`
	AddedOn        time.Time `json:"added_on" msgpack:"added_on"`
	LastActivity   time.Time `json:"last_activity" msgpack:"last_activity"`
	Status         string    `json:"status" msgpack:"status"`
	Progress       float64   `json:"progress" msgpack:"progress"`
	Percentage     float64   `json:"percentage" msgpack:"percentage"`
	SizeDownloaded int64     `json:"size_downloaded" msgpack:"size_downloaded"`
	ETA            int64     `json:"eta" msgpack:"eta"`
	Speed          int64     `json:"speed" msgpack:"speed"`
	CompletedOn    time.Time `json:"completed_on" msgpack:"completed_on"`
	IsBad          bool      `json:"is_bad" msgpack:"is_bad"`
	Storage        string    `json:"storage" msgpack:"storage"`
	FailMessage    string    `json:"fail_message,omitempty" msgpack:"fail_message,omitempty"`
	Password       string    `json:"password,omitempty" msgpack:"password,omitempty"`
}

// NZBFile represents a grouped file with its Segments
type NZBFile struct {
	NzbID         string       `json:"nzo_id" msgpack:"nzo_id"`
	Name          string       `json:"name" msgpack:"name"`
	InternalPath  string       `json:"internal_path,omitempty" msgpack:"internal_path,omitempty"` // Path within archive (for archived files)
	LocalPath     string       `json:"local_path,omitempty" msgpack:"local_path,omitempty"`       // Optional on-disk path for repaired/extracted files
	Size          int64        `json:"size" msgpack:"size"`
	StartOffset   int64        `json:"start_offset" msgpack:"start_offset"`
	Segments      []NZBSegment `json:"segments" msgpack:"segments"`
	Groups        []string     `json:"groups" msgpack:"groups"`
	Number        int          `json:"number,omitempty" msgpack:"number,omitempty"`             // Original sequence number from NZB
	FileType      NZBFileType  `json:"archive_type,omitempty" msgpack:"archive_type,omitempty"` // Type of the file (media, rar, 7z, zip, par2, ignore, unknown)
	Password      string       `json:"password,omitempty" msgpack:"password,omitempty"`
	IsDeleted     bool         `json:"is_deleted" msgpack:"is_deleted"`
	IsStored      bool         `json:"is_stored,omitempty" msgpack:"is_stored,omitempty"`           // True if stored without compression (seekable)
	SegmentSize   int64        `json:"segment_size,omitempty" msgpack:"segment_size,omitempty"`     // Size of each segment in bytes, if applicable
	EncryptionKey []byte       `json:"encryption_key,omitempty" msgpack:"encryption_key,omitempty"` // AES-256 key for encrypted files (32 bytes)
	EncryptionIV  []byte       `json:"encryption_iv,omitempty" msgpack:"encryption_iv,omitempty"`   // AES IV for encrypted files (16 bytes, from file extra area)
	IsEncrypted   bool         `json:"is_encrypted,omitempty" msgpack:"is_encrypted,omitempty"`     // True if file data is encrypted
}

func (nzb *NZB) GetFileByName(name string) *NZBFile {
	for i := range nzb.Files {
		f := nzb.Files[i]
		if f.IsDeleted {
			continue
		}
		if nzb.Files[i].Name == name {
			return &nzb.Files[i]
		}
	}
	return nil
}

func (nzb *NZB) MarkFileAsRemoved(fileName string) error {
	for i, file := range nzb.Files {
		if file.Name == fileName {
			// Mark the file as deleted
			nzb.Files[i].IsDeleted = true
			return nil
		}
	}
	return fmt.Errorf("file %s not found in NZB %s", fileName, nzb.ID)
}

func (nf *NZBFile) GetCacheKey() string {
	return fmt.Sprintf("rar_%s_%d", nf.Name, nf.Size)
}

func (nzb *NZB) GetFiles() []NZBFile {
	files := make([]NZBFile, 0, len(nzb.Files))
	for _, file := range nzb.Files {
		if !file.IsDeleted {
			files = append(files, file)
		}
	}
	return files[:len(files):len(files)] // Return a slice to avoid aliasing
}

// NZBSegment represents a segment with all necessary download info
type NZBSegment struct {
	Number           int    `json:"number" msgpack:"number"`
	MessageID        string `json:"message_id" msgpack:"message_id"`
	Bytes            int64  `json:"bytes" msgpack:"bytes"`                           // Size of data to read from this segment
	StartOffset      int64  `json:"start_offset" msgpack:"start_offset"`             // Position in the OUTPUT file where this segment's data goes
	EndOffset        int64  `json:"end_offset" msgpack:"end_offset"`                 // End position in the OUTPUT file
	Group            string `json:"group"`                                           // Newsgroup
	SegmentDataStart int64  `json:"segment_data_start" msgpack:"segment_data_start"` // Offset within the decoded NNTP segment where reading should begin (for sliced reads)
}

// ArchiveVolumeInfo holds metadata about archive volumes (internal parser use only)
type ArchiveVolumeInfo struct {
	Name         string
	Size         int64
	SegmentStart int
	SegmentEnd   int
}

// ExtractedFileInfo contains metadata for an extracted file from an archive
type ExtractedFileInfo struct {
	FileName     string
	InternalPath string
	FileSize     int64
	DataOffset   int64 // Offset within the archive where file data starts
	IsStored     bool
	Segments     []NZBSegment
}
