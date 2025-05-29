package types

import (
	"github.com/rs/zerolog"
)

type Client interface {
	SubmitMagnet(tr *Torrent) (*Torrent, error)
	CheckStatus(tr *Torrent, isSymlink bool) (*Torrent, error)
	GenerateDownloadLinks(tr *Torrent) error
	GetDownloadLink(tr *Torrent, file *File) (*DownloadLink, error)
	DeleteTorrent(torrentId string) error
	IsAvailable(infohashes []string) map[string]bool
	GetCheckCached() bool
	GetDownloadUncached() bool
	UpdateTorrent(torrent *Torrent) error
	GetTorrent(torrentId string) (*Torrent, error)
	GetTorrents() ([]*Torrent, error)
	GetName() string
	GetLogger() zerolog.Logger
	GetDownloadingStatus() []string
	GetDownloads() (map[string]DownloadLink, error)
	CheckLink(link string) error
	GetMountPath() string
	DisableAccount(string)
	ResetActiveDownloadKeys()
	DeleteDownloadLink(linkId string) error
	GetProfile() (*Profile, error)
}
