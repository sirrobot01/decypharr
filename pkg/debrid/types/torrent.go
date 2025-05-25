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

	Debrid string `json:"debrid"`

	Arr              *arr.Arr   `json:"arr"`
	Mu               sync.Mutex `json:"-"`
	SizeDownloaded   int64      `json:"-"` // This is used for local download
	DownloadUncached bool       `json:"-"`
}

type DownloadLink struct {
	Filename     string    `json:"filename"`
	Link         string    `json:"link"`
	DownloadLink string    `json:"download_link"`
	Generated    time.Time `json:"generated"`
	Size         int64     `json:"size"`
	Id           string    `json:"id"`
	AccountId    string    `json:"account_id"`
}

func (d *DownloadLink) String() string {
	return d.DownloadLink
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

type File struct {
	TorrentId    string        `json:"torrent_id"`
	Id           string        `json:"id"`
	Name         string        `json:"name"`
	Size         int64         `json:"size"`
	IsRar        bool          `json:"is_rar"`
	ByteRange    *[2]int64     `json:"byte_range,omitempty"`
	Path         string        `json:"path"`
	Link         string        `json:"link"`
	DownloadLink *DownloadLink `json:"-"`
	AccountId    string        `json:"account_id"`
	Generated    time.Time     `json:"generated"`
}

func (t *Torrent) Cleanup(remove bool) {
	if remove {
		err := os.Remove(t.Filename)
		if err != nil {
			return
		}
	}
}

func (t *Torrent) GetFile(id string) *File {
	for _, f := range t.Files {
		if f.Id == id {
			return &f
		}
	}
	return nil
}

type Account struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	Name     string `json:"name"`
	Token    string `json:"token"`
}
