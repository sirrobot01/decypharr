package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirrobot01/decypharr/internal/logger"
	"github.com/sirrobot01/decypharr/internal/utils"
	"github.com/sirrobot01/decypharr/pkg/arr"
)

type Torrent struct {
	Id               string          `json:"id"`
	InfoHash         string          `json:"info_hash"`
	Name             string          `json:"name"`
	Folder           string          `json:"folder"`
	Filename         string          `json:"filename"`
	OriginalFilename string          `json:"original_filename"`
	Size             int64           `json:"size"`
	Bytes            int64           `json:"bytes"` // Size of only the files that are downloaded
	Magnet           *utils.Magnet   `json:"magnet"`
	Files            map[string]File `json:"files"`
	Status           string          `json:"status"`
	Added            string          `json:"added"`
	Progress         float64         `json:"progress"`
	Speed            int64           `json:"speed"`
	Seeders          int             `json:"seeders"`
	Links            []string        `json:"links"`
	MountPath        string          `json:"mount_path"`
	DeletedFiles     []string        `json:"deleted_files"`

	Debrid string `json:"debrid"`

	Arr *arr.Arr `json:"arr"`

	SizeDownloaded   int64 `json:"-"` // This is used for local download
	DownloadUncached bool  `json:"-"`

	sync.Mutex
}

func (t *Torrent) Copy() *Torrent {
	t.Lock()
	defer t.Unlock()

	newFiles := make(map[string]File, len(t.Files))
	for k, v := range t.Files {
		newFiles[k] = v
	}

	return &Torrent{
		Id:               t.Id,
		InfoHash:         t.InfoHash,
		Name:             t.Name,
		Folder:           t.Folder,
		Filename:         t.Filename,
		OriginalFilename: t.OriginalFilename,
		Size:             t.Size,
		Bytes:            t.Bytes,
		Magnet:           t.Magnet,
		Files:            newFiles,
		Status:           t.Status,
		Added:            t.Added,
		Progress:         t.Progress,
		Speed:            t.Speed,
		Seeders:          t.Seeders,
		Links:            append([]string{}, t.Links...),
		MountPath:        t.MountPath,
		Debrid:           t.Debrid,
		Arr:              t.Arr,
	}
}

func (t *Torrent) GetSymlinkFolder(parent string) string {
	return filepath.Join(parent, t.Arr.Name, t.Folder)
}

func (t *Torrent) GetMountFolder(rClonePath string) (string, error) {
	_log := logger.Default()
	possiblePaths := []string{
		t.OriginalFilename,
		t.Filename,
		utils.RemoveExtension(t.OriginalFilename),
	}

	for _, path := range possiblePaths {
		_p := filepath.Join(rClonePath, path)
		_log.Trace().Msgf("Checking path: %s", _p)
		_, err := os.Stat(_p)
		if !os.IsNotExist(err) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no path found")
}

func (t *Torrent) GetFile(filename string) (File, bool) {
	f, ok := t.Files[filename]
	if !ok {
		return File{}, false
	}
	return f, !f.Deleted
}

func (t *Torrent) GetFiles() []File {
	files := make([]File, 0, len(t.Files))
	for _, f := range t.Files {
		if !f.Deleted {
			files = append(files, f)
		}
	}
	return files
}

type File struct {
	TorrentId    string        `json:"torrent_id"`
	Id           string        `json:"id"`
	Name         string        `json:"name"`
	Size         int64         `json:"size"`
	IsRar        bool          `json:"is_rar"`
	ByteRange    *[2]int64     `json:"byte_range,omitempty"`
	Path         string        `json:"path"`
	Link         string        `json:"link"`
	AccountId    string        `json:"account_id"`
	Generated    time.Time     `json:"generated"`
	Deleted      bool          `json:"deleted"`
	DownloadLink *DownloadLink `json:"-"`
}

func (t *Torrent) Cleanup(remove bool) {
	if remove {
		err := os.Remove(t.Filename)
		if err != nil {
			return
		}
	}
}

type IngestData struct {
	Debrid string `json:"debrid"`
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	Size   int64  `json:"size"`
}

type LibraryStats struct {
	Total       int `json:"total"`
	Bad         int `json:"bad"`
	ActiveLinks int `json:"active_links"`
}

type Stats struct {
	Profile  *Profile         `json:"profile"`
	Library  LibraryStats     `json:"library"`
	Accounts []map[string]any `json:"accounts"`
}

type Profile struct {
	Name       string    `json:"name"`
	Id         int64     `json:"id"`
	Username   string    `json:"username"`
	Email      string    `json:"email"`
	Points     int       `json:"points"`
	Type       string    `json:"type"`
	Premium    int64     `json:"premium"`
	Expiration time.Time `json:"expiration"`
}

type DownloadLink struct {
	Filename     string    `json:"filename"`
	Link         string    `json:"link"`
	DownloadLink string    `json:"download_link"`
	Generated    time.Time `json:"generated"`
	Size         int64     `json:"size"`
	Id           string    `json:"id"`
	ExpiresAt    time.Time
}

func (d *DownloadLink) String() string {
	return d.DownloadLink
}
