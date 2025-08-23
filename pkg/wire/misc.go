package wire

import (
	"os"
	"path/filepath"
	"strings"
)

func createTorrentFromMagnet(req *ImportRequest) *Torrent {
	magnet := req.Magnet
	arrName := req.Arr.Name
	torrent := &Torrent{
		ID:        req.Id,
		Hash:      strings.ToLower(magnet.InfoHash),
		Name:      magnet.Name,
		Size:      magnet.Size,
		Category:  arrName,
		Source:    string(req.Type),
		State:     "downloading",
		MagnetUri: magnet.Link,

		Tracker:    "udp://tracker.opentrackr.org:1337",
		UpLimit:    -1,
		DlLimit:    -1,
		AutoTmm:    false,
		Ratio:      1,
		RatioLimit: 1,
		SavePath:   filepath.Join(req.DownloadFolder, arrName) + string(os.PathSeparator),
	}
	return torrent
}
